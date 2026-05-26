package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wufe/loglens/pattern"
)

// patternsAreaHeight returns the rows allotted to the patterns container at
// the current window/layout, or 0 when it should be hidden.
//
// Sizing rule: header row + one row per current pattern, capped at
// patternsMaxAreaHeight() = m.height/4. The exact row count is recomputed by
// View at the end of each render and stashed in sharedState; reads here
// pick up that stashed value so the layout shrinks as soon as the pattern
// count drops (one-frame lag on transitions).
//
// First-time toggle (no stashed value yet) defaults to the maximum so the
// log viewport reserves space upfront, avoiding a one-frame flash.
func (m model) patternsAreaHeight() int {
	if !m.patternsVisible {
		return 0
	}
	// When the user has zoomed stats to full-screen, hide the patterns pane
	// to avoid stealing rows from a deliberately-focused stats view.
	if m.statsLayout == statsLayoutFullStats &&
		m.statsMgr != nil && len(m.statsMgr.All()) > 0 {
		return 0
	}
	maxH := m.patternsMaxAreaHeight()
	if maxH < 2 {
		return 0
	}
	stashed := int(m.s.patternsPaneHeight.Load())
	if stashed <= 0 {
		// No prior render has measured the actual content yet (just
		// toggled on, or layout just changed). Reserve the max so we
		// don't flash a too-small pane on the next frame.
		return maxH
	}
	if stashed > maxH {
		return maxH
	}
	if stashed < 2 {
		return 2
	}
	return stashed
}

// patternsMaxAreaHeight returns the upper bound on patterns-pane height —
// 1/4 of the terminal height, with a 2-row floor (header + one row). Kept
// separate so View can use it directly for the pre-render layout pass
// without depending on the stashed live value.
func (m model) patternsMaxAreaHeight() int {
	if !m.patternsVisible {
		return 0
	}
	if m.statsLayout == statsLayoutFullStats &&
		m.statsMgr != nil && len(m.statsMgr.All()) > 0 {
		return 0
	}
	avail := m.height - 1
	if m.searchMode {
		avail--
	}
	if avail < 4 {
		return 0
	}
	maxH := m.height / 4
	if maxH < 2 {
		maxH = 2
	}
	// Don't exceed the available space (mostly defensive on tiny terminals).
	if maxH > avail-m.statsAreaHeight() {
		maxH = avail - m.statsAreaHeight()
	}
	if maxH < 2 {
		return 0
	}
	return maxH
}

// updatePatternsMode handles key events while the patterns pane has focus.
// Mirrors updateStatsMode: a narrow keyset (j/k navigate, tab/esc returns to
// logs, p toggles the pane off, q quits). Other keys are ignored on purpose
// so the user doesn't accidentally drive the log viewport while reading the
// pattern list.
func (m model) updatePatternsMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if isKeyQuit(msg) {
		if m.ingestor != nil {
			m.ingestor.stop()
		}
		if m.inputSrc != nil {
			m.inputSrc.Stop()
		}
		if m.statsMgr != nil {
			m.statsMgr.Stop()
		}
		m.s.store.Close()
		pattern.ClearCache()
		return m, tea.Quit
	}

	// Pattern list depends on what's visible; count it once so navigation
	// bounds are sane even when the user scrolls the underlying logs.
	pats, _ := m.visiblePatterns()

	switch {
	case msg.String() == "tab" || msg.String() == "esc":
		m.patternsFocused = false
	case msg.String() == "p":
		// Toggling off from the pane returns focus to the logs.
		m.patternsVisible = false
		m.patternsFocused = false
	case isKeyUp(msg):
		if m.patternCursor > 0 {
			m.patternCursor--
		}
	case isKeyDown(msg):
		if m.patternCursor < len(pats)-1 {
			m.patternCursor++
		}
	}
	if m.patternCursor >= len(pats) {
		m.patternCursor = max0(len(pats) - 1)
	}
	return m, nil
}

