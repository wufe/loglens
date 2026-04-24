package parser

import (
	"encoding/json"
	"loglens/line"
	"regexp"
	"strings"
)

var (
	// Matches "key": pattern inside braces
	jsonKeyPattern = regexp.MustCompile(`"[\w]+"[\s]*:`)

	// Matches embedded JSON in single/double quotes or bare
	embeddedJSONSingle = regexp.MustCompile(`'\{[^']+\}'`)
	embeddedJSONDouble = regexp.MustCompile(`"\{[^"]+\}"`)
	embeddedJSONBare   = regexp.MustCompile(`\{[^{}]*"[\w]+"[\s]*:[^{}]*\}`)

	// Shell variable pattern to reject
	shellVarPattern = regexp.MustCompile(`\$\{[^}]+\}`)
	// Format placeholder pattern to reject
	fmtPlaceholder = regexp.MustCompile(`\{[%\d]\w*\}`)

	// Unwrapped JSON: "key": <value> — missing outer braces
	unwrappedJSONRe = regexp.MustCompile(`^\s*"[\w.-]+":\s*[\[{"]`)
)

// detectInlineJSON checks if an entire line (trimmed) is valid JSON.
func detectInlineJSON(raw string) *line.LogLine {
	trimmed := strings.TrimSpace(raw)

	// Strip surrounding quotes if present
	unquoted := trimmed
	if (strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'")) ||
		(strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"")) {
		unquoted = trimmed[1 : len(trimmed)-1]
	}

	// Must start with { or [, or be an unwrapped key-value pair
	if len(unquoted) < 3 || (unquoted[0] != '{' && unquoted[0] != '[') {
		return detectUnwrappedJSON(raw, unquoted)
	}

	// Empty braces not expandable
	if unquoted == "{}" || unquoted == "[]" {
		return nil
	}

	rawBytes := []byte(unquoted)
	parsed, ok := parseJSONAny(rawBytes)
	if !ok {
		return nil
	}
	summary := summarizeJSON(unquoted)
	keys := extractOrderedKeys(rawBytes)

	return &line.LogLine{
		Raw:        raw,
		Type:       line.TypeJSON,
		Expandable: true,
		Meta: &line.JSONMeta{
			Value:   parsed,
			Summary: summary,
			Keys:    keys,
			RawJSON: rawBytes,
		},
	}
}

// detectUnwrappedJSON detects JSON key-value pairs missing outer braces,
// e.g. "spec": {"template": {...}} — wraps in {} and parses.
func detectUnwrappedJSON(raw, unquoted string) *line.LogLine {
	if !unwrappedJSONRe.MatchString(unquoted) {
		return nil
	}

	wrapped := "{" + unquoted + "}"
	rawBytes := []byte(wrapped)
	parsed, ok := parseJSONAny(rawBytes)
	if !ok {
		return nil
	}
	keys := extractOrderedKeys(rawBytes)

	return &line.LogLine{
		Raw:        raw,
		Type:       line.TypeJSON,
		Expandable: true,
		Meta: &line.JSONMeta{
			Value:   parsed,
			Summary: summarizeJSON(wrapped),
			Keys:    keys,
			RawJSON: rawBytes,
		},
	}
}

// detectEmbeddedJSON checks for JSON embedded within a line of text.
func detectEmbeddedJSON(raw string) *line.LogLine {
	// Skip if it looks like shell variables
	if shellVarPattern.MatchString(raw) && !jsonKeyPattern.MatchString(raw) {
		return nil
	}

	// Skip format placeholders
	if fmtPlaceholder.MatchString(raw) && !jsonKeyPattern.MatchString(raw) {
		return nil
	}

	var matches []jsonMatch

	// Find JSON objects/arrays by brace counting (handles nesting)
	matches = append(matches, findBraceMatches(raw, '{', '}')...)
	matches = append(matches, findBraceMatches(raw, '[', ']')...)

	// Also try quoted patterns: '{"key":"val"}' or "{"key":"val"}"
	for _, pattern := range []*regexp.Regexp{embeddedJSONSingle, embeddedJSONDouble} {
		locs := pattern.FindAllStringIndex(raw, -1)
		for _, loc := range locs {
			candidate := raw[loc[0]:loc[1]]
			jsonStr := candidate[1 : len(candidate)-1] // strip quotes
			if m := tryJSONMatch(jsonStr, loc[0]+1, loc[1]-1); m != nil {
				matches = append(matches, *m)
			}
		}
	}

	if len(matches) == 0 {
		return nil
	}

	// Keep only the largest non-overlapping matches (prefer outermost)
	matches = filterLargestMatches(matches)

	if len(matches) == 0 {
		return nil
	}

	// Build segments: apply inline highlights to plain portions
	segments := buildEmbeddedSegmentsWithHighlights(raw, matches)

	return &line.LogLine{
		Raw:        raw,
		Type:       line.TypeJSON,
		Segments:   segments,
		Expandable: true,
		Meta: &line.JSONMeta{
			Value:   matches[0].parsed,
			Summary: summarizeJSON(matches[0].json),
			Keys:    extractOrderedKeys([]byte(matches[0].json)),
			RawJSON: []byte(matches[0].json),
		},
	}
}

