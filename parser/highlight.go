package parser

import (
	"github.com/wufe/loglens/line"
	"regexp"
	"strings"
)

var (
	// Warning/error prefixes at line start
	warnPrefixRe = regexp.MustCompile(`(?i)^(\s*(?:\[?))(Warning|WARN|WARNING)([:\]\s])`)
	errPrefixRe  = regexp.MustCompile(`(?i)^(\s*(?:\[?))(Error|ERROR|FATAL)([:\]\s])`)
	infoPrefixRe = regexp.MustCompile(`(?i)^(\s*(?:\[?))(INFO)([:\]\s])`)
	debugPrefixRe = regexp.MustCompile(`(?i)^(\s*(?:\[?))(DEBUG)([:\]\s])`)

	// Timestamps: HH:MM:SS (at word boundary, not part of file ref)
	timeRe = regexp.MustCompile(`(?:^|[\s|])(\d{2}:\d{2}:\d{2})(?:[\s|,.]|$)`)

	// Datetime: YYYY-MM-DD HH:MM:SS optionally with timezone
	datetimeRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:\s+[+-]\d{4})?(?:\s+[A-Z]{2,5})?`)

	// nginx datetime: YYYY/MM/DD HH:MM:SS — emitted by nginx error_log
	nginxDatetimeRe = regexp.MustCompile(`\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}`)

	// nginx severity bracket: [debug] [info] [notice] [warn] [error] [crit] [alert] [emerg]
	nginxLevelRe = regexp.MustCompile(`\[(debug|info|notice|warn|error|crit|alert|emerg)\]`)

	// klog prefix at line start: I0425 00:17:20.844360 — used by k8s components
	// (kube-apiserver, ingress-nginx controller, etc.). Letter is severity:
	// I=info, W=warning, E=error, F=fatal, D=debug.
	klogPrefixRe = regexp.MustCompile(`^([IWEFD])(\d{4}\s+\d{2}:\d{2}:\d{2}\.\d+)`)

	// nginx field markers: client:, server:, upstream:, host:, request:, etc.
	// Trailing class disambiguates field-marker colons from URL/HTTP colons;
	// the highlight range trims that trailing byte to leave the colon styled
	// alongside the keyword.
	nginxFieldRe = regexp.MustCompile(`\b(?:client|server|upstream|host|request|referrer|subrequest):[\s"]`)

	// nginx PROXY-protocol marker, appears as "while reading PROXY protocol"
	nginxProxyProtoRe = regexp.MustCompile(`while reading PROXY protocol`)

	// IPv4 address (rough — full validation isn't needed for highlighting)
	ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

	// Source file references: file.ext:linenum
	sourceRefRe = regexp.MustCompile(`\b([\w./-]+\.(?:go|py|js|ts|tsx|jsx|rs|java|rb|c|cpp|h|hpp|cs|swift|kt|scala|sh|bash|zsh|yaml|yml|toml|json|xml|html|css|scss|sql|proto|ex|exs|hs|lua|r|pl|pm|php)):(\d+)\b`)

	// K8s resource paths: kind.group/name
	k8sResourceRe = regexp.MustCompile(`\b([a-z]+(?:\.[a-z][a-z0-9.]*)+)/([a-zA-Z0-9._-]+)\b`)

	// Known K8s kinds for simple kind/name pattern
	k8sKinds = map[string]bool{
		"pod": true, "pods": true,
		"deployment": true, "deployments": true,
		"service": true, "services": true, "svc": true,
		"configmap": true, "configmaps": true, "cm": true,
		"secret": true, "secrets": true,
		"namespace": true, "namespaces": true, "ns": true,
		"node": true, "nodes": true,
		"daemonset": true, "daemonsets": true, "ds": true,
		"statefulset": true, "statefulsets": true, "sts": true,
		"replicaset": true, "replicasets": true, "rs": true,
		"job": true, "jobs": true,
		"cronjob": true, "cronjobs": true, "cj": true,
		"ingress": true, "ingresses": true, "ing": true,
		"persistentvolume": true, "pv": true,
		"persistentvolumeclaim": true, "pvc": true,
		"serviceaccount": true, "sa": true,
		"role": true, "clusterrole": true,
		"rolebinding": true, "clusterrolebinding": true,
		"networkpolicy": true, "netpol": true,
		"customresourcedefinition": true, "crd": true,
		"gateway": true, "gateways": true,
	}

	k8sSimpleRe = regexp.MustCompile(`\b([a-z]+)/([a-zA-Z0-9._-]+)\b`)

	// K8s status words
	k8sStatusWords = map[string]bool{
		"unchanged": true, "created": true, "configured": true,
		"deleted": true, "patched": true,
	}

	// K8s event severity: "Normal" or "Warning" surrounded by whitespace (mid-line)
	k8sEventSeverityRe = regexp.MustCompile(`(\s{2,})(Normal|Warning)(\s{2,})`)

	// "failed in step <name>" — the headline phrase emitted by go test e2e
	// frameworks (e2e-framework, kuttl, etc.) when a step in a test case
	// blows up. The follow-up line carries the actual error, so highlighting
	// the headline makes it easy to scan a wall of test output for the real
	// failure point.
	failedStepRe = regexp.MustCompile(`(?i)\bfailed in step\s+\S+`)
)