// visiblePatterns computes the patterns over the *largest* viewport
// the pane could ever occupy (i.e. assuming the pane shrinks to its
// minimum two rows). Using a stable window decouples the pattern set
// from the pane height, which removes the feedback loop responsible for
// the layout flicker seen with wrap-off:
//
//   - With a vh-dependent window, every pane resize changed the visible
//     line count, which changed the pattern count, which changed the
//     pane size next frame — an oscillation cycle.
//   - With a fixed window, the pattern set depends only on m.offset and
//     the terminal size, both of which are constant across consecutive
//     frames. The pane settles in one frame and stays there.
//
// Some patterns may correspond to lines that fall in the pane area
// (off-screen) at the chosen pane size; their LineIndices are still
// valid, the row-highlight pass just silently skips indices that have
// no rendered row.
func (m model) visiblePatterns() (pats []pattern.Pattern, storeIdx []int) {
	if !m.patternsVisible {
		return nil, nil
	}
	raws, storeIdx := m.patternsStableSnapshot()
	if len(raws) == 0 {
		return nil, nil
	}
	return pattern.ExtractPatterns(raws), storeIdx
}

// patternsStableSnapshot is like visibleLineSnapshot but walks far enough
// to cover the largest viewport the pane could ever leave behind (total
// avail minus minimum pane height = avail - 2). The result is stable
// across pane-height changes, so feeding it to ExtractPatterns produces
// a stable pattern set frame to frame.
func (m model) patternsStableSnapshot() (raws []string, storeIdx []int) {
	if m.s == nil || m.s.store == nil {
		return nil, nil
	}
	m.s.mu.RLock()
	defer m.s.mu.RUnlock()
	n := m.s.store.Len()
	if n == 0 {
		return nil, nil
	}
	// Use the largest possible viewport (assume the pane will shrink to its
	// 2-row minimum). The window we walk is therefore the same regardless
	// of how big the pane ends up being.
	avail := m.height - 1
	if m.searchMode {
		avail--
	}
	vh := avail - m.statsAreaHeight() - 2
	if vh <= 0 {
		return nil, nil
	}
	rows := 0
	raws = make([]string, 0, vh)
	storeIdx = make([]int, 0, vh)
	for i := m.offset; i < n && rows < vh; i++ {
		if m.s.store.IsHiddenGroupMember(i) {
			continue
		}
		isCursor := i == m.cursor
		var cursorPath []int
		if isCursor {
			cursorPath = m.cursorPath
		}
		visRows := visualRowsForLineStatic(m.s.store, i, m.s.width, m.s.wrapMode, isCursor, cursorPath)
		if i == m.offset {
			visRows -= m.offsetRow
		}
		if visRows < 1 {
			visRows = 1
		}
		raws = append(raws, m.s.store.Get(i).Raw)
		storeIdx = append(storeIdx, i)
		rows += visRows
	}
	return raws, storeIdx
}

// matchedLineIndices returns the set of store-line indices whose skeleton
// equals the focused pattern's. Returns nil when the pane is hidden or no
// pattern is selected — callers should treat a nil result as "no highlight".
//
// storeIdx must be the slice returned by visibleLineSnapshot for the same
// pattern set; a pattern's LineIndices are positions into that window.
func (m model) matchedLineIndices(pats []pattern.Pattern, storeIdx []int) map[int]bool {
	if !m.patternsVisible || len(pats) == 0 {
		return nil
	}
	if m.patternCursor < 0 || m.patternCursor >= len(pats) {
		return nil
	}
	li := pats[m.patternCursor].LineIndices
	want := make(map[int]bool, len(li))
	for _, idx := range li {
		if idx >= 0 && idx < len(storeIdx) {
			want[storeIdx[idx]] = true
		}
	}
	return want
}