// findBraceMatches finds JSON objects/arrays by counting braces.
func findBraceMatches(raw string, open, close byte) []jsonMatch {
	var matches []jsonMatch
	for i := 0; i < len(raw); i++ {
		if raw[i] != open {
			continue
		}
		// Count braces to find matching close
		depth := 0
		inString := false
		escaped := false
		end := -1
		for j := i; j < len(raw); j++ {
			ch := raw[j]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && inString {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if ch == open {
				depth++
			} else if ch == close {
				depth--
				if depth == 0 {
					end = j + 1
					break
				}
			}
		}
		if end < 0 {
			continue
		}
		jsonStr := raw[i:end]
		if m := tryJSONMatch(jsonStr, i, end); m != nil {
			matches = append(matches, *m)
		}
	}
	return matches
}

// tryJSONMatch validates a candidate JSON string and returns a match if valid.
func tryJSONMatch(jsonStr string, start, end int) *jsonMatch {
	if len(jsonStr) < 4 || jsonStr == "{}" || jsonStr == "[]" {
		return nil
	}
	if !jsonKeyPattern.MatchString(jsonStr) {
		return nil
	}
	parsed, ok := parseJSONAny([]byte(jsonStr))
	if !ok {
		return nil
	}
	return &jsonMatch{start: start, end: end, json: jsonStr, parsed: parsed}
}

// filterLargestMatches keeps the outermost matches, removing any that are
// contained within a larger match.
func filterLargestMatches(matches []jsonMatch) []jsonMatch {
	if len(matches) <= 1 {
		return matches
	}
	// Sort by start position, then by size descending
	for i := 1; i < len(matches); i++ {
		key := matches[i]
		j := i - 1
		for j >= 0 && (matches[j].start > key.start || (matches[j].start == key.start && (matches[j].end-matches[j].start) < (key.end-key.start))) {
			matches[j+1] = matches[j]
			j--
		}
		matches[j+1] = key
	}

	var result []jsonMatch
	lastEnd := -1
	for _, m := range matches {
		if m.start >= lastEnd {
			result = append(result, m)
			lastEnd = m.end
		}
	}
	return result
}

// buildEmbeddedSegmentsWithHighlights builds segments for embedded JSON lines,
// applying inline highlights (timestamps, source refs, etc.) to the plain portions.
func buildEmbeddedSegmentsWithHighlights(raw string, matches []jsonMatch) []line.Segment {
	var segments []line.Segment
	pos := 0

	for _, m := range matches {
		if m.start > pos {
			// Apply highlights to the plain portion
			plainText := raw[pos:m.start]
			plainSegs := highlightSegments(plainText)
			if plainSegs != nil {
				// Offset the segment text positions are already relative
				segments = append(segments, plainSegs...)
			} else {
				segments = append(segments, line.Segment{Text: plainText, Style: "plain"})
			}
		}
		segments = append(segments, line.Segment{Text: raw[m.start:m.end], Style: "json"})
		pos = m.end
	}

	if pos < len(raw) {
		plainText := raw[pos:]
		plainSegs := highlightSegments(plainText)
		if plainSegs != nil {
			segments = append(segments, plainSegs...)
		} else {
			segments = append(segments, line.Segment{Text: plainText, Style: "plain"})
		}
	}

	return segments
}

type jsonMatch struct {
	start, end int
	json       string
	parsed     any
}

// extractOrderedKeys returns the top-level object keys of raw JSON in their
// original order. Returns nil for non-objects or on malformed input.
//
// This is a byte-level scanner rather than an encoding/json.Decoder because
// Decoder.Token re-decodes every value; we only need top-level keys, and
// skip values unexamined. For a ~3KB DD/s3gw-shape line this is 4-5x
// faster than the Decoder-based implementation it replaced.
//
// Assumption: raw is already known to be valid JSON (the caller has
// successfully json.Unmarshal'd it). That means we can use simple bounds
// checks instead of defensive error handling everywhere.
func extractOrderedKeys(raw []byte) []string {
	i := skipJSONWS(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil
	}
	i++
	var keys []string
	for {
		i = skipJSONWS(raw, i)
		if i >= len(raw) {
			return keys
		}
		if raw[i] == '}' {
			return keys
		}
		if raw[i] != '"' {
			return keys
		}
		// Parse key: scan to the matching unescaped quote.
		end := scanJSONString(raw, i)
		if end < 0 {
			return keys
		}
		keyBytes := raw[i+1 : end]
		// Unescape only if needed.
		if bytesContainsByte(keyBytes, '\\') {
			if un, err := unquoteJSONKey(raw[i : end+1]); err == nil {
				keys = append(keys, un)
			} else {
				keys = append(keys, string(keyBytes))
			}
		} else {
			keys = append(keys, string(keyBytes))
		}
		i = end + 1
		i = skipJSONWS(raw, i)
		if i >= len(raw) || raw[i] != ':' {
			return keys
		}
		i++
		// Skip the value — any type.
		i = skipJSONValue(raw, i)
		if i < 0 {
			return keys
		}
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
			// Escape: skip the next byte. \u... is 5 bytes but the next
			// byte is 'u' which is not a quote, so this still works.
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
