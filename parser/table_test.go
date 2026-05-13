package parser

import (
	"github.com/wufe/loglens/line"
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

// TestPrefixedLogLineNotTable guards against the LLB / bracketed-prefix false
// positive: consecutive lines shaped like `[prefix]    content` create a
// two-boundary "table" (position 0 + the space gap after the bracket). Those
// lines are never tabular — they're just prefixed log output.
func TestPrefixedLogLineNotTable(t *testing.T) {
	p := New()
	rows := []string{
		"[LLB:1777068038434026212]    logger.go:42: 15:44:40 | myapp/1-create | Forwarding 127.0.0.1:8283",
		"[LLB:1777068038439484431]    logger.go:42: 15:44:40 | myapp/1-create | Forwarding [::1]:8283",
		"[LLB:1777068038443821752]    logger.go:42: 15:44:53 | myapp/1-create | === RUN Test",
	}
	for _, r := range rows {
		p.Parse(r, false)
	}
	for i, l := range p.Lines() {
		if l.Type == line.TypeTable {
			t.Errorf("row %d should not be detected as a table: %q", i, l.Raw)
		}
	}
}

// TestCoincidentalSpaceGapsNotTable guards against a space-gap variant of the
// prefixed-log false positive: two consecutive lines share their long prefix
// (so they match on boundary 0 and one boundary in the prefix) but the gaps
// beyond the prefix fall at different positions because of content. With only
// two boundaries in common, this must not register as a two-row table.
func TestCoincidentalSpaceGapsNotTable(t *testing.T) {
	p := New()
	rows := []string{
		"[LLB:1777068038443821752]     logger.go:42: 15:44:53 | myapp/1-create | === RUN   TestE2EMyAppInstallCreateDefault",
		"[LLB:1777068038449160566]     logger.go:42: 15:44:53 | myapp/1-create |     printer.go:57: POST http://localhost:8283/v1/internal/gateways",
	}
	for _, r := range rows {
		p.Parse(r, false)
	}
	for i, l := range p.Lines() {
		if l.Type == line.TypeTable {
			t.Errorf("row %d should not be detected as a table: %q", i, l.Raw)
		}
	}
}

// TestTabIndentedErrorTraceNotTable guards against treating tab-aligned error
// traces as tab-delimited tables. The cells between consecutive tabs collapse
// to whitespace-only (e.g. `\t\t\t\t` leaves three empty cells), which is the
// telltale sign of tab-as-indentation rather than tab-as-column-separator.
func TestTabIndentedErrorTraceNotTable(t *testing.T) {
	p := New()
	rows := []string{
		"    logger.go:42: 15:44:53 | t/1 |         \tError Trace:\t/a/reporter.go:24",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \t\t\t\t/a/assertion.go:278",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \t\t\t\t/a/chain.go:406",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \t\t\t\t/a/response.go:285",
	}
	for _, r := range rows {
		p.Parse(r, false)
	}
	for i, l := range p.Lines() {
		if l.Type == line.TypeTable {
			t.Errorf("row %d should not be detected as a table (tab indentation, not columns): %q", i, l.Raw)
		}
	}
}

// TestTabAlignedHTTPBlockNotTable guards against the request/response block
// pattern from httpexpect: every row has exactly two tabs with a whitespace-
// only cell sandwiched between them, used purely to align the body content
// to a fixed column. These share tab positions across rows but aren't a
// table — the only "middle" cell is empty on every row.
func TestTabAlignedHTTPBlockNotTable(t *testing.T) {
	p := New()
	rows := []string{
		"    logger.go:42: 15:44:53 | t/1 |         \t            \trequest: POST /v1/internal/gateways HTTP/1.1",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \t  Host: localhost:8283",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \t  Content-Type: application/json; charset=utf-8",
		"    logger.go:42: 15:44:53 | t/1 |         \t            \tresponse: HTTP/1.1 409 Conflict 21.607339ms",
	}
	for _, r := range rows {
		p.Parse(r, false)
	}
	for i, l := range p.Lines() {
		if l.Type == line.TypeTable {
			t.Errorf("row %d should not be detected as a table (tab-aligned HTTP block): %q", i, l.Raw)
		}
	}
}
