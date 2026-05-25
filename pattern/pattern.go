// Package pattern extracts repeated log "templates" out of a window of lines.
//
// The caller passes a slice of raw log strings — typically just the lines
// visible in the current viewport — and gets back clusters keyed by a
// "skeleton": the sequence of whitespace-separated tokens after dynamic
// substrings have been masked out (numbers, UUIDs, IPs, timestamps, hex
// hashes, paths, dotted hostnames, etc.).
//
// The pipeline has three phases:
//
//  1. Mask + initial cluster — strings.Fields each line, run a battery of
//     regex substitutions to replace dynamic substrings with "*", then group
//     by the joined-tokens string. Per-line masking is memoized in a bounded
//     cache so repeat calls (e.g. as the user navigates the panel) are O(1).
//  2. Adaptive merge — when the initial cluster count exceeds an adaptive
//     target (~sqrt(N) for N visible lines), greedily merge the most
//     token-multiset-similar pair until the target is reached or the
//     best similarity drops below a floor. The merged template marks
//     positions where the two skeletons disagree as "*", so the surviving
//     pattern visibly shows what varies among its members.
//  3. Sort + collapse — sort clusters by member count desc (ties broken by
//     first occurrence), and collapse consecutive "*" tokens for display.
//
// The merge step is sequence-insensitive (multiset comparison) on purpose:
// it lets JSON objects whose marshal order differs across logs collapse
// into one pattern without any JSON-specific code.
package pattern

