package main

import (
	"fmt"
	"loglens/line"
	"loglens/stats"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// updateStatsMode handles key events while the stats pane has focus. Only a
// narrow set of keys is meaningful here — h/l navigate between boxes,
// tab/esc returns focus to the logs, z toggles full-height stats, q quits.
// Everything else is intentionally ignored to avoid accidental log-viewport
// mutations while the user is reading stats.
func (m model) updateStatsMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		return m, tea.Quit
	}
	all := m.statsMgr.All()
	if len(all) == 0 {
		// No stats to focus on — fall back to logs.
		m.statsFocused = false
		return m, nil
	}
	switch {
	case msg.String() == "tab" || msg.String() == "esc":
		m.statsFocused = false
		if m.statsLayout == statsLayoutFullStats {
			m.statsLayout = statsLayoutSplit
			m.adjustOffsetLocked()
		}
	case isKeyZoom(msg):
		if m.statsLayout == statsLayoutFullStats {
			m.statsLayout = statsLayoutSplit
		} else {
			m.statsLayout = statsLayoutFullStats
		}
		m.adjustOffsetLocked()
	case msg.String() == "left" || msg.String() == "h":
		if m.statsBoxFocused > 0 {
			m.statsBoxFocused--
		}
	case msg.String() == "right" || msg.String() == "l":
		if m.statsBoxFocused < len(all)-1 {
			m.statsBoxFocused++
		}
	}
	return m, nil
}

// statsBackfillBatch is the number of lines we read from the store per
// RLock acquisition during backfill. Small batches keep ingestor stalls
// (a write Lock acquisition can wait on us) under one digit of milliseconds
// even when chunks need to be reloaded from disk.
const statsBackfillBatch = 64

// statsBoxWidth is the target width for one frequency-stat box. Anything
// smaller and the bar chart loses its shape; anything larger and only one
// box fits on a typical terminal.
const statsBoxWidth = 34

// startBackfill spawns a goroutine that feeds the most recent `n` lines from
// the store into `st` (in idx order). It tags the stat as backfilling for
// the renderer's progress hint and clears the flag on exit.
//
// `cutoff` is the line count captured at the moment the stat was registered
// with the manager. The window [cutoff-n, cutoff) is precisely the set of
// lines that the live Observe path will NOT see — see commitStat for why
// that boundary avoids both gaps and double-counts.
func startBackfill(s *sharedState, st *stats.Stat, cutoff, n int) {
	if cutoff <= 0 || n <= 0 {
		return
	}
	start := cutoff - n
	if start < 0 {
		start = 0
	}
	st.MarkBackfill(cutoff - start)
	go runBackfill(s, st, start, cutoff)
}

func runBackfill(s *sharedState, st *stats.Stat, start, end int) {
	defer st.FinishBackfill()
	i := start
	for i < end {
		// Read one batch under the shared read lock, then release the lock
		// before feeding the per-stat channel — Feed is non-blocking but
		// we still don't want to keep RLock held while we walk a slice.
		s.mu.RLock()
		batchEnd := i + statsBackfillBatch
		if batchEnd > end {
			batchEnd = end
		}
		buf := make([]*line.LogLine, 0, batchEnd-i)
		for j := i; j < batchEnd && j < s.store.Len(); j++ {
			buf = append(buf, s.store.Get(j))
		}
		s.mu.RUnlock()

		for _, l := range buf {
			st.Feed(l)
		}
		st.AdvanceBackfill(len(buf))
		i = batchEnd
	}
}

// statsAreaHeight returns the rows allotted to the stats container at the
// current window/layout, or 0 when it should be hidden.
func (m model) statsAreaHeight() int {
	if m.statsMgr == nil || len(m.statsMgr.All()) == 0 {
		return 0
	}
	if m.statsLayout == statsLayoutFullLogs {
		return 0
	}
	avail := m.height - 1
	if m.searchMode {
		avail--
	}
	if avail < 4 {
		return 0
	}
	switch m.statsLayout {
	case statsLayoutFullStats:
		return avail
	default: // split
		h := avail / 3
		if h < 4 {
			h = 4
		}
		return h
	}
}

