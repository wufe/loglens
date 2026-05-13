package parser

import (
	"github.com/wufe/loglens/line"
	"testing"
)

func TestBufferPushAndAccess(t *testing.T) {
	buf := NewBuffer(5)
	l := &line.LogLine{Raw: "hello"}
	idx := buf.Push(l)
	got := buf.Get(idx)
	if got != l {
		t.Error("expected to get back the pushed line")
	}
	if buf.Count() != 1 {
		t.Errorf("count = %d, want 1", buf.Count())
	}
}

func TestBufferCapacity(t *testing.T) {
	buf := NewBuffer(3)
	for i := 0; i < 10; i++ {
		buf.Push(&line.LogLine{Raw: "line"})
	}
	if buf.Count() != 3 {
		t.Errorf("count = %d, want 3 (capped)", buf.Count())
	}
}

func TestBufferLast(t *testing.T) {
	buf := NewBuffer(5)
	for i := 0; i < 3; i++ {
		buf.Push(&line.LogLine{Raw: "line"})
	}
	last := buf.Last(2)
	if len(last) != 2 {
		t.Errorf("last(2) = %d items, want 2", len(last))
	}
}

func TestMultiLineJSONDetection(t *testing.T) {
	allLines := []*line.LogLine{
		{Raw: "{", Type: line.TypePlain},
		{Raw: `  "a": 1`, Type: line.TypePlain},
		{Raw: "}", Type: line.TypePlain},
	}

	buf := NewBuffer(50)
	for _, l := range allLines {
		buf.Push(l)
	}

	indices := buf.DetectMultiLineJSON(allLines)
	if len(indices) == 0 {
		t.Fatal("expected reparse indices for multi-line JSON")
	}

	// First line should now be TypeJSON, GroupHead
	if allLines[0].Type != line.TypeJSON {
		t.Errorf("line 0: type = %v, want TypeJSON", allLines[0].Type)
	}
	if !allLines[0].GroupHead {
		t.Error("line 0 should be GroupHead")
	}
	if !allLines[0].Expandable {
		t.Error("line 0 should be Expandable")
	}
}

func TestMultiLineJSONInvalid(t *testing.T) {
	allLines := []*line.LogLine{
		{Raw: "{", Type: line.TypePlain},
		{Raw: "not json at all", Type: line.TypePlain},
		{Raw: "}", Type: line.TypePlain},
	}

	buf := NewBuffer(50)
	for _, l := range allLines {
		buf.Push(l)
	}

	indices := buf.DetectMultiLineJSON(allLines)
	if len(indices) != 0 {
		t.Error("expected no reparse for invalid JSON")
	}
}

func TestMultiLineJSONNested(t *testing.T) {
	allLines := []*line.LogLine{
		{Raw: "{", Type: line.TypePlain},
		{Raw: `  "a": {`, Type: line.TypePlain},
		{Raw: `    "b": 1`, Type: line.TypePlain},
		{Raw: "  }", Type: line.TypePlain},
		{Raw: "}", Type: line.TypePlain},
	}

	buf := NewBuffer(50)
	for _, l := range allLines {
		buf.Push(l)
	}

	indices := buf.DetectMultiLineJSON(allLines)
	if len(indices) == 0 {
		t.Fatal("expected reparse indices for nested multi-line JSON")
	}
	if allLines[0].Type != line.TypeJSON {
		t.Errorf("line 0: type = %v, want TypeJSON", allLines[0].Type)
	}
}

func TestMultiLineJSONWithNilPrefix(t *testing.T) {
	// Simulates ReleaseOldLines having nil'd out old entries.
	// DetectMultiLineJSON should stop at nil entries and not panic.
	allLines := make([]*line.LogLine, 300)
	// First 290 entries are nil (released by parser)
	for i := 290; i < 297; i++ {
		allLines[i] = &line.LogLine{Raw: "some plain text", Type: line.TypePlain}
	}
	// Multi-line JSON at the end
	allLines[297] = &line.LogLine{Raw: "{", Type: line.TypePlain}
	allLines[298] = &line.LogLine{Raw: `  "key": "value"`, Type: line.TypePlain}
	allLines[299] = &line.LogLine{Raw: "}", Type: line.TypePlain}

	buf := NewBuffer(50)
	for i := 290; i < 300; i++ {
		buf.Push(allLines[i])
	}

	indices := buf.DetectMultiLineJSON(allLines)
	if len(indices) == 0 {
		t.Fatal("expected reparse indices despite nil prefix entries")
	}
	if allLines[297].Type != line.TypeJSON {
		t.Errorf("line 297 type = %v, want TypeJSON", allLines[297].Type)
	}
}

func TestReleaseOldLines(t *testing.T) {
	p := New()
	for i := 0; i < 100; i++ {
		p.Parse("some line", false)
	}
	if p.LineCount() != 100 {
		t.Fatalf("LineCount = %d, want 100", p.LineCount())
	}

	p.ReleaseOldLines(20)

	// Indices should be preserved
	if p.LineCount() != 100 {
		t.Fatalf("LineCount after release = %d, want 100", p.LineCount())
	}

	// Old entries should be nil
	if p.LineAt(0) != nil {
		t.Error("expected line 0 to be nil after release")
	}
	if p.LineAt(79) != nil {
		t.Error("expected line 79 to be nil after release")
	}

	// Recent entries should still be valid
	if p.LineAt(80) == nil {
		t.Error("expected line 80 to be non-nil")
	}
	if p.LastLine() == nil {
		t.Error("expected LastLine to be non-nil")
	}
}

func TestAllocGroupID(t *testing.T) {
	buf := NewBuffer(5)
	id1 := buf.AllocGroupID()
	id2 := buf.AllocGroupID()
	if id1 == id2 {
		t.Error("expected unique group IDs")
	}
	if id2 != id1+1 {
		t.Errorf("expected sequential IDs: %d, %d", id1, id2)
	}
}
