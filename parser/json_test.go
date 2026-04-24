package parser

import (
	"loglens/line"
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
