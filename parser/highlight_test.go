package parser

import (
	"github.com/wufe/loglens/line"
	"testing"
)

func TestTimestamp(t *testing.T) {
	segments := highlightSegments("00:52:29 some log message")
	found := false
	for _, seg := range segments {
		if seg.Style == "timestamp" && seg.Text == "00:52:29" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected timestamp segment for 00:52:29")
	}
}

func TestDatetime(t *testing.T) {
	segments := highlightSegments("2026-04-11 00:39:46 +0200 CEST something happened")
	found := false
	for _, seg := range segments {
		if seg.Style == "datetime" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected datetime segment")
	}
}

func TestSourceFileRef(t *testing.T) {
	segments := highlightSegments("logger.go:42: some message")
	found := false
	for _, seg := range segments {
		if seg.Style == "sourceref" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected sourceref segment for logger.go:42")
	}
}

func TestFailedStep(t *testing.T) {
	segments := highlightSegments("    case.go:396: failed in step 1-create-cr")
	var got string
	for _, seg := range segments {
		if seg.Style == "failed-step" {
			got = seg.Text
			break
		}
	}
	want := "failed in step 1-create-cr"
	if got != want {
		t.Errorf("failed-step segment = %q, want %q", got, want)
	}
}

func TestK8sResource(t *testing.T) {
	segments := highlightSegments("customresourcedefinition.apiextensions.k8s.io/widgets.widget.example.io unchanged")
	found := false
	for _, seg := range segments {
		if seg.Style == "k8s" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected k8s segment")
	}
}

func TestWarningPrefix(t *testing.T) {
	p := New()
	result := p.Parse("Warning: something bad", false)
	if result.Line.Type != line.TypeWarning {
		t.Errorf("type = %v, want TypeWarning", result.Line.Type)
	}
}

func TestErrorPrefix(t *testing.T) {
	p := New()
	result := p.Parse("ERROR: something terrible", false)
	if result.Line.Type != line.TypeWarning {
		t.Errorf("type = %v, want TypeWarning", result.Line.Type)
	}
	meta := result.Line.Meta.(*line.WarningMeta)
	if meta.Level != "ERROR" {
		t.Errorf("level = %q, want ERROR", meta.Level)
	}
}

func TestMultipleHighlights(t *testing.T) {
	segments := highlightSegments("logger.go:42: 00:52:29 deployment.apps/my-deploy configured")
	styles := map[string]bool{}
	for _, seg := range segments {
		if seg.Style != "plain" {
			styles[seg.Style] = true
		}
	}
	if len(styles) < 2 {
		t.Errorf("expected at least 2 highlight styles, got %d: %v", len(styles), styles)
	}
}

func TestURLNotFileRef(t *testing.T) {
	segments := highlightSegments("http://localhost:8080/api")
	for _, seg := range segments {
		if seg.Style == "sourceref" {
			t.Error("URL port should not be detected as source file ref")
		}
	}
}

func TestNginxDatetimeAndLevel(t *testing.T) {
	raw := "2026/04/25 07:06:30 [crit] 835#835: *33051669 SSL_do_handshake() failed"
	segments := highlightSegments(raw)
	var sawDatetime, sawLevel bool
	for _, seg := range segments {
		if seg.Style == "datetime" && seg.Text == "2026/04/25 07:06:30" {
			sawDatetime = true
		}
		if seg.Style == "level-error" && seg.Text == "[crit]" {
			sawLevel = true
		}
	}
	if !sawDatetime {
		t.Error("expected datetime segment for nginx-style YYYY/MM/DD HH:MM:SS")
	}
	if !sawLevel {
		t.Error("expected level-error segment for [crit]")
	}
}

func TestNginxLevelMappings(t *testing.T) {
	cases := []struct {
		level, style string
	}{
		{"crit", "level-error"},
		{"alert", "level-error"},
		{"emerg", "level-error"},
		{"error", "level-error"},
		{"warn", "level-warn"},
		{"info", "level-info"},
		{"notice", "level-info"},
		{"debug", "level-debug"},
	}
	for _, tc := range cases {
		raw := "2026/04/25 07:06:30 [" + tc.level + "] 835#835: hi"
		segments := highlightSegments(raw)
		found := false
		for _, seg := range segments {
			if seg.Style == tc.style && seg.Text == "["+tc.level+"]" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("level %q: expected style %q segment, segments=%v", tc.level, tc.style, segments)
		}
	}
}

