package render

import (
	"github.com/wufe/loglens/line"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Styles holds all style definitions used by renderers.
type Styles struct {
	CursorLine lipgloss.Style

	JSONKey    lipgloss.Style
	JSONString lipgloss.Style
	JSONNumber lipgloss.Style
	JSONBool   lipgloss.Style
	JSONNull   lipgloss.Style
	JSONBrace  lipgloss.Style

	DiffAdd    lipgloss.Style
	DiffRemove lipgloss.Style
	DiffHunk   lipgloss.Style
	DiffHeader lipgloss.Style

	GoTestPass     lipgloss.Style
	GoTestFail     lipgloss.Style
	GoTestSkip     lipgloss.Style
	GoTestRun      lipgloss.Style
	GoTestDuration lipgloss.Style

	WarnPrefix  lipgloss.Style
	ErrorPrefix lipgloss.Style
	InfoPrefix  lipgloss.Style
	DebugPrefix lipgloss.Style

	Timestamp   lipgloss.Style
	Datetime    lipgloss.Style
	SourceRef   lipgloss.Style
	K8sResource     lipgloss.Style
	K8sEventNormal  lipgloss.Style
	K8sEventWarning lipgloss.Style

	LevelError lipgloss.Style
	LevelWarn  lipgloss.Style
	LevelInfo  lipgloss.Style
	LevelDebug lipgloss.Style
	NginxField lipgloss.Style
	IPAddr     lipgloss.Style
	FailedStep lipgloss.Style

	TableHeader lipgloss.Style
	TableCell   lipgloss.Style
	TableSep    lipgloss.Style

	StderrGutter    lipgloss.Style
	ExpandIndicator lipgloss.Style

	SearchMatch lipgloss.Style
	Plain       lipgloss.Style
}

// RenderLine renders a single LogLine to a string.
func RenderLine(l *line.LogLine, width int, isCursor bool, styles *Styles) string {
	if width <= 0 {
		width = 80
	}

	var sb strings.Builder

	// Gutter column (1 char)
	if l.FromStderr {
		sb.WriteString(styles.StderrGutter.Render("E"))
	} else {
		sb.WriteByte(' ')
	}

	// Expand indicator (2 chars)
	if l.Expandable {
		if l.Expanded {
			sb.WriteString(styles.ExpandIndicator.Render("▼ "))
		} else {
			sb.WriteString(styles.ExpandIndicator.Render("▶ "))
		}
	} else {
		sb.WriteString("  ")
	}

	// Content
	contentWidth := width - 3 // gutter + expand indicator
	content := renderContent(l, contentWidth, styles)
	sb.WriteString(content)

	result := sb.String()

	// Apply cursor highlight
	if isCursor {
		result = styles.CursorLine.Width(width).Render(result)
	}

	return truncateToWidth(result, width)
}

// RenderExpanded renders a line and its children when expanded.
// When wrapMode is true, all children are wrapped. The cursor child (identified
// by cursorPath) always wraps regardless of wrapMode.
// Returns the rendered rows and the 0-based index of the cursor row (-1 if no
// cursor in this tree).
func RenderExpanded(l *line.LogLine, width int, cursorPath []int, wrapMode bool, styles *Styles) ([]string, int) {
	var rows []string
	cursorRowIdx := -1

	// Render the parent line
	isCursorOnParent := len(cursorPath) == 1 && cursorPath[0] == -1
	parentRows := RenderLineWrapped(l, width, isCursorOnParent, wrapMode, styles)
	if isCursorOnParent {
		cursorRowIdx = 0
	}
	rows = append(rows, parentRows...)

	if !l.Expanded || l.Children == nil {
		return rows, cursorRowIdx
	}

	// Render children recursively
	renderExpandedChildren(l, width, cursorPath, wrapMode, styles, &rows, &cursorRowIdx)

	return rows, cursorRowIdx
}

// renderExpandedChildren recursively renders children of an expanded node,
// appending to rows and tracking the cursor row index.
func renderExpandedChildren(parent *line.LogLine, width int, cursorPath []int, wrapMode bool, styles *Styles, rows *[]string, cursorRowIdx *int) {
	indent := strings.Repeat("  ", parent.Depth+1)
	for i, child := range parent.Children {
		// Cursor is on this child only if it's the leaf of the path
		childIsCursor := len(cursorPath) == 1 && cursorPath[0] == i
		// This child is on the cursor path (ancestor of cursor target)
		childOnPath := len(cursorPath) > 1 && cursorPath[0] == i

		prefix := "  " + indent // gutter space + indent
		content := renderContent(child, width-len(prefix)-3, styles)

		var childLine string
		if child.Expandable {
			if child.Expanded {
				childLine = " " + indent + styles.ExpandIndicator.Render("▼ ") + content
			} else {
				childLine = " " + indent + styles.ExpandIndicator.Render("▶ ") + content
			}
		} else {
			childLine = prefix + content
		}

		shouldWrap := childIsCursor || wrapMode

		if shouldWrap {
			childRows := WrapLine(childLine, width)
			if childIsCursor {
				*cursorRowIdx = len(*rows)
				cursorStyle := styles.CursorLine.MaxWidth(width)
				for j := range childRows {
					childRows[j] = cursorStyle.Render(childRows[j])
				}
			}
			*rows = append(*rows, childRows...)
		} else {
			if childIsCursor {
				*cursorRowIdx = len(*rows)
				childLine = styles.CursorLine.MaxWidth(width).Render(childLine)
			}
			*rows = append(*rows, truncateToWidth(childLine, width))
		}

		// Recurse for expanded children
		if child.Expanded && child.Children != nil {
			var subPath []int
			if childOnPath {
				subPath = cursorPath[1:]
			}
			renderExpandedChildren(child, width, subPath, wrapMode, styles, rows, cursorRowIdx)
		}
	}
}

func renderContent(l *line.LogLine, width int, styles *Styles) string {
	switch l.Type {
	case line.TypeJSON:
		return renderJSON(l, width, styles)
	case line.TypeDiff:
		return renderDiff(l, styles)
	case line.TypeGoTestMarker, line.TypeGoTestResult:
		return renderGoTest(l, styles)
	case line.TypeTable:
		// Apply inline highlights first, then table formatting
		highlighted := renderHighlights(l, styles)
		return renderTableFormatted(l, highlighted, styles)
	case line.TypeWarning:
		return renderWarning(l, styles)
	case line.TypeFFmpegProgress:
		return renderFFmpegProgress(l, width, styles)
	default:
		return renderHighlights(l, styles)
	}
}

func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	// Use ANSI-aware truncation that properly handles escape sequences
	return ansi.Truncate(s, width, "...")
}

