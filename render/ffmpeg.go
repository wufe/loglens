package render

import (
	"fmt"
	"github.com/wufe/loglens/line"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

// renderFFmpegProgress turns a coalesced ffmpeg progress LogLine into a styled
// bar with a trailing summary. The content is sized to `width` (the terminal
// width minus the 3-char gutter/indicator prefix that RenderLine prepends).
//
// The bar stops growing once the stream ends (meta.Ended) or is abandoned
// (meta.Frozen); at that point it simply keeps its last percentage — the
// LogLine is immutable after the ingestor clears its active pointer.
func renderFFmpegProgress(l *line.LogLine, width int, styles *Styles) string {
	meta, ok := l.Meta.(*line.FFmpegMeta)
	if !ok {
		return l.Raw
	}
	if width < 10 {
		return l.Raw
	}

	summary := ffmpegSummary(meta)
	percentStr := fmt.Sprintf(" %5.1f%% ", meta.Percent*100)

	// Reserve space for the bar: leave room for " percent%" and a small gap
	// before the summary. Clamp to a sane range so the bar never disappears
	// on narrow terminals and never dominates wide ones.
	barWidth := width - len(percentStr) - len(summary) - 1
	if barWidth < 10 {
		barWidth = 10
		summary = ""
	}
	if barWidth > 60 {
		barWidth = 60
	}
	if barWidth > width-len(percentStr)-1 {
		barWidth = width - len(percentStr) - 1
	}
	if barWidth < 1 {
		return l.Raw
	}

	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(barWidth),
		progress.WithoutPercentage(),
	)
	bar := p.ViewAs(meta.Percent)

	var pct string
	switch {
	case meta.Ended:
		pct = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true).Render(percentStr)
	case meta.Frozen:
		pct = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render(percentStr)
	default:
		pct = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(percentStr)
	}

	if summary == "" {
		return bar + pct
	}
	summaryStyle := lipgloss.NewStyle().Faint(true)
	return bar + pct + " " + summaryStyle.Render(summary)
}

// ffmpegSummary returns a compact text line with the most useful stats
// (time / frame / speed / size). Kept short so the bar always has room.
func ffmpegSummary(meta *line.FFmpegMeta) string {
	var parts []string
	if meta.OutTime != "" {
		parts = append(parts, meta.OutTime)
	}
	if meta.Frame > 0 {
		parts = append(parts, fmt.Sprintf("f=%d", meta.Frame))
	}
	if meta.Speed != "" {
		parts = append(parts, meta.Speed)
	}
	tag := ""
	switch {
	case meta.Ended:
		tag = "done"
	case meta.Frozen:
		tag = "aborted"
	}
	if tag != "" {
		parts = append(parts, tag)
	}
	return strings.Join(parts, " · ")
}
