package parser

import (
	"loglens/line"
	"strings"

	"github.com/acarl005/stripansi"
)

// Parser orchestrates all detectors.
type Parser struct {
	buffer       *Buffer
	diffTracker  *DiffTracker
	tableTracker *TableTracker
	allLines     []*line.LogLine
}

// New creates a new Parser.
func New() *Parser {
	return &Parser{
		buffer:       NewBuffer(defaultBufferSize),
		diffTracker:  NewDiffTracker(),
		tableTracker: NewTableTracker(),
	}
}

// ParseResult contains the parsed line and any reparse indices.
type ParseResult struct {
	Line          *line.LogLine
	ReparseIndices []int
}

// Parse processes a raw line and returns a ParseResult.
func (p *Parser) Parse(raw string, fromStderr bool) ParseResult {
	// Strip ANSI codes from input and expand tabs to tab stops
	// (tabs have variable terminal width that breaks width calculations).
	// stripansi runs a regex internally; skip it when there's no ESC byte.
	cleaned := raw
	if strings.IndexByte(cleaned, 0x1b) >= 0 {
		cleaned = stripansi.Strip(cleaned)
	}
	// Replace stray control bytes (CR, NUL, BS, etc.) with a benign placeholder.
	// nginx error_log lines that capture raw network frames (broken headers,
	// PROXY-protocol probes) routinely contain these — left as-is they move
	// the terminal cursor or desync lipgloss's width math, which then corrupts
	// the rendered viewport.
	cleaned = sanitizeControlBytes(cleaned)
	var tabGaps []line.TabGap
	cleaned, tabGaps = expandTabs(cleaned, 8)

	lineIdx := len(p.allLines)

	// Run single-line detectors in priority order

	// 1. Go test markers/results (high priority to avoid diff false positives)
	if l := detectGoTest(cleaned); l != nil {
		l.Raw = cleaned
		l.FromStderr = fromStderr
		l.Segments = highlightSegments(cleaned)
		p.allLines = append(p.allLines, l)
		p.buffer.Push(l)
		// Reset diff tracker since this is not a diff line
		if p.diffTracker.State == DiffHeader1 || p.diffTracker.State == DiffHeader2 {
			p.diffTracker.Reset()
		}
		return ParseResult{Line: l}
	}

	// 2. Diff detection via state machine
	if meta, groupID := p.diffTracker.Feed(cleaned, lineIdx, p.buffer); meta != nil {
		l := &line.LogLine{
			Raw:        cleaned,
			Type:       line.TypeDiff,
			GroupID:    groupID,
			GroupHead:  meta.LineKind == "header-old",
			Expandable: meta.LineKind == "header-old",
			FromStderr: fromStderr,
			Meta:       meta,
		}
		p.allLines = append(p.allLines, l)
		p.buffer.Push(l)
		return ParseResult{Line: l}
	}

	// 3. Warning/Error prefix
	if l := detectWarning(cleaned); l != nil {
		l.FromStderr = fromStderr
		l.Segments = highlightSegments(cleaned)
		p.allLines = append(p.allLines, l)
		p.buffer.Push(l)
		return ParseResult{Line: l}
	}

	// 4. Inline full JSON
	if l := detectInlineJSON(cleaned); l != nil {
		l.FromStderr = fromStderr
		p.allLines = append(p.allLines, l)
		p.buffer.Push(l)
		return ParseResult{Line: l}
	}

	// 5. Embedded JSON in text
	if l := detectEmbeddedJSON(cleaned); l != nil {
		l.FromStderr = fromStderr
		p.allLines = append(p.allLines, l)
		p.buffer.Push(l)
		return ParseResult{Line: l}
	}

	// 6. Default: plain line
	l := &line.LogLine{
		Raw:        cleaned,
		Type:       line.TypePlain,
		FromStderr: fromStderr,
	}

	// Apply inline highlights
	l.Segments = highlightSegments(cleaned)

	p.allLines = append(p.allLines, l)
	p.buffer.Push(l)

	// 7. Table detection (post-append, since it needs access to allLines)
	if l.Type == line.TypePlain {
		if meta := p.tableTracker.Feed(cleaned, tabGaps, lineIdx, p.buffer, p.allLines); meta != nil {
			l.Type = line.TypeTable
			l.GroupID = p.tableTracker.GroupID()
			l.Meta = meta
		}
	}

	// 8. Multi-line JSON detection (buffer-level)
	reparseIndices := p.buffer.DetectMultiLineJSON(p.allLines)

	// Check for multi-line JSON — if the current line was part of a coalesced group,
	// update the return line
	if len(reparseIndices) > 0 {
		// The current line was updated in-place by DetectMultiLineJSON
		l = p.allLines[lineIdx]
	}

	return ParseResult{
		Line:           l,
		ReparseIndices: reparseIndices,
	}
}

