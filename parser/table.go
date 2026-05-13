package parser

import (
	"github.com/wufe/loglens/line"
	"strings"
	"unicode"
)

// minTableColumns is the smallest column count we treat as a table. Two-column
// "tables" (a prefix gap plus the rest of the line) are almost always a log
// preamble — e.g. `[LLB:123]    content` or `[ts]  msg` — not tabular data.
// Real tables in log output (kubectl get, go test summaries, kuttl events)
// have three or more columns, so requiring ≥3 eliminates that class of false
// positive without costing anything in practice.
const minTableColumns = 3

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
	// Tab-indented free-form text (error traces, stack traces) has tabs used
	// for alignment rather than column separation. We detect it up front so a
	// sequence of such lines doesn't accumulate into a fake table group.
	if len(tabGaps) > 0 && looksLikeTabIndentation(raw, tabGaps) {
		tt.reset()
		return nil
	}

	var boundaries []int
	if len(tabGaps) > 0 {
		boundaries = tabGapBoundaries(raw, tabGaps)
	} else {
		boundaries = computeColumnBoundaries(raw)
	}

	if len(boundaries) < minTableColumns {
		tt.reset()
		return nil
	}

	// Check alignment with previous line. For tab-based groups the tab stops
	// give us unambiguous column positions, so just two matching boundaries is
	// a reliable signal (and tolerates drift when variable object-name lengths
	// push later tabs to different stops). Space-gap detection is much noisier
	// — e.g. `[LLB:N] ...log... === RUN   Test` and `[LLB:N] ...log... printer.go:57:`
	// both happen to produce a gap in roughly the same area but they aren't a
	// table — so we require the full minTableColumns to match there.
	if tt.prevLineIdx == lineIdx-1 && tt.prevBoundaries != nil {
		matching := countMatchingBoundaries(tt.prevBoundaries, boundaries)
		matchThreshold := 2
		if len(tabGaps) == 0 {
			matchThreshold = minTableColumns
		}
		if matching >= matchThreshold {
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

// looksLikeTabIndentation returns true when the row's tab-separated cells
// suggest the tabs are being used for alignment/indentation, not columns.
// Rule: if the row has middle cells (cells between consecutive tabs) and
// none of them contain any non-whitespace content, the tabs are producing
// pure indentation — think httpexpect's `…\t            \trequest: POST …`
// (one whitespace-only middle cell) or its stack-trace cousin
// `…\t            \t\t\t\t/path` (four whitespace-only middle cells in a
// row). A real table row always has at least one middle column with data;
// kubectl events may leave one intentionally blank (`\t\t` between Object
// and Reason) but never leave every middle column empty in the same row.
//
// Leading (cell 0) and trailing (cell N) cells are not inspected — leading
// whitespace before the first tab is normal indentation, and a trailing
// empty cell is rare but benign.
func looksLikeTabIndentation(raw string, gaps []line.TabGap) bool {
	if len(gaps) < 2 {
		return false
	}
	prevEnd := gaps[0].End
	for i := 1; i < len(gaps); i++ {
		cell := raw[prevEnd:gaps[i].Start]
		if strings.TrimSpace(cell) != "" {
			return false
		}
		prevEnd = gaps[i].End
	}
	return true
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