// detectWarning checks if a line starts with a warning/error prefix.
func detectWarning(raw string) *line.LogLine {
	if m := errPrefixRe.FindStringSubmatch(raw); m != nil {
		return &line.LogLine{
			Raw:  raw,
			Type: line.TypeWarning,
			Meta: &line.WarningMeta{Level: strings.ToUpper(m[2])},
		}
	}
	if m := warnPrefixRe.FindStringSubmatch(raw); m != nil {
		return &line.LogLine{
			Raw:  raw,
			Type: line.TypeWarning,
			Meta: &line.WarningMeta{Level: strings.ToUpper(m[2])},
		}
	}
	return nil
}

// highlightSegments scans a line for inline highlights and returns segments.
func highlightSegments(raw string) []line.Segment {
	var highlights []highlight

	// nginx severity bracket — checked early so the level is colored even
	// when surrounded by other tokens (e.g. timestamp before, pid after).
	for _, m := range nginxLevelRe.FindAllStringSubmatchIndex(raw, -1) {
		if m[2] < 0 || m[3] < 0 {
			continue
		}
		level := raw[m[2]:m[3]]
		highlights = append(highlights, highlight{start: m[0], end: m[1], style: nginxLevelStyle(level)})
	}

	// klog prefix at line start (e.g. W0424 14:28:43.555568)
	if m := klogPrefixRe.FindStringSubmatchIndex(raw); m != nil {
		levelChar := raw[m[2]:m[3]]
		highlights = append(highlights, highlight{start: m[2], end: m[3], style: klogLevelStyle(levelChar)})
		highlights = append(highlights, highlight{start: m[4], end: m[5], style: "datetime"})
	}

	// nginx datetime (YYYY/MM/DD HH:MM:SS)
	for _, loc := range nginxDatetimeRe.FindAllStringIndex(raw, -1) {
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "datetime"})
		}
	}

	// Datetime (check before time to avoid overlap)
	for _, loc := range datetimeRe.FindAllStringIndex(raw, -1) {
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "datetime"})
		}
	}

	// Time HH:MM:SS
	for _, m := range timeRe.FindAllStringSubmatchIndex(raw, -1) {
		// m[2] and m[3] are the capture group indices
		if m[2] >= 0 && m[3] > 0 {
			// Check not overlapping with datetime
			if !overlapsAny(highlights, m[2], m[3]) {
				highlights = append(highlights, highlight{start: m[2], end: m[3], style: "timestamp"})
			}
		}
	}

	// nginx field markers — client:, server:, upstream:, host:, request:, ...
	// The match ends at the trailing whitespace/quote; trim that byte off
	// the highlight range so only `keyword:` is styled.
	for _, loc := range nginxFieldRe.FindAllStringIndex(raw, -1) {
		end := loc[1] - 1
		if !overlapsAny(highlights, loc[0], end) {
			highlights = append(highlights, highlight{start: loc[0], end: end, style: "nginx-field"})
		}
	}

	// nginx PROXY protocol marker
	for _, loc := range nginxProxyProtoRe.FindAllStringIndex(raw, -1) {
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "nginx-field"})
		}
	}

	// IPv4 addresses
	for _, loc := range ipv4Re.FindAllStringIndex(raw, -1) {
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "ip"})
		}
	}

	// "failed in step <name>" — flag e2e step-failure headlines.
	for _, loc := range failedStepRe.FindAllStringIndex(raw, -1) {
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "failed-step"})
		}
	}

	// K8s event severity (Normal/Warning mid-line, surrounded by whitespace)
	for _, m := range k8sEventSeverityRe.FindAllStringSubmatchIndex(raw, -1) {
		// m[4] and m[5] are the severity word capture group
		if m[4] >= 0 && m[5] > 0 {
			word := raw[m[4]:m[5]]
			style := "k8s-event-normal"
			if word == "Warning" {
				style = "k8s-event-warning"
			}
			if !overlapsAny(highlights, m[4], m[5]) {
				highlights = append(highlights, highlight{start: m[4], end: m[5], style: style})
			}
		}
	}

	// Source file references
	for _, loc := range sourceRefRe.FindAllStringIndex(raw, -1) {
		// Don't match URLs
		if loc[0] > 0 {
			prefix := raw[:loc[0]]
			if strings.HasSuffix(prefix, "://") || strings.HasSuffix(prefix, "/") {
				continue
			}
		}
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "sourceref"})
		}
	}

	// K8s resource paths (kind.group/name)
	for _, loc := range k8sResourceRe.FindAllStringIndex(raw, -1) {
		// Don't match filesystem paths
		if loc[0] > 0 && raw[loc[0]-1] == '/' {
			continue
		}
		// Only highlight if it looks like a K8s resource, not a container image URL
		matched := raw[loc[0]:loc[1]]
		if !isLikelyK8sResource(matched) {
			continue
		}
		if !overlapsAny(highlights, loc[0], loc[1]) {
			highlights = append(highlights, highlight{start: loc[0], end: loc[1], style: "k8s"})
		}
	}

	// K8s simple kind/name
	for _, loc := range k8sSimpleRe.FindAllStringSubmatchIndex(raw, -1) {
		// loc[2]:loc[3] is the kind capture group, loc[4]:loc[5] is the name
		if loc[2] < 0 || loc[3] < 0 {
			continue
		}
		kind := raw[loc[2]:loc[3]]
		if !k8sKinds[kind] {
			continue
		}
		start, end := loc[0], loc[1]
		if start > 0 && raw[start-1] == '/' {
			continue
		}
		if !overlapsAny(highlights, start, end) {
			highlights = append(highlights, highlight{start: start, end: end, style: "k8s"})
		}
	}

	if len(highlights) == 0 {
		return nil
	}

	// Sort highlights by start position
	sortHighlights(highlights)

	// Build non-overlapping segments
	var segments []line.Segment
	pos := 0
	for _, h := range highlights {
		if h.start < pos {
			continue
		}
		if h.start > pos {
			segments = append(segments, line.Segment{Text: raw[pos:h.start], Style: "plain"})
		}
		segments = append(segments, line.Segment{Text: raw[h.start:h.end], Style: h.style})
		pos = h.end
	}
	if pos < len(raw) {
		segments = append(segments, line.Segment{Text: raw[pos:], Style: "plain"})
	}

	return segments
}