// Lines returns all parsed lines.
func (p *Parser) Lines() []*line.LogLine {
	return p.allLines
}

// AppendExternal appends a pre-built LogLine to the parser's internal slice
// without running detectors. The ingestor uses this for synthesized lines
// (e.g. coalesced ffmpeg progress entries) so that parser.allLines and the
// store remain index-aligned — reparseIndices and Search both depend on
// that invariant. The line is pushed into the lookback buffer too, so the
// multi-line JSON detector doesn't underrun when the next raw line arrives.
func (p *Parser) AppendExternal(l *line.LogLine) {
	p.allLines = append(p.allLines, l)
	p.buffer.Push(l)
}

// LastLine returns the most recently parsed line, or nil if none.
func (p *Parser) LastLine() *line.LogLine {
	if len(p.allLines) == 0 {
		return nil
	}
	return p.allLines[len(p.allLines)-1]
}

// LineAt returns the line at the given index.
func (p *Parser) LineAt(idx int) *line.LogLine {
	if idx < 0 || idx >= len(p.allLines) {
		return nil
	}
	return p.allLines[idx]
}

// LineCount returns the total number of lines.
func (p *Parser) LineCount() int {
	return len(p.allLines)
}

// ReleaseOldLines nils out all but the last keepN entries in allLines,
// allowing the GC to reclaim LogLine objects that the store has offloaded
// to disk. The slice length and indices are preserved so that reparseIndices
// returned by DetectMultiLineJSON remain valid.
func (p *Parser) ReleaseOldLines(keepN int) {
	n := len(p.allLines)
	if n <= keepN {
		return
	}
	cutoff := n - keepN
	for i := range cutoff {
		p.allLines[i] = nil
	}
}

// Search finds the next line containing query (case-insensitive) starting from startIdx.
func (p *Parser) Search(query string, startIdx int) int {
	q := strings.ToLower(query)
	for i := startIdx; i < len(p.allLines); i++ {
		if strings.Contains(strings.ToLower(p.allLines[i].Raw), q) {
			return i
		}
	}
	// Wrap around
	for i := 0; i < startIdx && i < len(p.allLines); i++ {
		if strings.Contains(strings.ToLower(p.allLines[i].Raw), q) {
			return i
		}
	}
	return -1
}

// SearchReverse finds the previous line containing query starting from startIdx.
func (p *Parser) SearchReverse(query string, startIdx int) int {
	q := strings.ToLower(query)
	for i := startIdx; i >= 0; i-- {
		if strings.Contains(strings.ToLower(p.allLines[i].Raw), q) {
			return i
		}
	}
	// Wrap around
	for i := len(p.allLines) - 1; i > startIdx; i-- {
		if strings.Contains(strings.ToLower(p.allLines[i].Raw), q) {
			return i
		}
	}
	return -1
}

// sanitizeControlBytes replaces ASCII control bytes (other than tab and
// newline) and DEL with '?'. Tab is preserved for expandTabs; newline shouldn't
// appear here (the ingestor splits on it) but is preserved defensively. The
// substitution is byte-for-byte so segment indices computed downstream stay
// valid.
func sanitizeControlBytes(s string) string {
	hit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f {
			hit = true
			break
		}
	}
	if !hit {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f {
			b[i] = '?'
		}
	}
	return string(b)
}

// expandTabs replaces each tab with spaces to the next tab stop, and returns
// the expanded region for each tab (start = column of first space, end =
// column of next content). When the source uses tabs as column delimiters,
// these gaps are authoritative column boundaries — the table tracker uses
// them instead of the multi-space-gap heuristic so detection isn't fooled
// by variable tab-stop gap widths (a "Warning" field leaves a 1-space gap
// while "Normal" leaves 2, which the heuristic would miss).
func expandTabs(s string, tabWidth int) (string, []line.TabGap) {
	if !strings.Contains(s, "\t") {
		return s, nil
	}
	var sb strings.Builder
	var gaps []line.TabGap
	col := 0
	for _, ch := range s {
		if ch == '\t' {
			spaces := tabWidth - (col % tabWidth)
			gaps = append(gaps, line.TabGap{Start: col, End: col + spaces})
			sb.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			sb.WriteRune(ch)
			col++
		}
	}
	return sb.String(), gaps
}