import (
	"hash/fnv"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// splitTokens chops a string into tokens using three rules:
//
//   - whitespace, comma, and semicolon are discarded separators (so JSON
//     fields and semicolon-separated lists become one token each);
//   - braces and brackets ({, }, [, ]) are emitted as their own single-
//     character tokens (preserved, not discarded) so two structurally
//     similar lines whose bracketed contents are reordered still share
//     the bracket tokens in their multisets;
//   - everything else accumulates into the current token.
//
// Fully generic — no JSON-specific parsing or field-name awareness — but
// dense enough to give the clustering algorithm something to align on
// even when log lines pack a lot of structure into very few whitespace
// boundaries.
func splitTokens(s string) []string {
	tokens := make([]string, 0, 16)
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsSpace(r), r == ',', r == ';':
			flush()
		case r == '{', r == '}', r == '[', r == ']':
			flush()
			tokens = append(tokens, string(r))
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// maxCacheEntries bounds the per-generation cache size. With a generational
// rotation scheme (active + previous), the worst-case memory ceiling is
// 2 × maxCacheEntries entries. 8192 was picked to comfortably hold the unique
// lines in a typical streaming viewport while keeping the ceiling under a
// few MB even for very long raw strings.
const maxCacheEntries = 8192

// skeletonCache memoizes the result of maskTokens for a given raw log line.
// Pure mask work is the dominant cost in ExtractPatterns and is repeated on
// every cursor move in the patterns panel; caching turns subsequent reads
// into O(1) map lookups.
//
// Generational design: when active fills to maxCacheEntries, it becomes
// previous and a fresh empty map takes over. Reads check active first then
// previous, so anything still in working set survives the rotation. Old
// entries get dropped on the next rotation; nothing lives longer than two
// generations.
var (
	cacheMu       sync.Mutex
	activeCache   = make(map[string]string, maxCacheEntries)
	previousCache map[string]string
)

// ClearCache drops every memoized skeleton and every memoized extraction
// result. Safe to call concurrently. Useful when the caller knows it's done
// with pattern extraction (program exit, large workload change, tests) so
// the caches don't retain raw log content past their useful life.
func ClearCache() {
	cacheMu.Lock()
	activeCache = make(map[string]string, maxCacheEntries)
	previousCache = nil
	cacheMu.Unlock()
	resultCacheMu.Lock()
	activeResultCache = make(map[uint64][]Pattern, maxResultCacheEntries)
	previousResultCache = nil
	resultCacheMu.Unlock()
}

// maxResultCacheEntries bounds the result-level cache. Each entry holds a
// slice of Patterns whose total footprint depends on the input, but a few
// hundred entries is plenty for typical viewport use (cursor navigation in
// the patterns panel only ever asks for the same input repeatedly).
const maxResultCacheEntries = 256

// activeResultCache memoizes the full ExtractPatterns output keyed by a
// hash of its input slice. The adaptive-merge phase (LCS over pairs of
// clusters) is the most expensive step and is identical for identical
// inputs, so caching it makes panel navigation effectively free as long
// as the user isn't scrolling the underlying log viewport.
var (
	resultCacheMu       sync.Mutex
	activeResultCache   = make(map[uint64][]Pattern, maxResultCacheEntries)
	previousResultCache map[uint64][]Pattern
)

// hashInput produces a 64-bit fingerprint of the input slice. Two slices
// with the same lines in the same order yield the same hash; differing
// inputs are extremely unlikely to collide. We embed the line count and a
// per-line length so adding/removing a line at the end can't accidentally
// match a prefix.
func hashInput(lines []string) uint64 {
	h := fnv.New64a()
	var lenBuf [8]byte
	count := uint64(len(lines))
	for i := 0; i < 8; i++ {
		lenBuf[i] = byte(count >> (8 * i))
	}
	h.Write(lenBuf[:])
	for _, ln := range lines {
		l := uint64(len(ln))
		for i := 0; i < 8; i++ {
			lenBuf[i] = byte(l >> (8 * i))
		}
		h.Write(lenBuf[:])
		h.Write([]byte(ln))
	}
	return h.Sum64()
}

// cachedResult returns the cached result for hash if present, otherwise
// computes it via compute and stores it under hash. Handles generational
// rotation under the mutex, same shape as cachedSkeleton.
func cachedResult(hash uint64, compute func() []Pattern) []Pattern {
	resultCacheMu.Lock()
	if v, ok := activeResultCache[hash]; ok {
		resultCacheMu.Unlock()
		return v
	}
	if previousResultCache != nil {
		if v, ok := previousResultCache[hash]; ok {
			activeResultCache[hash] = v
			resultCacheMu.Unlock()
			return v
		}
	}
	resultCacheMu.Unlock()

	v := compute()

	resultCacheMu.Lock()
	if len(activeResultCache) >= maxResultCacheEntries {
		previousResultCache = activeResultCache
		activeResultCache = make(map[uint64][]Pattern, maxResultCacheEntries)
	}
	activeResultCache[hash] = v
	resultCacheMu.Unlock()
	return v
}

// cachedSkeleton returns the cached skeleton string for raw, computing it
// with compute if absent. Handles generational rotation under the mutex.
func cachedSkeleton(raw string, compute func() string) string {
	cacheMu.Lock()
	if v, ok := activeCache[raw]; ok {
		cacheMu.Unlock()
		return v
	}
	if previousCache != nil {
		if v, ok := previousCache[raw]; ok {
			// Promote a still-warm entry to the active generation so the
			// next rotation doesn't evict it. Cheap insurance against
			// thrashing when the viewport keeps the same lines visible.
			activeCache[raw] = v
			cacheMu.Unlock()
			return v
		}
	}
	cacheMu.Unlock()

	v := compute()

	cacheMu.Lock()
	if len(activeCache) >= maxCacheEntries {
		previousCache = activeCache
		activeCache = make(map[string]string, maxCacheEntries)
	}
	activeCache[raw] = v
	cacheMu.Unlock()
	return v
}

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

// ExtractPatterns groups the given lines by their masked-token skeleton,
// then runs an adaptive merge pass to keep cluster cardinality proportional
// to input variety. Returns clusters sorted by member count desc, breaking
// ties by the index of the first member.
//
// Memoized end-to-end: identical input slices (as judged by an FNV-1a hash
// of the lines) return the cached slice instantly. Pattern panel navigation
// re-asks for the same input on every j/k press, so without this cache the
// adaptive-merge phase would re-run on every cursor move.
func ExtractPatterns(lines []string) []Pattern {
	if len(lines) == 0 {
		return nil
	}
	return cachedResult(hashInput(lines), func() []Pattern {
		return extractPatternsUncached(lines)
	})
}

// extractPatternsUncached is the actual masking + clustering + merging
// pipeline. Public callers go through ExtractPatterns to benefit from the
// result cache; this exists separately so the cache wrapper stays tiny.
func extractPatternsUncached(lines []string) []Pattern {
	// Phase 1: mask + initial cluster.
	groups := make(map[string]*Pattern, len(lines))
	order := make([]string, 0, len(lines))
	for i, ln := range lines {
		key := skeletonKey(ln)
		p, ok := groups[key]
		if !ok {
			p = &Pattern{
				SkeletonKey: key,
			}
			groups[key] = p
			order = append(order, key)
		}
		p.LineIndices = append(p.LineIndices, i)
	}
	clusters := make([]Pattern, 0, len(order))
	for _, k := range order {
		clusters = append(clusters, *groups[k])
	}

	// Phase 2: adaptive merge. Target ~sqrt(N) clusters; if we already
	// have fewer, the dataset is naturally diverse and nothing happens.
	clusters = adaptiveMerge(clusters, len(lines))

	// Phase 3: build display Templates (collapse-star pass) and sort.
	for i := range clusters {
		clusters[i].Template = collapseStars(clusters[i].SkeletonKey)
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		if len(clusters[i].LineIndices) != len(clusters[j].LineIndices) {
			return len(clusters[i].LineIndices) > len(clusters[j].LineIndices)
		}
		return clusters[i].LineIndices[0] < clusters[j].LineIndices[0]
	})
	return clusters
}

// skeletonKey returns the masked-token-joined cluster key for one line.
// Memoized via cachedSkeleton so repeat calls with the same raw line cost
// only a map lookup.
func skeletonKey(line string) string {
	return cachedSkeleton(line, func() string {
		tokens := splitTokens(line)
		for i, t := range tokens {
			tokens[i] = maskToken(t)
		}
		return strings.Join(tokens, " ")
	})
}

// mergeSimilarityFloor is the minimum Jaccard similarity between two
// clusters' token multisets for the adaptive merge to consider them
// mergeable. Pairs below this score stay split even if we're over target —
// the floor prevents the algorithm from forcibly homogenizing genuinely
// different lines just to hit a number.
const mergeSimilarityFloor = 0.6

// adaptiveMerge greedily merges the most-similar cluster pair until either
// the input-size-derived target is reached or no remaining pair exceeds the
// similarity floor. The merged Pattern's skeleton marks positions where the
// two source skeletons disagree as "*" (using a length-aware merge — see
// mergeSkeletons), so the resulting Template visibly displays what varies
// among the members.
//
// Cost is O(K²·T) where K is the cluster count and T is the average token
// list length. The phase-1 mask work is the heavier expense and only runs
// once per unique raw line thanks to the cache; this merge runs over
// already-masked skeletons and stays under a millisecond for the viewport
// sizes loglens cares about.
func adaptiveMerge(clusters []Pattern, lineCount int) []Pattern {
	if len(clusters) <= 2 {
		return clusters
	}
	// Target is ceil(sqrt(N)) but never below 2. So 9 lines target 3
	// clusters, 50 target 8, 100 target 10. Scales gently — diverse inputs
	// already below target get no merging.
	target := max(2, int(math.Ceil(math.Sqrt(float64(lineCount)))))
	if len(clusters) <= target {
		return clusters
	}

	// Precompute token multisets once per cluster; the inner loop reuses them.
	multisets := make([]map[string]int, len(clusters))
	sizes := make([]int, len(clusters))
	for i := range clusters {
		multisets[i], sizes[i] = tokenMultiset(clusters[i].SkeletonKey)
	}

	for len(clusters) > target {
		bestI, bestJ := -1, -1
		bestSim := mergeSimilarityFloor
		for i := 0; i < len(clusters); i++ {
			for j := i + 1; j < len(clusters); j++ {
				sim := jaccardMultiset(multisets[i], multisets[j], sizes[i], sizes[j])
				if sim > bestSim {
					bestSim = sim
					bestI, bestJ = i, j
				}
			}
		}
		if bestI < 0 {
			// No pair similar enough — stop merging even if we're still
			// above target. The floor is doing its job.
			break
		}
		merged := mergeClusters(clusters[bestI], clusters[bestJ])
		mergedMS, mergedSize := tokenMultiset(merged.SkeletonKey)
		// Replace bestI with the merged cluster, drop bestJ.
		clusters[bestI] = merged
		multisets[bestI] = mergedMS
		sizes[bestI] = mergedSize
		clusters = append(clusters[:bestJ], clusters[bestJ+1:]...)
		multisets = append(multisets[:bestJ], multisets[bestJ+1:]...)
		sizes = append(sizes[:bestJ], sizes[bestJ+1:]...)
	}
	return clusters
}

// tokenMultiset turns a space-separated skeleton into a frequency map.
// Returns the map and the total token count, the latter so jaccardMultiset
// doesn't need to re-sum on every comparison.
func tokenMultiset(skel string) (map[string]int, int) {
	tokens := strings.Fields(skel)
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m, len(tokens)
}

// jaccardMultiset returns |A ∩ B| / |A ∪ B| over token multisets, where the
// intersection sums min(count_A, count_B) per token and the union sums
// max(count_A, count_B). The two precomputed sizes are passed so the union
// can be derived without re-walking either map.
func jaccardMultiset(a, b map[string]int, sizeA, sizeB int) float64 {
	if sizeA == 0 && sizeB == 0 {
		return 1
	}
	intersection := 0
	// Walk the smaller map for fewer lookups.
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for tok, ca := range small {
		if cb, ok := large[tok]; ok {
			if ca < cb {
				intersection += ca
			} else {
				intersection += cb
			}
		}
	}
	union := sizeA + sizeB - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// mergeClusters combines two patterns into one. The new skeleton is the
// position-by-position merge of the two source skeletons (see
// mergeSkeletons); LineIndices is the union of both members in sorted order.
func mergeClusters(a, b Pattern) Pattern {
	merged := Pattern{
		SkeletonKey: mergeSkeletons(a.SkeletonKey, b.SkeletonKey),
	}
	merged.LineIndices = make([]int, 0, len(a.LineIndices)+len(b.LineIndices))
	merged.LineIndices = append(merged.LineIndices, a.LineIndices...)
	merged.LineIndices = append(merged.LineIndices, b.LineIndices...)
	sort.Ints(merged.LineIndices)
	return merged
}

// mergeSkeletons returns a skeleton built from the longest common
// subsequence (LCS) of the two inputs' tokens. Tokens present in both —
// in their natural order — are kept verbatim; gaps where one or both
// sides have non-matching tokens collapse to a single "*".
//
// LCS-based merging preserves common structure even when individual
// positions don't line up (e.g. JSON objects whose keys appear in a
// different order across log lines). This matters because repeated
// merging compounds: a position-XOR approach erodes one bit of structure
// per merge until only first/last tokens survive, while LCS only loses
// tokens that genuinely differ across all merged inputs.
func mergeSkeletons(a, b string) string {
	ta := splitTokens(a)
	tb := splitTokens(b)
	if len(ta) == 0 && len(tb) == 0 {
		return ""
	}
	common := lcs(ta, tb)
	out := make([]string, 0, len(common)*2+1)
	ia, ib, ic := 0, 0, 0
	appendStar := func() {
		if len(out) == 0 || out[len(out)-1] != "*" {
			out = append(out, "*")
		}
	}
	for ic < len(common) {
		// Skip over divergent tokens in each input until we land on the
		// next common token. A non-zero skip means the gap holds at least
		// one differing token, which gets represented by a single "*".
		startA, startB := ia, ib
		for ia < len(ta) && ta[ia] != common[ic] {
			ia++
		}
		for ib < len(tb) && tb[ib] != common[ic] {
			ib++
		}
		if ia > startA || ib > startB {
			appendStar()
		}
		out = append(out, common[ic])
		ia++
		ib++
		ic++
	}
	// Trailing divergent tokens (anything past the last LCS match).
	if ia < len(ta) || ib < len(tb) {
		appendStar()
	}
	return strings.Join(out, " ")
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

// lcs returns the longest common subsequence of two token slices using a
// standard dynamic-programming table. O(m·n) time and memory; with token
// lists capped around 100 entries per cluster this stays under a few
// hundred KB and well under a millisecond.
func lcs(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	out := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			out = append(out, a[i-1])
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	// Reverse — backtrack produced the subsequence in reverse order.
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
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
