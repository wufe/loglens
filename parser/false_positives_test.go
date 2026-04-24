package parser

import (
	"loglens/line"
	"testing"
)

func TestYAMLDashNotDiff(t *testing.T) {
	p := New()
	result := p.Parse("  - widget.example.io/finalizer", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("YAML list item should NOT be TypeDiff")
	}
}

func TestShellVarNotJSON(t *testing.T) {
	p := New()
	result := p.Parse("echo ${VAR}", false)
	if result.Line.Type == line.TypeJSON {
		t.Error("shell variable should NOT be TypeJSON")
	}
}

func TestFormatPlaceholderNotJSON(t *testing.T) {
	p := New()
	result := p.Parse(`fmt.Sprintf("{%d}")`, false)
	if result.Line.Type == line.TypeJSON {
		t.Error("format placeholder should NOT be TypeJSON")
	}
}

func TestGoTestDashNotDiff(t *testing.T) {
	p := New()
	result := p.Parse("--- FAIL: kuttl (439.95s)", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("--- FAIL: should NOT be TypeDiff")
	}
	if result.Line.Type != line.TypeGoTestResult {
		t.Errorf("--- FAIL: should be TypeGoTestResult, got %v", result.Line.Type)
	}
}

func TestSingleDashLineNotDiff(t *testing.T) {
	p := New()
	result := p.Parse("- this is a list item", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("dash list item should NOT be TypeDiff")
	}
}

func TestEmptyBracesNotJSON(t *testing.T) {
	p := New()
	result := p.Parse("{}", false)
	if result.Line.Type == line.TypeJSON && result.Line.Expandable {
		t.Error("empty braces should NOT be expandable JSON")
	}
}

func TestRandomColonsNotTimestamp(t *testing.T) {
	segments := highlightSegments("key:value:pair")
	for _, seg := range segments {
		if seg.Style == "timestamp" {
			t.Error("key:value:pair should not have timestamp highlight")
		}
	}
}

func TestFilesystemPathNotK8s(t *testing.T) {
	segments := highlightSegments("/usr/local/bin/kubectl")
	for _, seg := range segments {
		if seg.Style == "k8s" {
			t.Error("filesystem path should not be K8s resource")
		}
	}
}

func TestVersionNotTimestamp(t *testing.T) {
	segments := highlightSegments("go 1.25.0")
	for _, seg := range segments {
		if seg.Style == "timestamp" && seg.Text == "1.25.0" {
			t.Error("version number should not be highlighted as timestamp")
		}
	}
}
