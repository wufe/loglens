package render

import (
	"fmt"
	"loglens/line"
	"os"
	"strings"
	"sync"
)

var (
	fileExistsCache sync.Map
)

func renderHighlights(l *line.LogLine, styles *Styles) string {
	if len(l.Segments) == 0 {
		return l.Raw
	}
	return renderSegments(l.Segments, styles)
}

func renderWarning(l *line.LogLine, styles *Styles) string {
	meta, ok := l.Meta.(*line.WarningMeta)
	if !ok {
		return renderHighlights(l, styles)
	}

	// Apply prefix coloring then render rest with highlights
	level := strings.ToUpper(meta.Level)
	var prefixStyle func(string) string

	switch level {
	case "ERROR", "FATAL":
		prefixStyle = func(s string) string { return styles.ErrorPrefix.Render(s) }
	case "WARN", "WARNING":
		prefixStyle = func(s string) string { return styles.WarnPrefix.Render(s) }
	case "INFO":
		prefixStyle = func(s string) string { return styles.InfoPrefix.Render(s) }
	case "DEBUG":
		prefixStyle = func(s string) string { return styles.DebugPrefix.Render(s) }
	default:
		prefixStyle = func(s string) string { return s }
	}

	// Find the prefix in the raw line
	raw := l.Raw
	lowerRaw := strings.ToLower(raw)
	lowerLevel := strings.ToLower(meta.Level)

	idx := strings.Index(lowerRaw, lowerLevel)
	if idx < 0 {
		return renderHighlights(l, styles)
	}

	end := idx + len(lowerLevel)
	// Include trailing : or ] if present
	if end < len(raw) && (raw[end] == ':' || raw[end] == ']') {
		end++
	}

	prefix := raw[:end]
	rest := raw[end:]

	return prefixStyle(prefix) + renderSegmentsForText(rest, l.Segments, len(prefix), styles)
}

func renderSegments(segments []line.Segment, styles *Styles) string {
	var sb strings.Builder
	for _, seg := range segments {
		sb.WriteString(renderSegment(seg, styles))
	}
	return sb.String()
}

func renderSegmentsForText(text string, segments []line.Segment, offset int, styles *Styles) string {
	// Simple fallback: just render the text
	if len(segments) == 0 {
		return text
	}

	// Try to apply segments that fall within this text range
	var sb strings.Builder
	pos := 0
	for _, seg := range segments {
		segStart := 0
		for i, s := range segments {
			if i == 0 {
				segStart = 0
			}
			_ = s
			break
		}
		_ = segStart
		sb.WriteString(renderSegment(seg, styles))
	}

	if sb.Len() == 0 {
		return text
	}

	_ = pos
	return text
}

func renderSegment(seg line.Segment, styles *Styles) string {
	switch seg.Style {
	case "timestamp":
		return styles.Timestamp.Render(seg.Text)
	case "datetime":
		return styles.Datetime.Render(seg.Text)
	case "sourceref":
		return renderSourceRef(seg.Text, styles)
	case "k8s":
		return styles.K8sResource.Render(seg.Text)
	case "k8s-event-normal":
		return styles.K8sEventNormal.Render(seg.Text)
	case "k8s-event-warning":
		return styles.K8sEventWarning.Render(seg.Text)
	case "level-error":
		return styles.LevelError.Render(seg.Text)
	case "level-warn":
		return styles.LevelWarn.Render(seg.Text)
	case "level-info":
		return styles.LevelInfo.Render(seg.Text)
	case "level-debug":
		return styles.LevelDebug.Render(seg.Text)
	case "nginx-field":
		return styles.NginxField.Render(seg.Text)
	case "ip":
		return styles.IPAddr.Render(seg.Text)
	case "json":
		return styles.JSONString.Render(seg.Text)
	case "plain":
		return seg.Text
	default:
		return seg.Text
	}
}

func renderSourceRef(text string, styles *Styles) string {
	// Check if file exists for hyperlink
	parts := strings.SplitN(text, ":", 2)
	if len(parts) != 2 {
		return styles.SourceRef.Render(text)
	}

	filePath := parts[0]

	// Check cache
	if exists, ok := fileExistsCache.Load(filePath); ok {
		if exists.(bool) {
			absPath, _ := os.Getwd()
			return fmt.Sprintf("\x1b]8;;file://%s/%s\x07%s\x1b]8;;\x07",
				absPath, filePath,
				styles.SourceRef.Render(text))
		}
		return styles.SourceRef.Render(text)
	}

	// Check file existence
	_, err := os.Stat(filePath)
	exists := err == nil
	fileExistsCache.Store(filePath, exists)

	if exists {
		absPath, _ := os.Getwd()
		return fmt.Sprintf("\x1b]8;;file://%s/%s\x07%s\x1b]8;;\x07",
			absPath, filePath,
			styles.SourceRef.Render(text))
	}

	return styles.SourceRef.Render(text)
}