// renderPatternsPane produces the bottom-most pane string. Returns "" when
// the pane is hidden. Reads pats so the caller can share the same Pattern
// slice with the row-highlight pass — avoids running ExtractPatterns twice
// per render.
func (m model) renderPatternsPane(pats []pattern.Pattern) string {
	height := m.patternsAreaHeight()
	if height <= 0 {
		return ""
	}

	headerStyle := lipgloss.NewStyle().Faint(true)
	focusedHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))

	header := "  PATTERNS  (p to close, tab to focus)"
	if m.patternsFocused {
		header = focusedHeader.Render("▸ PATTERNS (focused — j/k to navigate)")
	} else {
		header = headerStyle.Render(header)
	}
	scrollHint := ""
	if len(pats) > 0 {
		scrollHint = headerStyle.Render(fmt.Sprintf("[%d/%d]", m.patternCursor+1, len(pats)))
	} else {
		scrollHint = headerStyle.Render("[0/0]")
	}
	gap := m.width - lipgloss.Width(header) - lipgloss.Width(scrollHint) - 2
	if gap < 1 {
		gap = 1
	}
	headerLine := " " + header + strings.Repeat(" ", gap) + scrollHint + " "
	headerLine = lipgloss.NewStyle().Width(m.width).Render(headerLine)

	bodyHeight := height - 1
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	// Keep the cursor in view: drag the window when it scrolls past edges.
	if m.patternCursor < m.patternBoxOffset {
		m.patternBoxOffset = m.patternCursor
	}
	if m.patternCursor >= m.patternBoxOffset+bodyHeight {
		m.patternBoxOffset = m.patternCursor - bodyHeight + 1
	}
	if m.patternBoxOffset < 0 {
		m.patternBoxOffset = 0
	}

	rowStyle := lipgloss.NewStyle()
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("53")).Foreground(lipgloss.Color("231")).Bold(true)
	countStyle := lipgloss.NewStyle().Faint(true)
	blankRow := lipgloss.NewStyle().Width(m.width).Render("")

	// Build the pane as a fixed-length slice of lines and join with "\n" at
	// the end. Earlier versions appended \n's piecewise and double-counted
	// the separator between the last rendered row and the padding region,
	// overflowing patternsAreaHeight by one row and pushing the top log line
	// off-screen.
	lines := make([]string, 0, height)
	lines = append(lines, headerLine)

	if len(pats) == 0 {
		empty := lipgloss.NewStyle().Width(m.width).Render(
			headerStyle.Render("  (no visible lines)"))
		lines = append(lines, empty)
		for len(lines) < height {
			lines = append(lines, blankRow)
		}
		return strings.Join(lines, "\n")
	}

	end := m.patternBoxOffset + bodyHeight
	if end > len(pats) {
		end = len(pats)
	}
	for i := m.patternBoxOffset; i < end; i++ {
		p := pats[i]
		count := fmt.Sprintf("%d×", len(p.LineIndices))
		marker := "  "
		if m.patternsFocused && i == m.patternCursor {
			marker = "▸ "
		}
		// Fixed-width count cell (4 chars + 1 space) so different counts line
		// up vertically and the template column always starts at the same x.
		countCell := fmt.Sprintf("%-4s ", count)
		template := p.Template
		// Width budget: full width minus marker (2) minus count cell (5) minus 1 right padding.
		maxTpl := m.width - 2 - 5 - 1
		if maxTpl > 0 && lipgloss.Width(template) > maxTpl {
			template = truncate(template, maxTpl)
		}
		row := marker + countStyle.Render(countCell) + template
		if m.patternsFocused && i == m.patternCursor {
			row = selectedStyle.Width(m.width).Render(row)
		} else {
			row = rowStyle.Width(m.width).Render(row)
		}
		lines = append(lines, row)
	}
	for len(lines) < height {
		lines = append(lines, blankRow)
	}
	return strings.Join(lines, "\n")
}

// max0 returns x clamped at 0 below. Avoids importing "max" just for this.
func max0(x int) int {
	if x < 0 {
		return 0
	}
	return x
}
