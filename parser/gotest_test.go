package parser

import (
	"loglens/line"
	"testing"
)

func TestGoTestRun(t *testing.T) {
	result := detectGoTest("=== RUN   TestFoo")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Type != line.TypeGoTestMarker {
		t.Errorf("type = %v, want TypeGoTestMarker", result.Type)
	}
	meta := result.Meta.(*line.GoTestMeta)
	if meta.Action != "RUN" {
		t.Errorf("action = %q, want RUN", meta.Action)
	}
	if meta.TestName != "TestFoo" {
		t.Errorf("testName = %q, want TestFoo", meta.TestName)
	}
}

func TestGoTestPass(t *testing.T) {
	result := detectGoTest("--- PASS: TestFoo (1.23s)")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Type != line.TypeGoTestResult {
		t.Errorf("type = %v, want TypeGoTestResult", result.Type)
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsPass {
		t.Error("expected IsPass = true")
	}
	if meta.Duration != "1.23s" {
		t.Errorf("duration = %q, want 1.23s", meta.Duration)
	}
}

func TestGoTestFail(t *testing.T) {
	result := detectGoTest("--- FAIL: TestFoo (1.23s)")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsFail {
		t.Error("expected IsFail = true")
	}
}

func TestGoTestSkip(t *testing.T) {
	result := detectGoTest("--- SKIP: TestFoo (0.00s)")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsSkip {
		t.Error("expected IsSkip = true")
	}
}

func TestGoTestBarePass(t *testing.T) {
	result := detectGoTest("PASS")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Type != line.TypeGoTestResult {
		t.Errorf("type = %v, want TypeGoTestResult", result.Type)
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsPass {
		t.Error("expected IsPass = true")
	}
}

func TestGoTestBareFail(t *testing.T) {
	result := detectGoTest("FAIL")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsFail {
		t.Error("expected IsFail = true")
	}
}

func TestGoTestSubTests(t *testing.T) {
	tests := []struct {
		input string
		depth int
	}{
		{"--- PASS: TestFoo (0.01s)", 0},
		{"    --- PASS: TestFoo/sub (0.01s)", 1},
		{"        --- FAIL: TestFoo/sub/deep (0.01s)", 2},
	}
	for _, tt := range tests {
		result := detectGoTest(tt.input)
		if result == nil {
			t.Fatalf("expected non-nil for %q", tt.input)
		}
		if result.Depth != tt.depth {
			t.Errorf("depth for %q = %d, want %d", tt.input, result.Depth, tt.depth)
		}
	}
}

func TestGoTestPackageSummary(t *testing.T) {
	result := detectGoTest("ok  \texample/math\t0.003s")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Type != line.TypeGoTestResult {
		t.Errorf("type = %v, want TypeGoTestResult", result.Type)
	}
	meta := result.Meta.(*line.GoTestMeta)
	if !meta.IsPass {
		t.Error("expected IsPass = true")
	}
}

func TestGoTestPause(t *testing.T) {
	result := detectGoTest("=== PAUSE TestFoo")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Type != line.TypeGoTestMarker {
		t.Errorf("type = %v, want TypeGoTestMarker", result.Type)
	}
	meta := result.Meta.(*line.GoTestMeta)
	if meta.Action != "PAUSE" {
		t.Errorf("action = %q, want PAUSE", meta.Action)
	}
}