func overlapsAny(highlights []highlight, start, end int) bool {
	for _, h := range highlights {
		if start < h.end && end > h.start {
			return true
		}
	}
	return false
}

type highlight struct {
	start, end int
	style      string
}

// isLikelyK8sResource checks if a matched kind.group/name pattern is actually
// a K8s resource vs a container image URL or other false positive.
//
// K8s resources have the form: kind.apigroup/name where:
//   - kind is a known K8s resource type, OR
//   - apigroup has 2+ dot-separated segments (e.g., apps, apiextensions.k8s.io)
//
// Container images like docker.io/user have a short TLD-like "group" (just "io")
// and the "kind" is not a K8s resource.
func isLikelyK8sResource(s string) bool {
	slashIdx := strings.Index(s, "/")
	if slashIdx < 0 {
		return false
	}
	kindGroup := s[:slashIdx]
	dotIdx := strings.Index(kindGroup, ".")
	if dotIdx < 0 {
		return false
	}
	kind := strings.ToLower(kindGroup[:dotIdx])
	group := kindGroup[dotIdx+1:]

	// If the kind is a known K8s resource, it's very likely a K8s resource path
	if k8sKinds[kind] {
		return true
	}

	// If the API group has 2+ dots (e.g., apiextensions.k8s.io, widget.example.io),
	// it's likely a K8s CRD, not a container registry
	if strings.Count(group, ".") >= 2 {
		return true
	}

	// Single-dot groups like ".apps", ".coordination" are K8s core API groups
	// Container registries have TLD-like groups: ".io", ".com", ".dev"
	// K8s API groups are typically longer than 3 chars
	if len(group) > 3 && !strings.Contains(group, ".") {
		return true
	}

	return false
}

// nginxLevelStyle maps an nginx error_log severity word to a render style key.
func nginxLevelStyle(level string) string {
	switch level {
	case "crit", "alert", "emerg", "error":
		return "level-error"
	case "warn":
		return "level-warn"
	case "info", "notice":
		return "level-info"
	case "debug":
		return "level-debug"
	}
	return "plain"
}

// klogLevelStyle maps a klog severity letter (I/W/E/F/D) to a render style key.
func klogLevelStyle(c string) string {
	switch c {
	case "E", "F":
		return "level-error"
	case "W":
		return "level-warn"
	case "I":
		return "level-info"
	case "D":
		return "level-debug"
	}
	return "plain"
}

func sortHighlights(hs []highlight) {
	// Simple insertion sort — typically few highlights per line
	for i := 1; i < len(hs); i++ {
		key := hs[i]
		j := i - 1
		for j >= 0 && hs[j].start > key.start {
			hs[j+1] = hs[j]
			j--
		}
		hs[j+1] = key
	}
}
