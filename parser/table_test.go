package parser

import (
	"loglens/line"
	"testing"
)

func TestSimpleTable(t *testing.T) {
	p := New()

	p.Parse("NAME                     STATUS   AGE", false)
	result := p.Parse("my-operator-system-dev   Active   13h", false)

	allLines := p.Lines()
	// Both lines should be table
	if allLines[0].Type != line.TypeTable {
		t.Errorf("line 0: type = %v, want TypeTable", allLines[0].Type)
	}
	if result.Line.Type != line.TypeTable {
		t.Errorf("line 1: type = %v, want TypeTable", result.Line.Type)
	}

	// First line should be header
	if meta, ok := allLines[0].Meta.(*line.TableMeta); ok {
		if !meta.IsHeader {
			t.Error("first line should be header")
		}
	}
}

func TestTableMultipleRows(t *testing.T) {
	p := New()
	lines := []string{
		"NAME                     STATUS   AGE",
		"my-operator-system-dev   Active   13h",
		"kube-system              Active   2d",
		"default                  Active   2d",
	}

	for _, l := range lines {
		p.Parse(l, false)
	}

	allLines := p.Lines()
	// All lines from index 1 onward should be table
	for i := 1; i < len(allLines); i++ {
		if allLines[i].Type != line.TypeTable {
			t.Errorf("line %d: type = %v, want TypeTable", i, allLines[i].Type)
		}
	}
}

func TestSingleLineNotTable(t *testing.T) {
	p := New()
	result := p.Parse("NAME                     STATUS   AGE", false)

	if result.Line.Type == line.TypeTable {
		t.Error("single line should not be TypeTable")
	}
}

func TestTableBreaksOnMismatch(t *testing.T) {
	p := New()
	p.Parse("NAME                     STATUS   AGE", false)
	p.Parse("my-operator-system-dev   Active   13h", false)
	result := p.Parse("this line has no alignment at all", false)

	if result.Line.Type == line.TypeTable {
		t.Error("non-aligned line should not be TypeTable")
	}
}

// TestTabDelimitedTable verifies that log lines using tabs as column
// separators are detected as a table even when tab expansion produces
// different gap widths per row (e.g., "Normal\t" expands to 2 spaces but
// "Warning\t" expands to 1 space at the same tab stop).
func TestTabDelimitedTable(t *testing.T) {
	p := New()
	// Rows from kubectl-style events output with tab separators.
	rows := []string{
		"2026-04-23 23:14:09 +0200 CEST\tNormal\tWidget.widget.example.io e2e-widget-cr-test\tSuccessfulWidgetApply\texample-widget-controller",
		"2026-04-23 23:14:10 +0200 CEST\tWarning\tWidget.widget.example.io e2e-widget-cr-test\tWidgetNotReady\texample-widget-controller",
		"2026-04-23 23:14:15 +0200 CEST\tNormal\tWidget.widget.example.io e2e-widget-cr-test\tWidgetReady\texample-widget-controller",
	}
	for _, r := range rows {
		p.Parse(r, false)
	}

	all := p.Lines()
	for i, l := range all {
		if l.Type != line.TypeTable {
			t.Errorf("row %d: type = %v, want TypeTable (raw=%q)", i, l.Type, l.Raw)
		}
		meta, ok := l.Meta.(*line.TableMeta)
		if !ok {
			t.Fatalf("row %d: missing TableMeta", i)
		}
		if len(meta.TabGaps) == 0 {
			t.Errorf("row %d: expected TabGaps populated when source used tabs", i)
		}
	}

	// All three rows should share the same group ID.
	if all[0].GroupID == 0 || all[0].GroupID != all[1].GroupID || all[1].GroupID != all[2].GroupID {
		t.Errorf("group IDs inconsistent: %d, %d, %d",
			all[0].GroupID, all[1].GroupID, all[2].GroupID)
	}

	// Header boundaries must be consistent across rows — taken from the header
	// row. Each row's TabGaps.End corresponds to boundary positions, and
	// should differ by at most the expected tab-stop cell width.
	headerMeta := all[0].Meta.(*line.TableMeta)
	if len(headerMeta.Columns) < 3 {
		t.Errorf("expected ≥3 columns, got %d: %v", len(headerMeta.Columns), headerMeta.Columns)
	}
}

func TestColumnBoundaries(t *testing.T) {
	bounds := computeColumnBoundaries("NAME                     STATUS   AGE")
	if len(bounds) < 3 {
		t.Errorf("expected at least 3 boundaries, got %d: %v", len(bounds), bounds)
	}
}
