// Package pattern extracts repeated log "templates" out of a window of lines.
//
// The caller passes a slice of raw log strings — typically just the lines
// visible in the current viewport — and gets back clusters keyed by a
// "skeleton": the sequence of whitespace-separated tokens after dynamic
// substrings have been masked out (numbers, UUIDs, IPs, timestamps, hex
// hashes, paths, dotted hostnames, etc.).
//
// The algorithm is mask-then-cluster, intentionally simple:
//
//  1. strings.Fields the line into tokens.
//  2. For each token, run a battery of regex substitutions that replace
//     dynamic substrings with "*".
//  3. Use the joined masked tokens as the cluster key.
//  4. Group by key; collapse consecutive "*" tokens for display only.
//
// Cost is O(N·L) over the visible window and stays sub-millisecond for the
// ~50 lines a viewport holds, so it is safe to recompute on every render
// while the panel is open. Lines outside the viewport are never touched, so
// follow-mode ingestion pays nothing for this feature when the panel is
// closed.
package pattern

import (
	"regexp"
	"sort"
	"strings"
)

// Pattern represents one cluster of similar lines from the input window.
//
// LineIndices stores positions into the slice passed to ExtractPatterns, in
// the order the lines appeared, so the UI can highlight matching rows by
// looking up indices instead of re-running clustering.
type Pattern struct {
	Template    string // display form: skeleton with consecutive "*" collapsed
	SkeletonKey string // raw cluster key (uncollapsed); stable identifier
	LineIndices []int  // indices into the input slice, ascending
}

// ExtractPatterns groups the given lines by their masked-token skeleton.
// Returns clusters sorted by member count desc, breaking ties by the index
// of the first member (so the order on screen is stable across re-renders
// when counts are equal).
func ExtractPatterns(lines []string) []Pattern {
	if len(lines) == 0 {
		return nil
	}
	groups := make(map[string]*Pattern, len(lines))
	order := make([]string, 0, len(lines))
	for i, ln := range lines {
		key := skeletonKey(ln)
		p, ok := groups[key]
		if !ok {
			p = &Pattern{
				Template:    collapseStars(key),
				SkeletonKey: key,
			}
			groups[key] = p
			order = append(order, key)
		}
		p.LineIndices = append(p.LineIndices, i)
	}
	out := make([]Pattern, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].LineIndices) != len(out[j].LineIndices) {
			return len(out[i].LineIndices) > len(out[j].LineIndices)
		}
		return out[i].LineIndices[0] < out[j].LineIndices[0]
	})
	return out
}

// skeletonKey returns the masked-token-joined cluster key for one line.
func skeletonKey(line string) string {
	tokens := strings.Fields(line)
	for i, t := range tokens {
		tokens[i] = maskToken(t)
	}
	return strings.Join(tokens, " ")
}

// collapseStars merges runs of consecutive "*" tokens into a single "*"
// for display. Cluster keys keep the uncollapsed form so two lines that
// happen to mask to the same collapsed shape but with different token
// counts still cluster apart.
func collapseStars(key string) string {
	tokens := strings.Fields(key)
	out := tokens[:0]
	prevStar := false
	for _, t := range tokens {
		isStar := t == "*"
		if isStar && prevStar {
			continue
		}
		out = append(out, t)
		prevStar = isStar
	}
	return strings.Join(out, " ")
}

// maskRule is one substitution pass over a token. Either with is set (plain
// constant replacement) or fn is set (per-match callback for cases that need
// to inspect the matched text before deciding whether to mask). Exactly one
// is populated per rule.
type maskRule struct {
	re   *regexp.Regexp
	with string
	fn   func(string) string
}

func (r maskRule) apply(s string) string {
	if r.fn != nil {
		return r.re.ReplaceAllStringFunc(s, r.fn)
	}
	return r.re.ReplaceAllString(s, r.with)
}

