package parser

import (
	"bytes"
	"encoding/json"
	"loglens/line"
	"strings"
)

const defaultBufferSize = 50

// Buffer is a ring buffer of recent lines for multi-line construct detection.
type Buffer struct {
	lines []*line.LogLine
	size  int
	head  int
	count int

	nextGroupID int
}

// NewBuffer creates a buffer with the given capacity.
func NewBuffer(size int) *Buffer {
	if size <= 0 {
		size = defaultBufferSize
	}
	return &Buffer{
		lines:       make([]*line.LogLine, size),
		size:        size,
		nextGroupID: 1,
	}
}

// Push adds a line to the buffer and returns its absolute index.
func (b *Buffer) Push(l *line.LogLine) int {
	idx := b.head
	b.lines[idx] = l
	b.head = (b.head + 1) % b.size
	if b.count < b.size {
		b.count++
	}
	return idx
}

// Get returns the line at the given ring index.
func (b *Buffer) Get(idx int) *line.LogLine {
	if idx < 0 || idx >= b.size {
		return nil
	}
	return b.lines[idx]
}

// Last returns the last N lines in order (oldest first).
func (b *Buffer) Last(n int) []*line.LogLine {
	if n > b.count {
		n = b.count
	}
	result := make([]*line.LogLine, 0, n)
	start := (b.head - n + b.size) % b.size
	for i := 0; i < n; i++ {
		idx := (start + i) % b.size
		if b.lines[idx] != nil {
			result = append(result, b.lines[idx])
		}
	}
	return result
}

// Count returns how many lines are in the buffer.
func (b *Buffer) Count() int {
	return b.count
}

// AllocGroupID returns a new unique group ID.
func (b *Buffer) AllocGroupID() int {
	id := b.nextGroupID
	b.nextGroupID++
	return id
}

// maxMultiLineScanBack limits how far backward DetectMultiLineJSON scans.
// Multi-line JSON constructs spanning more than 200 lines are impractical;
// this bound also allows the parser to release old line references for GC.
const maxMultiLineScanBack = 200

// DetectMultiLineJSON scans backward from the most recently pushed line
// to detect a complete multi-line JSON construct. Returns the indices into
// the global lines slice that should be reparsed, or nil.
func (b *Buffer) DetectMultiLineJSON(allLines []*line.LogLine) []int {
	n := len(allLines)
	if n == 0 {
		return nil
	}

	lastLine := allLines[n-1]
	trimmed := strings.TrimSpace(lastLine.Raw)

	// Only trigger on closing brace/bracket
	if trimmed != "}" && trimmed != "]" {
		return nil
	}

	closingChar := trimmed[0]
	var openingChar byte
	if closingChar == '}' {
		openingChar = '{'
	} else {
		openingChar = '['
	}

	// Scan backward through recent lines to find a matching opener.
	// Limited to maxMultiLineScanBack so older nil'd entries are never touched.
	depth := 0
	startIdx := -1
	minIdx := max(0, n-maxMultiLineScanBack)

	for i := n - 1; i >= minIdx; i-- {
		if allLines[i] == nil {
			break
		}
		lt := strings.TrimSpace(allLines[i].Raw)
		// Count braces
		for j := len(lt) - 1; j >= 0; j-- {
			if lt[j] == closingChar {
				depth++
			} else if lt[j] == openingChar {
				depth--
				if depth == 0 {
					startIdx = i
					break
				}
			}
		}
		if startIdx >= 0 {
			break
		}
	}

	if startIdx < 0 || startIdx == n-1 {
		return nil
	}

	// Already classified?
	if allLines[startIdx].Type == line.TypeJSON {
		return nil
	}

	// Check if the first line has prefix text before the opening brace.
	// Find the position of the opening brace in the raw line.
	firstRaw := allLines[startIdx].Raw
	bracePos := strings.LastIndexByte(firstRaw, openingChar)
	if bracePos < 0 {
		return nil
	}

	prefix := ""
	firstLineJSON := firstRaw
	if strings.TrimSpace(firstRaw[:bracePos]) != "" {
		// There's text before the brace — extract prefix and JSON portion
		prefix = firstRaw[:bracePos]
		firstLineJSON = firstRaw[bracePos:]
	}

	// Concatenate and validate JSON
	var sb strings.Builder
	sb.WriteString(firstLineJSON)
	sb.WriteByte('\n')
	for i := startIdx + 1; i < n; i++ {
		sb.WriteString(allLines[i].Raw)
		sb.WriteByte('\n')
	}
	combined := sb.String()

	parsed, ok := parseJSONAny([]byte(combined))
	if !ok {
		return nil
	}

	// Mark all lines as TypeJSON with the same group
	groupID := b.AllocGroupID()
	indices := make([]int, 0, n-startIdx)

	for i := startIdx; i < n; i++ {
		allLines[i].Type = line.TypeJSON
		allLines[i].GroupID = groupID
		allLines[i].GroupHead = (i == startIdx)
		allLines[i].Expandable = (i == startIdx)
		if i == startIdx {
			rawBytes := []byte(combined)
			allLines[i].Expanded = true
			allLines[i].Meta = &line.JSONMeta{
				Value:   parsed,
				Summary: summarizeJSON(combined),
				Keys:    extractOrderedKeys(rawBytes),
				RawJSON: rawBytes,
				Prefix:  prefix,
			}
		} else {
			// Clear any stale meta from inner group detections
			allLines[i].Meta = nil
			allLines[i].Children = nil
			allLines[i].Expanded = false
			// Ensure segments exist for highlighting
			if allLines[i].Segments == nil {
				allLines[i].Segments = highlightSegments(allLines[i].Raw)
			}
		}
		indices = append(indices, i)
	}

	return indices
}

func summarizeJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 60 {
		return trimmed[:57] + "..."
	}
	// Compact it
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(trimmed)); err != nil {
		return trimmed
	}
	result := out.String()
	if len(result) > 60 {
		return result[:57] + "..."
	}
	return result
}
