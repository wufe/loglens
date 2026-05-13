package render

import (
	"github.com/wufe/loglens/line"
)

func renderDiff(l *line.LogLine, styles *Styles) string {
	meta, ok := l.Meta.(*line.DiffMeta)
	if !ok {
		return l.Raw
	}

	switch meta.LineKind {
	case "header-old", "header-new":
		return styles.DiffHeader.Render(l.Raw)
	case "hunk":
		return styles.DiffHunk.Render(l.Raw)
	case "add":
		return styles.DiffAdd.Render(l.Raw)
	case "remove":
		return styles.DiffRemove.Render(l.Raw)
	case "context":
		return l.Raw
	default:
		return l.Raw
	}
}