// renderStatsPane produces the bottom-pane string of width m.width and
// height m.statsAreaHeight(). Returns "" when the pane is hidden.
func (m model) renderStatsPane() string {
	height := m.statsAreaHeight()
	if height <= 0 || m.statsMgr == nil {
		return ""
	}
	all := m.statsMgr.All()
	if len(all) == 0 {
		return ""
	}

	// Header: focus indicator + scroll position. Always one row, leaving
	// height-1 rows for the boxes.
	headerStyle := lipgloss.NewStyle().Faint(true)
	focusedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	header := "STATS"
	if m.statsFocused {
		header = focusedStyle.Render("▸ STATS (focused)")
	} else {
		header = headerStyle.Render("  STATS  (tab to focus)")
	}
	scrollHint := headerStyle.Render(fmt.Sprintf("[%d/%d]", m.statsBoxFocused+1, len(all)))
	gap := m.width - lipgloss.Width(header) - lipgloss.Width(scrollHint) - 2
	if gap < 1 {
		gap = 1
	}
	headerLine := " " + header + strings.Repeat(" ", gap) + scrollHint + " "

	// Body: render each stat as a box, lay them out left-to-right, scroll
	// horizontally based on m.statsBoxOffset so the focused box stays on
	// screen.
	bodyHeight := height - 1
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	maxBoxes := m.width / statsBoxWidth
	if maxBoxes < 1 {
		maxBoxes = 1
	}
	if m.statsBoxFocused < m.statsBoxOffset {
		// Defensive: keep offset consistent when caller forgot to clamp.
		m.statsBoxOffset = m.statsBoxFocused
	}
	if m.statsBoxFocused >= m.statsBoxOffset+maxBoxes {
		m.statsBoxOffset = m.statsBoxFocused - maxBoxes + 1
	}

	end := m.statsBoxOffset + maxBoxes
	if end > len(all) {
		end = len(all)
	}
	var boxes []string
	for i := m.statsBoxOffset; i < end; i++ {
		boxes = append(boxes,
			renderStatBox(all[i], statsBoxWidth, bodyHeight,
				m.statsFocused && i == m.statsBoxFocused))
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, boxes...)

	// Pad body to fit width so the JoinVertical doesn't shrink-wrap.
	body = lipgloss.NewStyle().Width(m.width).Render(body)
	headerLine = lipgloss.NewStyle().Width(m.width).Render(headerLine)

	// Top border line so the user sees the split clearly.
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("238")).
		Render(strings.Repeat("─", m.width))
	_ = separator

	return headerLine + "\n" + body
}

// renderStatBox renders one frequency stat as a bordered box of the given
// width × height. The bar chart uses unicode block fractions so a single
// terminal column carries 8 distinct bar levels.
func renderStatBox(st *stats.Stat, width, height int, focused bool) string {
	snap := st.Snapshot()
	border := lipgloss.RoundedBorder()
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(lipgloss.Color("238")).
		Width(width - 2).
		Height(height - 2)
	if focused {
		style = style.BorderForeground(lipgloss.Color("220"))
	}

	var sb strings.Builder
	title := snap.Title
	if focused {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render(title)
	} else {
		title = lipgloss.NewStyle().Bold(true).Render(title)
	}
	sb.WriteString(title)
	sb.WriteString("\n")

	innerW := width - 4 // border 2 + padding implicit
	if innerW < 10 {
		innerW = 10
	}

	// Determine how many rows we have for bars: total - title - footer (1).
	rowsAvail := height - 4
	if rowsAvail < 1 {
		rowsAvail = 1
	}

	counts := snap.Counts
	if len(counts) > rowsAvail {
		counts = counts[:rowsAvail]
	}

	var maxCount uint64
	for _, c := range counts {
		if c.Count > maxCount {
			maxCount = c.Count
		}
	}

	for _, c := range counts {
		labelW := innerW / 3
		if labelW < 6 {
			labelW = 6
		}
		barW := innerW - labelW - 8 // leave room for count
		if barW < 4 {
			barW = 4
		}
		bar := renderBar(c.Count, maxCount, barW)
		label := truncate(c.Label, labelW)
		sb.WriteString(fmt.Sprintf("%-*s %s %d\n", labelW, label, bar, c.Count))
	}

	// Pad remaining rows.
	for i := len(counts); i < rowsAvail; i++ {
		sb.WriteString("\n")
	}

	// Footer summarizes counters worth watching at a glance:
	//   total    cumulative lines counted into a group
	//   pending  labels still waiting in the worker's queue
	//            (sustained non-zero = worker is the bottleneck)
	//   dropped  labels refused because the manager's 500MB pending budget
	//            was full — should always stay at 0 in normal use
	footer := fmt.Sprintf("total %d", snap.Total)
	if snap.Pending > 0 {
		footer += fmt.Sprintf("  pending %d", snap.Pending)
	}
	if snap.Backfill.Active {
		footer += fmt.Sprintf("  backfill %d/%d",
			snap.Backfill.Processed, snap.Backfill.Total)
	}
	if snap.Dropped > 0 {
		footer += fmt.Sprintf("  dropped %d", snap.Dropped)
	}
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render(footer))

	return style.Render(sb.String())
}

// renderBar draws a horizontal bar with sub-cell precision using the unicode
// "Lower One Eighth Block" family. value/maxValue scales to the bar's
// available width.
func renderBar(value, maxValue uint64, width int) string {
	if width <= 0 || maxValue == 0 {
		return strings.Repeat(" ", width)
	}
	// 8 sub-cells per column.
	totalSub := uint64(width) * 8
	filledSub := value * totalSub / maxValue
	full := int(filledSub / 8)
	frac := int(filledSub % 8)

	var sb strings.Builder
	sb.Grow(width * 3)
	for i := 0; i < full && i < width; i++ {
		sb.WriteString("█")
	}
	if full < width && frac > 0 {
		// Block fraction characters: 1/8 → ▏, 2/8 → ▎, ...
		eighths := []string{" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
		sb.WriteString(eighths[frac])
		full++
	}
	for i := full; i < width; i++ {
		sb.WriteString(" ")
	}
	return sb.String()
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) <= w {
		return s
	}
	if w <= 3 {
		return s[:w]
	}
	return s[:w-1] + "…"
}
