package parser

import (
	"loglens/line"
	"unicode"
)

// TableTracker detects aligned column tables across consecutive lines.
type TableTracker struct {
	prevBoundaries   []int
	prevTabGaps      []line.TabGap
	prevRaw          string
	headerBoundaries []int // boundaries from the header row, used for all rows in group
	headerTabGaps    []line.TabGap
	prevLineIdx      int
	groupID          int
	count            int

	// groupState is the shared max-width tracker for the active tab-delimited
	// group. Each row's TableMeta points at this same struct so widening one
	// row's column retroactively widens every already-parsed row on next
	// render. nil until the group is confirmed (count == 2) or if the group
	// was detected from multi-space gaps (no tabs).
	groupState *line.TableGroupState
}

// NewTableTracker creates a new table tracker.
func NewTableTracker() *TableTracker {
	return &TableTracker{prevLineIdx: -1}
}

// Feed checks if the current line forms a table with previous lines.
// Returns table metadata if it matches, nil otherwise. When tabGaps is
// non-empty, those positions are used as authoritative column boundaries
// (tabs are unambiguous column delimiters); otherwise we fall back to
// detecting ≥2-space gaps.
func (tt *TableTracker) Feed(raw string, tabGaps []line.TabGap, lineIdx int, buf *Buffer, allLines []*line.LogLine) *line.TableMeta {
	var boundaries []int
	if len(tabGaps) > 0 {
		boundaries = tabGapBoundaries(raw, tabGaps)
	} else {
		boundaries = computeColumnBoundaries(raw)
	}

	// Need at least 2 columns (so at least 2 boundaries including position 0)
	if len(boundaries) < 2 {
		tt.reset()
		return nil
	}

	// Check alignment with previous line
	if tt.prevLineIdx == lineIdx-1 && tt.prevBoundaries != nil {
		matching := countMatchingBoundaries(tt.prevBoundaries, boundaries)
		// Need at least 2 matching column boundaries
		if matching >= 2 {
			tt.count++
			if tt.count == 2 {
				// This is the second consecutive matching line — retroactively mark the first
				tt.groupID = buf.AllocGroupID()
				tt.headerBoundaries = tt.prevBoundaries
				tt.headerTabGaps = tt.prevTabGaps
				// Initialize shared group state from header row widths.
				if len(tt.prevTabGaps) > 0 {
					tt.groupState = &line.TableGroupState{}
					updateGroupWidths(tt.groupState, tt.prevRaw, tt.prevTabGaps)
				} else {
					tt.groupState = nil
				}
				if tt.prevLineIdx >= 0 && tt.prevLineIdx < len(allLines) {
					prev := allLines[tt.prevLineIdx]
					if prev != nil && prev.Type == line.TypePlain {
						prev.Type = line.TypeTable
						prev.GroupID = tt.groupID
						prev.GroupHead = true
						prev.Meta = &line.TableMeta{
							Columns:    tt.headerBoundaries,
							IsHeader:   true,
							TabGaps:    tt.prevTabGaps,
							GroupState: tt.groupState,
						}
					}
				}
			}
			if tt.count >= 2 {
				if tt.groupState != nil {
					updateGroupWidths(tt.groupState, raw, tabGaps)
				}
				tt.prevBoundaries = boundaries
				tt.prevTabGaps = tabGaps
				tt.prevRaw = raw
				tt.prevLineIdx = lineIdx
				return &line.TableMeta{
					Columns:    tt.headerBoundaries,
					IsHeader:   false,
					TabGaps:    tabGaps,
					GroupState: tt.groupState,
				}
			}
		} else {
			tt.reset()
		}
	} else {
		tt.count = 1
	}

	tt.prevBoundaries = boundaries
	tt.prevTabGaps = tabGaps
	tt.prevRaw = raw
	tt.prevLineIdx = lineIdx
	return nil
}

func (tt *TableTracker) reset() {
	tt.prevBoundaries = nil
	tt.prevTabGaps = nil
	tt.prevRaw = ""
	tt.headerBoundaries = nil
	tt.headerTabGaps = nil
	tt.prevLineIdx = -1
	tt.groupID = 0
	tt.count = 0
	tt.groupState = nil
}

// updateGroupWidths takes the max of existing per-column widths and the cell
// widths of the new row. Cell i content spans [prevEnd, tabGaps[i].Start),
// where prevEnd is 0 for i=0 and tabGaps[i-1].End otherwise. The final cell
// (after the last tab) spans [tabGaps[last].End, len(raw)).
func updateGroupWidths(state *line.TableGroupState, raw string, gaps []line.TabGap) {
	prevEnd := 0
	cellCount := len(gaps) + 1
	if cellCount > len(state.ColWidths) {
		grow := make([]int, cellCount-len(state.ColWidths))
		state.ColWidths = append(state.ColWidths, grow...)
	}
	for i, g := range gaps {
		w := g.Start - prevEnd
		if w > state.ColWidths[i] {
			state.ColWidths[i] = w
		}
		prevEnd = g.End
	}
	lastW := len(raw) - prevEnd
	if lastW > 0 && lastW > state.ColWidths[len(gaps)] {
		state.ColWidths[len(gaps)] = lastW
	}
}

// tabGapBoundaries returns column start positions derived from tab gaps:
// col 0 if the line starts with content, plus the end column of each tab gap.
func tabGapBoundaries(raw string, gaps []line.TabGap) []int {
	bounds := make([]int, 0, len(gaps)+1)
	if len(raw) > 0 && raw[0] != ' ' && raw[0] != '\t' {
		bounds = append(bounds, 0)
	}
	for _, g := range gaps {
		bounds = append(bounds, g.End)
	}
	return bounds
}

// GroupID returns the current group ID.
func (tt *TableTracker) GroupID() int {
	return tt.groupID
}

// computeColumnBoundaries finds positions where space-to-nonspace transitions
// occur, but only when the gap is >= 2 spaces.
func computeColumnBoundaries(s string) []int {
	if len(s) == 0 {
		return nil
	}

	var boundaries []int
	inSpace := false
	spaceStart := 0

	// Always include position 0 if first char is non-space
	if !unicode.IsSpace(rune(s[0])) {
		boundaries = append(boundaries, 0)
	}

	for i, ch := range s {
		if unicode.IsSpace(ch) {
			if !inSpace {
				inSpace = true
				spaceStart = i
			}
		} else {
			if inSpace {
				gap := i - spaceStart
				if gap >= 2 {
					boundaries = append(boundaries, i)
				}
				inSpace = false
			}
		}
	}

	return boundaries
}

// countMatchingBoundaries counts how many boundaries from a and b are within +/-1 position.
func countMatchingBoundaries(a, b []int) int {
	count := 0
	for _, av := range a {
		for _, bv := range b {
			diff := av - bv
			if diff >= -1 && diff <= 1 {
				count++
				break
			}
		}
	}
	return count
}
