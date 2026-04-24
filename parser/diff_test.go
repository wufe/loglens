package parser

import (
	"loglens/line"
	"testing"
)

func TestDiffFullBlock(t *testing.T) {
	p := New()
	lines := []string{
		"--- a/file.go",
		"+++ b/file.go",
		"@@ -1,3 +1,4 @@",
		" context line",
		"+added line",
		"-removed line",
	}

	for _, l := range lines {
		result := p.Parse(l, false)
		if result.Line.Type != line.TypeDiff {
			t.Errorf("line %q: type = %v, want TypeDiff", l, result.Line.Type)
		}
	}

	// Verify group IDs are the same
	allLines := p.Lines()
	groupID := allLines[0].GroupID
	for i, l := range allLines {
		if l.GroupID != groupID {
			t.Errorf("line %d: groupID = %d, want %d", i, l.GroupID, groupID)
		}
	}

	// Verify meta kinds
	expectedKinds := []string{"header-old", "header-new", "hunk", "context", "add", "remove"}
	for i, l := range allLines {
		meta := l.Meta.(*line.DiffMeta)
		if meta.LineKind != expectedKinds[i] {
			t.Errorf("line %d: kind = %q, want %q", i, meta.LineKind, expectedKinds[i])
		}
	}
}

func TestDiffMultipleHunks(t *testing.T) {
	p := New()
	lines := []string{
		"--- a/file.go",
		"+++ b/file.go",
		"@@ -1,3 +1,3 @@",
		"+added",
		"@@ -10,3 +10,3 @@",
		"-removed",
	}

	for _, l := range lines {
		p.Parse(l, false)
	}

	allLines := p.Lines()
	groupID := allLines[0].GroupID
	for i, l := range allLines {
		if l.Type != line.TypeDiff {
			t.Errorf("line %d: type = %v, want TypeDiff", i, l.Type)
		}
		if l.GroupID != groupID {
			t.Errorf("line %d: groupID = %d, want %d", i, l.GroupID, groupID)
		}
	}
}

func TestDiffEndsOnPlainLine(t *testing.T) {
	p := New()
	diffLines := []string{
		"--- a/file.go",
		"+++ b/file.go",
		"@@ -1,3 +1,3 @@",
		"+added",
	}
	for _, l := range diffLines {
		p.Parse(l, false)
	}

	// Non-diff line should end the block
	result := p.Parse("this is a plain line", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("expected plain line after diff block to NOT be TypeDiff")
	}
}

func TestDiffNotTriggeredWithoutHeader(t *testing.T) {
	p := New()

	// Random + and - lines should NOT be typed as diff
	result := p.Parse("+this looks like an addition", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("+ line without header should not be TypeDiff")
	}

	result = p.Parse("-this looks like a removal", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("- line without header should not be TypeDiff")
	}
}

func TestDiffVsGoTest(t *testing.T) {
	p := New()

	// --- FAIL: should be Go test, NOT diff header
	result := p.Parse("--- FAIL: kuttl (439.95s)", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("--- FAIL: should NOT be TypeDiff")
	}
	if result.Line.Type != line.TypeGoTestResult {
		t.Errorf("--- FAIL: should be TypeGoTestResult, got %v", result.Line.Type)
	}
}

func TestDiffHeaderWithoutFollowUp(t *testing.T) {
	p := New()

	// --- file without +++ should not persist as diff
	p.Parse("--- a/file.go", false)
	result := p.Parse("some random line", false)
	if result.Line.Type == line.TypeDiff {
		t.Error("expected reset after --- without +++")
	}
}
