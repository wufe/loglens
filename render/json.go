package render

import (
	"encoding/json"
	"fmt"
	"loglens/line"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func renderJSON(l *line.LogLine, width int, styles *Styles) string {
	// Multiline JSON group member (not head): always hidden, but handle gracefully
	if l.GroupID != 0 && !l.GroupHead {
		return l.Raw
	}

	// Multiline JSON group head: show prefix (if any) + JSON summary.
	// The tree children (shown via RenderExpanded) provide the expanded view.
	if l.GroupHead && l.GroupID != 0 {
		meta, ok := l.Meta.(*line.JSONMeta)
		if !ok {
			return l.Raw
		}
		if l.Expanded {
			// Expanded: show prefix + structural indicator
			indicator := renderStructIndicator(meta.Value, styles)
			if meta.Prefix != "" {
				return styles.Plain.Render(meta.Prefix) + indicator
			}
			return indicator
		}
		if meta.Prefix != "" {
			prefixRendered := styles.Plain.Render(meta.Prefix)
			return prefixRendered + renderJSONCollapsed(meta.Value, meta.RawJSON, width-lipgloss.Width(prefixRendered), styles)
		}
		return renderJSONCollapsed(meta.Value, meta.RawJSON, width, styles)
	}

	// Embedded JSON (JSON inside a larger line): render the full line
	// with highlighted segments, preserving all original content.
	if len(l.Segments) > 0 {
		return renderHighlights(l, styles)
	}

	meta, ok := l.Meta.(*line.JSONMeta)
	if !ok {
		return l.Raw
	}

	// Child node in an expanded JSON tree: render "key": value with colors
	if l.Depth > 0 {
		return renderJSONChild(l, meta, width, styles)
	}

	// Inline full JSON (entire line is JSON)
	if l.Expanded {
		return renderStructIndicator(meta.Value, styles)
	}
	return renderJSONCollapsed(meta.Value, meta.RawJSON, width, styles)
}

// renderJSONChild renders a child entry in an expanded JSON tree.
func renderJSONChild(l *line.LogLine, meta *line.JSONMeta, width int, styles *Styles) string {
	// Raw has format: "key": value or [idx]: value
	raw := l.Raw
	colonIdx := strings.Index(raw, ": ")
	if colonIdx < 0 {
		// Fallback: render value directly with width budget
		var sb strings.Builder
		budget := width + budgetMargin
		renderJSONValueOrdered(&sb, meta.Value, meta.RawJSON, styles, &budget)
		return sb.String()
	}

	key := raw[:colonIdx]
	var sb strings.Builder
	sb.WriteString(styles.JSONKey.Render(key))
	sb.WriteString(": ")

	if l.Expanded {
		// When expanded, children show the details — just indicate the structure type
		sb.WriteString(renderStructIndicator(meta.Value, styles))
	} else {
		remaining := width - lipgloss.Width(key) - 2
		if remaining < 0 {
			remaining = 0
		}
		budget := remaining + budgetMargin
		renderJSONValueOrdered(&sb, meta.Value, meta.RawJSON, styles, &budget)
	}

	return sb.String()
}

// renderStructIndicator renders a short structural indicator for expanded nodes.
func renderStructIndicator(v any, styles *Styles) string {
	switch val := v.(type) {
	case map[string]any:
		return styles.JSONBrace.Render("{") + styles.JSONBrace.Render("…") + styles.JSONBrace.Render("}")
	case []any:
		return styles.JSONBrace.Render("[") + fmt.Sprintf("%d", len(val)) + styles.JSONBrace.Render("]")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// budgetMargin is added to the width target so truncation has a few extra
// visible characters to cut from, ensuring the final "..." marker fits.
const budgetMargin = 16

func renderJSONCollapsed(v any, rawJSON []byte, width int, styles *Styles) string {
	if width <= 0 {
		return ""
	}
	var sb strings.Builder
	budget := width + budgetMargin
	renderJSONValueOrdered(&sb, v, rawJSON, styles, &budget)
	result := sb.String()
	if lipgloss.Width(result) > width {
		// O(N) ANSI-aware truncation. The previous rune-by-rune loop called
		// lipgloss.Width() on the full string every iteration, making this
		// O(N²) — ~25M ops per visible line for a large JSON object, enough
		// to freeze the event loop at even a few lines per second.
		if width <= 3 {
			return ansi.Truncate(result, width, "")
		}
		return ansi.Truncate(result, width-3, "") + styles.JSONBrace.Render("...")
	}
	return result
}

// renderJSONValueOrdered renders a JSON value, using rawJSON to preserve
// key order at every nesting level.
//
// budget is a *visible-char* budget; it is decremented as content is emitted.
// Once it goes non-positive the recursion short-circuits, leaving the caller
// to truncate any trailing overshoot. This cuts collapsed-render cost from
// O(JSON size) to O(terminal width) — a ~25× win for ~5 KB DD/s3gw lines.
func renderJSONValueOrdered(sb *strings.Builder, v any, rawJSON []byte, styles *Styles, budget *int) {
	if *budget <= 0 {
		return
	}
	switch val := v.(type) {
	case map[string]any:
		sb.WriteString(styles.JSONBrace.Render("{"))
		*budget -= 1

		// Fused single-pass extraction of ordered keys + per-key raw bytes.
		keys, rawVals := scanObjectChildren(rawJSON)
		if len(keys) == 0 {
			// Fallback for callers without rawJSON (e.g. tests / BuildJSONChildren without raw).
			for k := range val {
				keys = append(keys, k)
			}
		}

		for i, k := range keys {
			if *budget <= 0 {
				break
			}
			child, ok := val[k]
			if !ok {
				continue
			}
			if i > 0 {
				sb.WriteString(", ")
				*budget -= 2
			}
			keyQuoted := fmt.Sprintf("%q", k)
			sb.WriteString(styles.JSONKey.Render(keyQuoted))
			*budget -= len(keyQuoted)
			sb.WriteString(": ")
			*budget -= 2

			var childRaw []byte
			if i < len(rawVals) {
				childRaw = rawVals[i]
			}
			if k == "level" {
				if s, isStr := child.(string); isStr {
					quoted := fmt.Sprintf("%q", s)
					sb.WriteString(levelStyleFor(s, styles).Render(quoted))
					*budget -= len(quoted)
					continue
				}
			}
			renderJSONValueOrdered(sb, child, childRaw, styles, budget)
		}
		sb.WriteString(styles.JSONBrace.Render("}"))
		*budget -= 1

	case []any:
		sb.WriteString(styles.JSONBrace.Render("["))
		*budget -= 1

		rawVals := scanArrayChildren(rawJSON)
		for i, child := range val {
			if *budget <= 0 {
				break
			}
			if i > 0 {
				sb.WriteString(", ")
				*budget -= 2
			}
			var childRaw []byte
			if i < len(rawVals) {
				childRaw = rawVals[i]
			}
			renderJSONValueOrdered(sb, child, childRaw, styles, budget)
		}
		sb.WriteString(styles.JSONBrace.Render("]"))
		*budget -= 1

	case string:
		s := fmt.Sprintf("%q", val)
		sb.WriteString(styles.JSONString.Render(s))
		*budget -= len(s)

	case float64:
		s := formatNumber(val)
		sb.WriteString(styles.JSONNumber.Render(s))
		*budget -= len(s)

	case bool:
		s := fmt.Sprintf("%t", val)
		sb.WriteString(styles.JSONBool.Render(s))
		*budget -= len(s)

	case nil:
		sb.WriteString(styles.JSONNull.Render("null"))
		*budget -= 4

	default:
		s := fmt.Sprintf("%v", val)
		sb.WriteString(s)
		*budget -= len(s)
	}
}

// levelStyleFor maps a JSON "level" string value to the matching render style.
// Falls back to JSONString when the value isn't a recognized severity word.
func levelStyleFor(level string, styles *Styles) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "err", "fatal", "panic", "crit", "critical", "alert", "emerg":
		return styles.LevelError
	case "warn", "warning":
		return styles.LevelWarn
	case "info", "notice":
		return styles.LevelInfo
	case "debug", "trace":
		return styles.LevelDebug
	}
	return styles.JSONString
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// BuildJSONChildren creates child LogLines for an expanded JSON value.
// orderedKeys provides the key order for objects. rawJSON is the original
// JSON bytes used to extract nested key order.
func BuildJSONChildren(v any, depth int, orderedKeys []string, rawJSON ...[]byte) []*line.LogLine {
	var parentRaw []byte
	if len(rawJSON) > 0 {
		parentRaw = rawJSON[0]
	}

	switch val := v.(type) {
	case map[string]any:
		// Single-pass extraction: keys in order + per-key raw bytes.
		scannedKeys, rawVals := scanObjectChildren(parentRaw)

		// Prefer caller-provided key order (parser may have cached it); fall
		// back to scan result, then to map iteration order.
		keys := orderedKeys
		if len(keys) == 0 {
			keys = scannedKeys
		}
		if len(keys) == 0 {
			for k := range val {
				keys = append(keys, k)
			}
		}

		// Index rawVals by key so we can look up regardless of whether
		// the caller's orderedKeys aligns with our scan order.
		var rawByKey map[string][]byte
		if len(scannedKeys) > 0 {
			rawByKey = make(map[string][]byte, len(scannedKeys))
			for i, sk := range scannedKeys {
				rawByKey[sk] = rawVals[i]
			}
		}

		var children []*line.LogLine
		for _, k := range keys {
			child, ok := val[k]
			if !ok {
				continue
			}
			expandable := isExpandable(child)
			summary := jsonChildSummary(child)

			var childKeys []string
			var childRaw []byte
			if raw, ok := rawByKey[k]; ok {
				childRaw = raw
				childKeys, _ = scanObjectChildren(raw)
			}

			c := &line.LogLine{
				Raw:        fmt.Sprintf("%q: %s", k, summary),
				Type:       line.TypeJSON,
				Expandable: expandable,
				Depth:      depth + 1,
				Meta: &line.JSONMeta{
					Value:   child,
					Summary: summary,
					Keys:    childKeys,
					RawJSON: childRaw,
				},
			}
			children = append(children, c)
		}
		return children

	case []any:
		rawVals := scanArrayChildren(parentRaw)
		var children []*line.LogLine
		for i, child := range val {
			expandable := isExpandable(child)
			summary := jsonChildSummary(child)

			var childKeys []string
			var childRaw []byte
			if i < len(rawVals) {
				childRaw = rawVals[i]
				childKeys, _ = scanObjectChildren(childRaw)
			}

			c := &line.LogLine{
				Raw:        fmt.Sprintf("[%d]: %s", i, summary),
				Type:       line.TypeJSON,
				Expandable: expandable,
				Depth:      depth + 1,
				Meta: &line.JSONMeta{
					Value:   child,
					Summary: summary,
					Keys:    childKeys,
					RawJSON: childRaw,
				},
			}
			children = append(children, c)
		}
		return children
	}
	return nil
}

func isExpandable(v any) bool {
	switch val := v.(type) {
	case map[string]any:
		return len(val) > 0
	case []any:
		return len(val) > 0
	}
	return false
}

func jsonChildSummary(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 50 {
		return s[:47] + "..."
	}
	return s
}

// scanObjectChildren parses a raw JSON object and returns ordered keys and
// per-key raw bytes in a single pass. Returns nil, nil for non-objects.
//
// Assumes raw is already known to be valid JSON (the parser has
// successfully json.Unmarshal'd it before we get here). The caller may
// still pass nil/empty raw (e.g. when children were built without raw
// context); both loops handle that by returning nil slices.
//
// Replaces the previous pair (extractNestedKeys + extractChildRawJSON),
// halving the walks over each raw JSON node and eliminating the map[string][]byte
// allocation when the caller only needs parallel arrays.
func scanObjectChildren(raw []byte) (keys []string, rawVals [][]byte) {
	if len(raw) == 0 {
		return nil, nil
	}
	i := skipJSONWS(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil, nil
	}
	i++
	for {
		i = skipJSONWS(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			return keys, rawVals
		}
		if raw[i] != '"' {
			return keys, rawVals
		}
		end := scanJSONString(raw, i)
		if end < 0 {
			return keys, rawVals
		}
		keyBytes := raw[i+1 : end]
		var key string
		if bytesContainsByte(keyBytes, '\\') {
			if un, err := unquoteJSONKey(raw[i : end+1]); err == nil {
				key = un
			} else {
				key = string(keyBytes)
			}
		} else {
			key = string(keyBytes)
		}
		i = end + 1
		i = skipJSONWS(raw, i)
		if i >= len(raw) || raw[i] != ':' {
			return keys, rawVals
		}
		i++
		i = skipJSONWS(raw, i)
		valStart := i
		valEnd := skipJSONValue(raw, i)
		if valEnd < 0 {
			return keys, rawVals
		}
		keys = append(keys, key)
		rawVals = append(rawVals, raw[valStart:valEnd])
		i = valEnd
		i = skipJSONWS(raw, i)
		if i < len(raw) && raw[i] == ',' {
			i++
		}
	}
}

// scanArrayChildren returns raw bytes for each element of a top-level JSON
// array in order. Returns nil for non-arrays.
func scanArrayChildren(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	i := skipJSONWS(raw, 0)
	if i >= len(raw) || raw[i] != '[' {
		return nil
	}
	i++
	var rawVals [][]byte
	for {
		i = skipJSONWS(raw, i)
		if i >= len(raw) || raw[i] == ']' {
			return rawVals
		}
		valStart := i
		valEnd := skipJSONValue(raw, i)
		if valEnd < 0 {
			return rawVals
		}
		rawVals = append(rawVals, raw[valStart:valEnd])
		i = valEnd
		i = skipJSONWS(raw, i)
		if i < len(raw) && raw[i] == ',' {
			i++
		}
	}
}

func skipJSONWS(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanJSONString scans a JSON-encoded string starting at raw[i] == '"'.
// Returns the index of the closing quote, or -1 on malformed input.
func scanJSONString(raw []byte, i int) int {
	i++ // skip opening quote
	for i < len(raw) {
		c := raw[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == '"' {
			return i
		}
		i++
	}
	return -1
}

// skipJSONValue advances past one complete JSON value starting at i,
// returning the index just after the value, or -1 on malformed input.
func skipJSONValue(raw []byte, i int) int {
	i = skipJSONWS(raw, i)
	if i >= len(raw) {
		return -1
	}
	switch raw[i] {
	case '"':
		end := scanJSONString(raw, i)
		if end < 0 {
			return -1
		}
		return end + 1
	case '{', '[':
		depth := 1
		i++
		for i < len(raw) && depth > 0 {
			c := raw[i]
			switch c {
			case '"':
				end := scanJSONString(raw, i)
				if end < 0 {
					return -1
				}
				i = end + 1
			case '{', '[':
				depth++
				i++
			case '}', ']':
				depth--
				i++
			default:
				i++
			}
		}
		if depth != 0 {
			return -1
		}
		return i
	default:
		// Primitive: number, true, false, null. Scan until a structural
		// or whitespace byte.
		for i < len(raw) {
			c := raw[i]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				return i
			}
			i++
		}
		return i
	}
}

func bytesContainsByte(b []byte, c byte) bool {
	for _, x := range b {
		if x == c {
			return true
		}
	}
	return false
}

// unquoteJSONKey handles escape sequences in a JSON string (including the
// surrounding quotes). Falls back to encoding/json for correctness on
// the rare key that contains backslash escapes.
func unquoteJSONKey(quoted []byte) (string, error) {
	var s string
	if err := json.Unmarshal(quoted, &s); err != nil {
		return "", err
	}
	return s, nil
}