// WrapLine wraps a rendered line into multiple visual rows of the given width.
// Returns the wrapped rows. If the line fits, returns a single-element slice.
func WrapLine(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	visible := lipgloss.Width(s)
	if visible <= width {
		return []string{s}
	}

	// Use ANSI-aware hard wrap that preserves escape sequences across breaks
	wrapped := ansi.Hardwrap(s, width, false)
	rows := strings.Split(wrapped, "\n")
	if len(rows) == 0 {
		return []string{s}
	}
	return rows
}

// VisualHeight returns how many terminal rows a line will occupy at the given width.
// If wrap is false, always returns 1. If wrap is true, computes based on content width.
func VisualHeight(l *line.LogLine, width int, wrap bool, styles *Styles) int {
	if !wrap {
		return 1
	}
	rendered := RenderLine(l, width, false, styles)
	rows := WrapLine(rendered, width)
	return len(rows)
}

// RenderLineWrapped renders a single LogLine, returning multiple visual rows if wrap is true.
// When isCursor is true, the line is always shown unwrapped (full content visible)
// regardless of the global wrap setting.
func RenderLineWrapped(l *line.LogLine, width int, isCursor bool, wrap bool, styles *Styles) []string {
	if width <= 0 {
		width = 80
	}

	var sb strings.Builder

	// Gutter column (1 char)
	if l.FromStderr {
		sb.WriteString(styles.StderrGutter.Render("E"))
	} else {
		sb.WriteByte(' ')
	}

	// Expand indicator (2 chars)
	if l.Expandable {
		if l.Expanded {
			sb.WriteString(styles.ExpandIndicator.Render("▼ "))
		} else {
			sb.WriteString(styles.ExpandIndicator.Render("▶ "))
		}
	} else {
		sb.WriteString("  ")
	}

	contentWidth := width - 3
	content := renderContent(l, contentWidth, styles)
	sb.WriteString(content)

	result := sb.String()
	cursorStyle := styles.CursorLine.MaxWidth(width)

	// Cursor line: always wrap to show full content
	if isCursor {
		rows := WrapLine(result, width)
		for i, row := range rows {
			rows[i] = cursorStyle.Render(row)
		}
		return rows
	}

	// Non-cursor lines: respect global wrap mode
	if !wrap {
		result = truncateToWidth(result, width)
		return []string{result}
	}

	return WrapLine(result, width)
}