func TestKlogPrefix(t *testing.T) {
	raw := "W0424 14:28:43.555568       7 controller.go:1455] Error getting SSL certificate"
	segments := highlightSegments(raw)
	var sawLevel, sawDatetime, sawSourceRef bool
	for _, seg := range segments {
		if seg.Style == "level-warn" && seg.Text == "W" {
			sawLevel = true
		}
		if seg.Style == "datetime" && seg.Text == "0424 14:28:43.555568" {
			sawDatetime = true
		}
		if seg.Style == "sourceref" && seg.Text == "controller.go:1455" {
			sawSourceRef = true
		}
	}
	if !sawLevel {
		t.Error("expected level-warn segment for W")
	}
	if !sawDatetime {
		t.Error("expected datetime segment for klog timestamp")
	}
	if !sawSourceRef {
		t.Error("expected sourceref segment for controller.go:1455")
	}
}

func TestKlogLevelMappings(t *testing.T) {
	cases := []struct {
		letter, style string
	}{
		{"I", "level-info"},
		{"W", "level-warn"},
		{"E", "level-error"},
		{"F", "level-error"},
		{"D", "level-debug"},
	}
	for _, tc := range cases {
		raw := tc.letter + "0425 00:17:20.844360       7 store.go:439] msg"
		segments := highlightSegments(raw)
		found := false
		for _, seg := range segments {
			if seg.Style == tc.style && seg.Text == tc.letter {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("letter %q: expected style %q segment, segments=%v", tc.letter, tc.style, segments)
		}
	}
}

func TestNginxFieldMarkers(t *testing.T) {
	raw := `2026/04/25 05:19:23 [error] 1667#1667: *15507905 upstream timed out, client: 10.0.0.33, server: x.example.com, request: "PUT /file/path HTTP/1.1", upstream: "http://10.0.0.99:443/", host: "x.example.com"`
	segments := highlightSegments(raw)
	wantFields := map[string]bool{"client:": false, "server:": false, "request:": false, "upstream:": false, "host:": false}
	for _, seg := range segments {
		if seg.Style == "nginx-field" {
			if _, ok := wantFields[seg.Text]; ok {
				wantFields[seg.Text] = true
			}
		}
	}
	for k, v := range wantFields {
		if !v {
			t.Errorf("expected nginx-field segment for %q", k)
		}
	}
}

func TestIPv4Highlight(t *testing.T) {
	raw := "2026/04/25 05:19:23 [error] 1667#1667: client: 10.0.0.33, server: 10.0.0.99:443"
	segments := highlightSegments(raw)
	wantIPs := map[string]bool{"10.0.0.33": false, "10.0.0.99": false}
	for _, seg := range segments {
		if seg.Style == "ip" {
			if _, ok := wantIPs[seg.Text]; ok {
				wantIPs[seg.Text] = true
			}
		}
	}
	for ip, seen := range wantIPs {
		if !seen {
			t.Errorf("expected ip segment for %q", ip)
		}
	}
}

func TestProxyProtocolMarker(t *testing.T) {
	raw := `2026/04/25 07:11:44 [error] 836#836: *32845027 broken header: "l    " while reading PROXY protocol, client: 203.0.113.10, server: 0.0.0.0:443`
	segments := highlightSegments(raw)
	found := false
	for _, seg := range segments {
		if seg.Style == "nginx-field" && seg.Text == "while reading PROXY protocol" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected nginx-field segment for PROXY protocol marker")
	}
}

func TestControlBytesSanitized(t *testing.T) {
	// nginx broken-header lines contain raw network bytes — NUL, BEL, BS, CR,
	// etc. — that would otherwise reach the terminal and either move the
	// cursor or desync lipgloss's width math.
	p := New()
	raw := "2026/04/25 07:11:19 [error] 836#836: \x00broken\x07\x08\rheader\x7f"
	res := p.Parse(raw, false)
	for i := 0; i < len(res.Line.Raw); i++ {
		c := res.Line.Raw[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			t.Errorf("control byte 0x%02x leaked through at offset %d", c, i)
		}
	}
}

func TestKlogIngressIsNotK8sResource(t *testing.T) {
	// `asd/asd-3` looks like kind/name but `asd` isn't a known K8s kind —
	// shouldn't get the k8s style or we'd over-color arbitrary slash-separated
	// strings.
	raw := `I0425 00:17:20.844360       7 store.go:439] "Ignoring ingress" ingress="asd/asd-3"`
	segments := highlightSegments(raw)
	for _, seg := range segments {
		if seg.Style == "k8s" && seg.Text == "asd/asd-3" {
			t.Error("asd/asd-3 should not be highlighted as k8s resource")
		}
	}
}
