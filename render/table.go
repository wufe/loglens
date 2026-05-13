package render

import (
	"github.com/wufe/loglens/line"
	"strings"
)

// renderTableFormatted takes a pre-highlighted string and inserts dim │ separators
// at column boundaries defined by the table header.
//
// Tab-delimited tables (meta.TabGaps set, meta.GroupState set): each cell is
// padded to the max width observed across the group so columns align even
// when individual fields vary in length. See renderTableAligned.
//
// Legacy space-gap tables (TabGaps empty): the header's multi-space gap
// positions (meta.Columns) define separator sites; we replace those in-place
// without padding.
func renderTableFormatted(l *line.LogLine, highlighted string, styles *Styles) string {
	meta, ok := l.Meta.(*line.TableMeta)
	if !ok || len(meta.Columns) < 2 {
		return highlighted
	}

	if len(meta.TabGaps) > 0 && meta.GroupState != nil && len(meta.GroupState.ColWidths) > 0 {
		return renderTableAligned(highlighted, meta, styles)
	}

	raw := l.Raw

	// Find gaps to replace with column separators. When the source used tabs
	// but no shared group state is available (shouldn't normally happen), we
	// still insert separators at each tab gap without padding.
	type gap struct{ start, end int }
	var replaceGaps []gap

	if len(meta.TabGaps) > 0 {
		replaceGaps = make([]gap, 0, len(meta.TabGaps))
		for _, tg := range meta.TabGaps {
			if tg.Start == 0 || tg.End <= tg.Start {
				continue
			}
			replaceGaps = append(replaceGaps, gap{tg.Start, tg.End})
		}
	} else {
		// Find multi-space gaps (>=2 consecutive spaces) in the raw line.
		var allGaps []gap
		i := 0
		for i < len(raw) {
			if raw[i] == ' ' {
				start := i
				for i < len(raw) && raw[i] == ' ' {
					i++
				}
				if i-start >= 2 {
					allGaps = append(allGaps, gap{start, i})
				}
			} else {
				i++
			}
		}

		if len(allGaps) == 0 {
			return highlighted
		}

		// Only replace gaps that correspond to header column boundaries.
		for _, col := range meta.Columns[1:] {
			for _, g := range allGaps {
				if g.start == 0 {
					continue
				}
				if col >= g.start && col <= g.end {
					replaceGaps = append(replaceGaps, g)
					break
				}
			}
		}
	}

	if len(replaceGaps) == 0 {
		return highlighted
	}

	// Build a set of visible positions that fall inside a gap.
	// For each gap, record what to emit at gap start.
	type gapReplace struct {
		gapWidth int
	}
	gapStarts := make(map[int]gapReplace)
	gapPositions := make(map[int]bool)
	for _, g := range replaceGaps {
		gapStarts[g.start] = gapReplace{gapWidth: g.end - g.start}
		for p := g.start; p < g.end; p++ {
			gapPositions[p] = true
		}
	}

	// Walk the highlighted string, tracking visible character position.
	// Copy everything except gap characters; at gap start, insert │ padding.
	var sb strings.Builder
	visPos := 0
	runes := []rune(highlighted)
	j := 0

	for j < len(runes) {
		ch := runes[j]

		// Check for ANSI escape sequence: ESC [ ... final_byte
		if ch == '\x1b' && j+1 < len(runes) && runes[j+1] == '[' {
			// Copy entire escape sequence
			start := j
			j += 2 // skip ESC [
			for j < len(runes) && !isANSIFinalByte(runes[j]) {
				j++
			}
			if j < len(runes) {
				j++ // include final byte
			}
			sb.WriteString(string(runes[start:j]))
			continue
		}

		// Check for OSC sequence: ESC ] ... ST
		if ch == '\x1b' && j+1 < len(runes) && runes[j+1] == ']' {
			start := j
			j += 2
			for j < len(runes) {
				if runes[j] == '\x1b' && j+1 < len(runes) && runes[j+1] == '\\' {
					j += 2
					break
				}
				if runes[j] == '\x07' {
					j++
					break
				}
				j++
			}
			sb.WriteString(string(runes[start:j]))
			continue
		}

		// Visible character
		if gr, ok := gapStarts[visPos]; ok {
			// At gap start: emit (gapWidth-1) spaces, then │ flush against the
			// next column. Putting │ at end-of-gap makes separators align
			// vertically across rows even when gap widths differ (e.g. tab
			// after "Normal" is 2 chars, after "Warning" is 1 char, but the
			// next column starts at the same tab-stop column on both rows).
			if gr.gapWidth > 1 {
				sb.WriteString(strings.Repeat(" ", gr.gapWidth-1))
			}
			sb.WriteString(styles.TableSep.Render("│"))

			// Skip runes that correspond to gap positions
			for j < len(runes) && gapPositions[visPos] {
				ch = runes[j]
				if ch == '\x1b' {
					// Skip ANSI sequences within the gap — still copy them
					start := j
					if j+1 < len(runes) && runes[j+1] == '[' {
						j += 2
						for j < len(runes) && !isANSIFinalByte(runes[j]) {
							j++
						}
						if j < len(runes) {
							j++
						}
					} else {
						j++
					}
					sb.WriteString(string(runes[start:j]))
					continue
				}
				visPos++
				j++
			}
			continue
		}

		// Normal visible character outside any gap
		sb.WriteRune(ch)
		visPos++
		j++
	}

	return sb.String()
}