// Mask rules are applied in order. Earlier rules win where they overlap
// (the input has already been rewritten by the time the later rule runs),
// so place specific patterns before generic ones — e.g. ISO timestamps
// before time-of-day, UUIDs before plain hex, IPs before decimal numbers.
var maskRules = []maskRule{
	// ISO 8601 timestamp with optional fractional seconds and tz.
	{re: regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`), with: "*"},
	// nginx access-log style date: 25/May/2026:07:59:59
	{re: regexp.MustCompile(`\d{1,2}/[A-Z][a-z]{2}/\d{4}(?::\d{2}:\d{2}:\d{2})?`), with: "*"},
	// Slash-date 2026/05/25
	{re: regexp.MustCompile(`\b\d{4}/\d{2}/\d{2}\b`), with: "*"},
	// Full URL — mask the whole thing so embedded UUIDs/paths don't need a second pass.
	{re: regexp.MustCompile(`https?://[^\s"]+`), with: "*"},
	// AWS SigV4 auth components. These vary per request (credential scope rotates daily,
	// SignedHeaders depends on what was signed, Signature is a 64-char hex), and they
	// live inside a single quoted field with no whitespace, so the structural mask wouldn't
	// otherwise reach them.
	{re: regexp.MustCompile(`Credential=[^",\s]+`), with: "Credential=*"},
	{re: regexp.MustCompile(`SignedHeaders=[^",\s]+`), with: "SignedHeaders=*"},
	{re: regexp.MustCompile(`Signature=[^",\s]+`), with: "Signature=*"},
	// URL-encoded UUID braces, e.g. %7B529f0000-0c51-4036-b350-8074cebc637d%7D
	{re: regexp.MustCompile(`%7[Bb][0-9a-fA-F-]+%7[Dd]`), with: "*"},
	// Standard UUID
	{re: regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), with: "*"},
	// IPv4 with optional port (must come before the decimal-number rule, which would
	// otherwise nibble the octets one pair at a time).
	{re: regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d+)?\b`), with: "*"},
	// nginx worker PID like 2833#2833:
	{re: regexp.MustCompile(`\b\d+#\d+:?`), with: "*"},
	// nginx connection ID like *31642313
	{re: regexp.MustCompile(`\*\d+`), with: "*"},
	// Time-of-day hh:mm:ss with optional fraction. Must come after ISO timestamp.
	{re: regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:\.\d+)?\b`), with: "*"},
	// Time with AM/PM, e.g. 7:13AM
	{re: regexp.MustCompile(`\b\d{1,2}:\d{2}(?:AM|PM|am|pm)\b`), with: "*"},
	// klog timestamp prefix W0525 / E0525 / I0525 (leading letter + 4 digits).
	{re: regexp.MustCompile(`\b[WEI]\d{4}\b`), with: "*"},
	// Dotted hostname / FQDN: at least 3 alphanumeric parts joined by dots,
	// each starting with a letter. Avoids matching "controller.go" (2 parts)
	// or "request.method" (would-be field name, also 2 parts).
	{re: regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9-]*(?:\.[a-zA-Z][a-zA-Z0-9-]*){2,}\b`), with: "*"},
	// Long hex blob ≥16 chars: MD5/SHA hashes, request IDs.
	{re: regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`), with: "*"},
	// Long mixed-character identifier ≥16 chars: AWS access keys, traceparent ids,
	// short signing material. The function gate avoids over-masking long
	// lowercase-only words like "determinedupload" while still catching anything
	// with mixed case, digits-and-letters, or base64 padding/separators.
	{re: regexp.MustCompile(`\b[A-Za-z0-9+/=]{16,}\b`), fn: maskMixedAlnum},
	// Decimal number anywhere. Catches request_time "60.069", "0.000",
	// HTTP/1.1 → HTTP/* (the version is dynamic noise for our purposes here),
	// unix ms timestamps with fractional ".379", etc.
	{re: regexp.MustCompile(`\d+\.\d+`), with: "*"},
	// Quoted path values inside JSON: "/some/path/like/this" → "*". Without
	// this, two access logs with different request_uri values would split
	// even though everything around them masks identically.
	{re: regexp.MustCompile(`"/[^"\s]*"`), with: `"*"`},
	// Quoted purely-numeric values inside JSON: "38", "503", "108858" → "*".
	// This collapses short counter values (connection_requests, body_bytes_sent,
	// etc.) that the plain-integer rule misses because they have <4 digits.
	// Side effect: HTTP status codes inside quotes also merge — two access
	// logs that differ only by status cluster together, which is the right
	// call for "what does the volume of these requests look like" pattern UX.
	{re: regexp.MustCompile(`"\d+"`), with: `"*"`},
	// Plain integer ≥4 digits: request lengths, byte counts, connection IDs,
	// epoch ms timestamps. Status codes like 200/404/503 stay literal.
	{re: regexp.MustCompile(`\b\d{4,}\b`), with: "*"},
}

// maskMixedAlnum decides whether a 16+ char [A-Za-z0-9+/=]-run should be
// masked. Returns "*" if the run looks identifier-shaped (mixed case, has
// digits-and-letters, or contains base64 punctuation), otherwise returns
// the input unchanged so the regex acts like a no-op for that match.
func maskMixedAlnum(m string) string {
	var hasUpper, hasLower, hasDigit, hasSym bool
	for i := 0; i < len(m); i++ {
		c := m[i]
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '+' || c == '/' || c == '=':
			hasSym = true
		}
	}
	if (hasUpper && hasLower) || (hasLower && hasDigit) || (hasUpper && hasDigit) || hasSym {
		return "*"
	}
	return m
}

// maskToken applies all substring masks to a single whitespace-delimited
// token. The token may have surrounding punctuation (quotes, commas,
// brackets) preserved when not part of a matched mask — that punctuation
// is structural and helps templates stay readable.
//
// Two extra whole-token rules run after substring masking:
//   - tokens that start with "/" become "*" outright (they're filesystem or
//     URL paths; collapsing them avoids fighting the path regex with all
//     the funny URL-encoded characters it might contain).
//   - tokens that, after substring masking, contain "*" surrounded by what
//     used to be hex/numeric punctuation reduce to "*" alone, so two
//     differently-shaped tokens like "*-aux" and "*-mp4" don't split a
//     cluster. The collapse is conservative: only when the token has no
//     alphabetic chars left outside the asterisks.
func maskToken(tok string) string {
	if len(tok) == 0 {
		return tok
	}
	// Whole-token path mask: any token whose first non-quote, non-paren
	// character is "/" is treated as a path/URI fragment. This catches the
	// "/path/to/whatever" tokens that fall between "PUT" and "HTTP/1.1"
	// in nginx and access-log lines, where the path's internal slashes
	// and percent-encoding make a regex-based mask brittle.
	if isPathToken(tok) {
		return "*"
	}
	s := tok
	for _, r := range maskRules {
		s = r.apply(s)
	}
	return s
}

// isPathToken reports whether tok looks like a URI path: starts with "/"
// after any leading quote/paren, and contains at least one more "/" or a
// percent-encoding.
func isPathToken(tok string) bool {
	i := 0
	for i < len(tok) && (tok[i] == '"' || tok[i] == '\'' || tok[i] == '(' || tok[i] == '[') {
		i++
	}
	if i >= len(tok) || tok[i] != '/' {
		return false
	}
	rest := tok[i:]
	return strings.ContainsAny(rest, "/%")
}
