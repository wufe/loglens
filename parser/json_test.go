package parser

import (
	"github.com/wufe/loglens/line"
	"testing"
)

func TestInlineJSON(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantType   line.LineType
		expandable bool
	}{
		{"simple object", `{"name":"test","value":42}`, line.TypeJSON, true},
		{"simple array", `[1,2,3,"hello"]`, line.TypeJSON, true},
		{"nested object", `{"nested":{"key":"value"},"array":[1,2,3]}`, line.TypeJSON, true},
		{"with whitespace", `  {"key": "value"}  `, line.TypeJSON, true},
		{"single quoted wrapper", `'{"key":"value"}'`, line.TypeJSON, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectInlineJSON(tt.input)
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.Type != tt.wantType {
				t.Errorf("type = %v, want %v", result.Type, tt.wantType)
			}
			if result.Expandable != tt.expandable {
				t.Errorf("expandable = %v, want %v", result.Expandable, tt.expandable)
			}
			meta, ok := result.Meta.(*line.JSONMeta)
			if !ok {
				t.Fatal("expected JSONMeta")
			}
			if meta.Value == nil {
				t.Error("expected parsed value")
			}
		})
	}
}

func TestInlineJSONNotExpandableEmpty(t *testing.T) {
	result := detectInlineJSON("{}")
	if result != nil {
		t.Error("expected nil for empty braces")
	}

	result = detectInlineJSON("[]")
	if result != nil {
		t.Error("expected nil for empty brackets")
	}
}

func TestInlineJSONRejectsInvalid(t *testing.T) {
	tests := []string{
		"not json at all",
		"{invalid}",
		"hello world",
		"123",
		"",
	}
	for _, input := range tests {
		result := detectInlineJSON(input)
		if result != nil {
			t.Errorf("expected nil for %q, got non-nil", input)
		}
	}
}

func TestEmbeddedJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"single quoted", `kubectl -p '{"spec":{"replicas":3}}'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectEmbeddedJSON(tt.input)
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.Type != line.TypeJSON {
				t.Errorf("type = %v, want TypeJSON", result.Type)
			}
			if len(result.Segments) == 0 {
				t.Error("expected segments")
			}
		})
	}
}

func TestEmbeddedJSONRejectsShellVars(t *testing.T) {
	result := detectEmbeddedJSON("echo ${VAR}")
	if result != nil {
		t.Error("expected nil for shell variable")
	}
}

func TestEmbeddedJSONRejectsFormatPlaceholders(t *testing.T) {
	result := detectEmbeddedJSON(`fmt.Sprintf("{%d}")`)
	if result != nil {
		t.Error("expected nil for format placeholder")
	}
}

func TestInlineJSONLevelDetection(t *testing.T) {
	cases := []struct {
		name, input, wantLevel string
	}{
		{"error", `{"level":"error","msg":"boom"}`, "error"},
		{"upper Error", `{"level":"Error","msg":"boom"}`, "error"},
		{"warn", `{"level":"warn","msg":"slow"}`, "warn"},
		{"warning", `{"level":"warning","msg":"slow"}`, "warn"},
		{"info", `{"level":"info","msg":"hi"}`, "info"},
		{"debug", `{"level":"debug","msg":"hi"}`, "debug"},
		{"fatal", `{"level":"fatal","msg":"die"}`, "fatal"},
		{"no level field", `{"msg":"hi"}`, ""},
		{"non-string level", `{"level":3}`, ""},
		{"unknown level", `{"level":"verbose"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := detectInlineJSON(tc.input)
			if res == nil {
				t.Fatal("expected detection")
			}
			meta := res.Meta.(*line.JSONMeta)
			if meta.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", meta.Level, tc.wantLevel)
			}
		})
	}
}

func TestEmbeddedJSONLevelSegmentsErrorOnly(t *testing.T) {
	raw := `gateway-operator {"level":"error","msg":"Reconciler error","ts":"2026-04-30T13:01:00Z"}`
	res := detectEmbeddedJSON(raw)
	if res == nil {
		t.Fatal("expected detection")
	}
	meta := res.Meta.(*line.JSONMeta)
	if meta.Level != "error" {
		t.Fatalf("Level = %q, want error", meta.Level)
	}
	var sawErrorSeg bool
	for _, seg := range res.Segments {
		if seg.Style == "level-error" && seg.Text == `"error"` {
			sawErrorSeg = true
		}
	}
	if !sawErrorSeg {
		t.Errorf("expected a level-error segment for the value \"error\", got segments=%v", res.Segments)
	}
}

func TestEmbeddedJSONLevelSegmentsWarn(t *testing.T) {
	raw := `prefix {"level":"warn","msg":"slow"}`
	res := detectEmbeddedJSON(raw)
	if res == nil {
		t.Fatal("expected detection")
	}
	var sawWarnSeg bool
	for _, seg := range res.Segments {
		if seg.Style == "level-warn" && seg.Text == `"warn"` {
			sawWarnSeg = true
		}
	}
	if !sawWarnSeg {
		t.Errorf("expected a level-warn segment for the value \"warn\", got segments=%v", res.Segments)
	}
}

func TestFindLevelValueRange(t *testing.T) {
	src := `{"level":"error","msg":"x"}`
	vs, ve := findLevelValueRange([]byte(src))
	got := src[vs:ve]
	if got != `"error"` {
		t.Errorf("range carved %q, want %q", got, `"error"`)
	}
}

func TestFindLevelValueRangeNoLevel(t *testing.T) {
	vs, ve := findLevelValueRange([]byte(`{"msg":"x"}`))
	if vs != -1 || ve != -1 {
		t.Errorf("expected -1,-1 for object without level, got %d,%d", vs, ve)
	}
}