// isANSIFinalByte returns true if the rune is an ANSI CSI final byte (0x40-0x7E).
func isANSIFinalByte(r rune) bool {
	return r >= 0x40 && r <= 0x7E
}

// renderTableAligned walks the highlighted string cell-by-cell (delimited by
// meta.TabGaps), pads each cell to the corresponding max width from the
// shared GroupState, and emits a │ separator at the end of every cell.
// Since every row in the group points at the same GroupState, a later row
// that widens column N instantly widens that column on every prior row's
// next render — columns stay aligned in a streaming view.
func renderTableAligned(highlighted string, meta *line.TableMeta, styles *Styles) string {
	tabGaps := meta.TabGaps
	colWidths := meta.GroupState.ColWidths

	sep := styles.TableSep.Render("│")

	var sb strings.Builder
	runes := []rune(highlighted)
	j := 0
	visPos := 0 // visible position in raw
	cellStart := 0
	gapIdx := 0 // tabGaps cursor — each gap is handled exactly once

	for j < len(runes) {
		ch := runes[j]

		// CSI escape: ESC [ ... final_byte
		if ch == '\x1b' && j+1 < len(runes) && runes[j+1] == '[' {
			start := j
			j += 2
			for j < len(runes) && !isANSIFinalByte(runes[j]) {
				j++
			}
			if j < len(runes) {
				j++
			}
			sb.WriteString(string(runes[start:j]))
			continue
		}
		// OSC escape: ESC ] ... ST
		if ch == '\x1b' && j+1 < len(runes) && runes[j+1] == ']' {
			start := j
			j += 2
			for j < len(runes) {
				if runes[j] == '\x1b' && j+1 < len(runes) && runes[j+1] == '\\' {
					j += 2
					break
				}
				if runes[j] == '\x07' {
					j++
					break
				}
				j++
			}
			sb.WriteString(string(runes[start:j]))
			continue
		}

		if gapIdx < len(tabGaps) && visPos == tabGaps[gapIdx].Start {
			g := tabGaps[gapIdx]
			// Cell gapIdx just ended. If the column is always empty across
			// the group (two consecutive tabs in the source, e.g. kubectl's
			// ^I^I between Object and Reason), suppress the separator so we
			// don't render a redundant ││ pair.
			emitSep := !(gapIdx < len(colWidths) && colWidths[gapIdx] == 0)
			if emitSep {
				contentW := visPos - cellStart
				if gapIdx < len(colWidths) && colWidths[gapIdx] > contentW {
					sb.WriteString(strings.Repeat(" ", colWidths[gapIdx]-contentW))
				}
				sb.WriteString(sep)
			}

			// Skip over the gap's runes in the highlighted string (preserving
			// any ANSI escapes that happen to land inside the gap), so visPos
			// advances past the original expansion.
			for j < len(runes) && visPos < g.End {
				ch2 := runes[j]
				if ch2 == '\x1b' && j+1 < len(runes) && runes[j+1] == '[' {
					start := j
					j += 2
					for j < len(runes) && !isANSIFinalByte(runes[j]) {
						j++
					}
					if j < len(runes) {
						j++
					}
					sb.WriteString(string(runes[start:j]))
					continue
				}
				visPos++
				j++
			}
			gapIdx++
			cellStart = visPos
			continue
		}

		sb.WriteRune(ch)
		visPos++
		j++
	}

	// Final cell after the last tab gap: pad to its max width so the row
	// length stays consistent across the group (e.g. when one row has extra
	// trailing content others don't).
	lastIdx := len(tabGaps)
	if lastIdx < len(colWidths) {
		contentW := visPos - cellStart
		if colWidths[lastIdx] > contentW {
			sb.WriteString(strings.Repeat(" ", colWidths[lastIdx]-contentW))
		}
	}

	return sb.String()
}
