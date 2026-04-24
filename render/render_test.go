package render

import (
	"loglens/line"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func testStyles() *Styles {
	return &Styles{
		CursorLine:      lipgloss.NewStyle().Background(lipgloss.Color("236")),
		JSONKey:         lipgloss.NewStyle().Foreground(lipgloss.Color("86")),
		JSONString:      lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		JSONNumber:      lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		JSONBool:        lipgloss.NewStyle().Foreground(lipgloss.Color("170")),
		JSONNull:        lipgloss.NewStyle().Faint(true),
		JSONBrace:       lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		DiffAdd:         lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		DiffRemove:      lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		DiffHunk:        lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		DiffHeader:      lipgloss.NewStyle().Bold(true),
		GoTestPass:      lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		GoTestFail:      lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		GoTestSkip:      lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		GoTestRun:       lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		GoTestDuration:  lipgloss.NewStyle().Faint(true),
		WarnPrefix:      lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		ErrorPrefix:     lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		InfoPrefix:      lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		DebugPrefix:     lipgloss.NewStyle().Faint(true),
		Timestamp:       lipgloss.NewStyle().Faint(true),
		Datetime:        lipgloss.NewStyle().Faint(true),
		SourceRef:       lipgloss.NewStyle().Underline(true),
		K8sResource:     lipgloss.NewStyle().Foreground(lipgloss.Color("33")),
		K8sEventNormal:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		K8sEventWarning: lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		TableHeader:     lipgloss.NewStyle().Bold(true),
		TableCell:       lipgloss.NewStyle(),
		TableSep:        lipgloss.NewStyle().Faint(true),
		StderrGutter:    lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		ExpandIndicator: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		SearchMatch:     lipgloss.NewStyle().Background(lipgloss.Color("220")),
		Plain:           lipgloss.NewStyle(),
	}
}

func TestRenderPlainLine(t *testing.T) {
	l := &line.LogLine{
		Raw:  "hello world",
		Type: line.TypePlain,
	}
	result := RenderLine(l, 80, false, testStyles())
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected plain text in output, got %q", result)
	}
}

func TestRenderLineWithStderrGutter(t *testing.T) {
	l := &line.LogLine{
		Raw:        "error output",
		Type:       line.TypePlain,
		FromStderr: true,
	}
	result := RenderLine(l, 80, false, testStyles())
	if !strings.Contains(result, "E") {
		t.Error("expected stderr gutter 'E' in output")
	}
}

func TestRenderLineExpandIndicator(t *testing.T) {
	l := &line.LogLine{
		Raw:        `{"key":"value"}`,
		Type:       line.TypeJSON,
		Expandable: true,
		Meta:       &line.JSONMeta{Value: map[string]any{"key": "value"}, Summary: `{"key":"value"}`},
	}
	result := RenderLine(l, 80, false, testStyles())
	if !strings.Contains(result, "▶") {
		t.Error("expected expand indicator ▶ for collapsed expandable line")
	}
}

func TestRenderExpandedIndicator(t *testing.T) {
	l := &line.LogLine{
		Raw:        `{"key":"value"}`,
		Type:       line.TypeJSON,
		Expandable: true,
		Expanded:   true,
		Meta:       &line.JSONMeta{Value: map[string]any{"key": "value"}, Summary: `{"key":"value"}`},
	}
	result := RenderLine(l, 80, false, testStyles())
	if !strings.Contains(result, "▼") {
		t.Error("expected expand indicator ▼ for expanded line")
	}
}

func TestJSONCollapsedFitsOneLine(t *testing.T) {
	l := &line.LogLine{
		Raw:        `{"key":"value","num":42}`,
		Type:       line.TypeJSON,
		Expandable: true,
		Meta:       &line.JSONMeta{Value: map[string]any{"key": "value", "num": float64(42)}, Summary: `{"key":"value","num":42}`},
	}
	result := RenderLine(l, 80, false, testStyles())
	lines := strings.Split(result, "\n")
	if len(lines) > 1 && lines[len(lines)-1] != "" {
		// Allow trailing newline
		nonEmpty := 0
		for _, ln := range lines {
			if ln != "" {
				nonEmpty++
			}
		}
		if nonEmpty > 1 {
			t.Errorf("collapsed JSON should fit on 1 line, got %d", nonEmpty)
		}
	}
}

func TestDiffAdditionRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "+added line",
		Type: line.TypeDiff,
		Meta: &line.DiffMeta{LineKind: "add"},
	}
	// Should not panic
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

func TestDiffRemovalRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "-removed line",
		Type: line.TypeDiff,
		Meta: &line.DiffMeta{LineKind: "remove"},
	}
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

func TestGoTestPassRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "--- PASS: TestFoo (1.23s)",
		Type: line.TypeGoTestResult,
		Meta: &line.GoTestMeta{Action: "PASS", TestName: "TestFoo", Duration: "1.23s", IsPass: true},
	}
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

func TestGoTestFailRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "--- FAIL: TestFoo (1.23s)",
		Type: line.TypeGoTestResult,
		Meta: &line.GoTestMeta{Action: "FAIL", TestName: "TestFoo", Duration: "1.23s", IsFail: true},
	}
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

func TestTableRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "NAME                     STATUS   AGE",
		Type: line.TypeTable,
		Meta: &line.TableMeta{Columns: []int{0, 25, 34}, IsHeader: true},
	}
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

// TestTableRenderingWithTabGaps verifies that when TabGaps are populated,
// the renderer emits a │ separator at every tab gap — even when the gap
// width is 1 (no ≥2-space run for the legacy heuristic to detect).
func TestTableRenderingWithTabGaps(t *testing.T) {
	// Raw is already tab-expanded; columns end at positions 6, 13, 18.
	// Gap widths: 2 ("Normal  "), 1 ("Widget "), 1 ("X ").
	raw := "Normal  Widget X ..."
	//                  0123456789012345678901
	meta := &line.TableMeta{
		Columns:  []int{0, 8, 15, 17},
		IsHeader: false,
		TabGaps: []line.TabGap{
			{Start: 6, End: 8},
			{Start: 14, End: 15},
			{Start: 16, End: 17},
		},
	}
	l := &line.LogLine{Raw: raw, Type: line.TypeTable, Meta: meta}
	out := RenderLine(l, 120, false, testStyles())
	seps := strings.Count(out, "│")
	if seps != 3 {
		t.Errorf("expected 3 │ separators (one per tab gap), got %d: %q", seps, out)
	}
}

// TestTableAlwaysEmptyColumnSuppressed verifies that a column whose max
// width across the group is 0 (from consecutive tabs like ^I^I in kubectl
// output) produces no separator — otherwise rows would render as "a││b"
// instead of "a│b". Raw here is "A" + 7-space tab expansion + 8-space tab
// expansion + "B" — i.e. a valid two-tab sequence with empty cell between.
func TestTableAlwaysEmptyColumnSuppressed(t *testing.T) {
	raw := "A" + strings.Repeat(" ", 7) + strings.Repeat(" ", 8) + "B"
	state := &line.TableGroupState{ColWidths: []int{1, 0, 1}}
	l := &line.LogLine{
		Raw:  raw,
		Type: line.TypeTable,
		Meta: &line.TableMeta{
			Columns:    []int{0, 8, 16},
			IsHeader:   false,
			TabGaps:    []line.TabGap{{Start: 1, End: 8}, {Start: 8, End: 16}},
			GroupState: state,
		},
	}
	out := RenderLine(l, 120, false, testStyles())
	plain := stripANSI(out)
	if strings.Contains(plain, "││") {
		t.Errorf("rendered output contains ││ from always-empty column: %q", plain)
	}
	// Should still contain a single │ between A and B.
	if !strings.Contains(plain, "│") {
		t.Errorf("expected at least one │ separator, got: %q", plain)
	}
}

// TestTableSeparatorsAlignAcrossRows verifies the core fix: when rows have
// varying field widths ("Normal"/"Warning", "SuccessfulWidgetApply"/
// "WidgetNotReady"), the renderer must pad cells to the max width observed
// across the group so every │ separator lands at the same visual column.
// Without GroupState padding, fields fell at tab-stop positions that depend
// on each row's preceding content and columns drifted apart.
func TestTableSeparatorsAlignAcrossRows(t *testing.T) {
	// Shared group state: col 0 width 6, col 1 width 22, col 2 width 24.
	state := &line.TableGroupState{ColWidths: []int{6, 22, 24}}

	// Row 1: "Normal" + "SuccessfulWidgetApply" + "example-widget-controller"
	// Row 2: "Warning" would be 7 chars — grows col 0 to 7. Use pre-widened
	// state to simulate second-row arrival.
	state.ColWidths[0] = 7
	state.ColWidths[1] = 22

	rows := []*line.LogLine{
		{
			Raw:  "Normal  SuccessfulWidgetApply  example-widget-controller",
			Type: line.TypeTable,
			Meta: &line.TableMeta{
				Columns:    []int{0, 8, 32},
				IsHeader:   true,
				TabGaps:    []line.TabGap{{Start: 6, End: 8}, {Start: 30, End: 32}},
				GroupState: state,
			},
		},
		{
			Raw:  "Warning WidgetNotReady  example-widget-controller",
			Type: line.TypeTable,
			Meta: &line.TableMeta{
				Columns:    []int{0, 8, 24},
				IsHeader:   false,
				TabGaps:    []line.TabGap{{Start: 7, End: 8}, {Start: 22, End: 24}},
				GroupState: state,
			},
		},
	}

	// Collect the visible column of every │ per row.
	var perRow [][]int
	for _, l := range rows {
		out := RenderLine(l, 500, false, testStyles())
		plain := stripANSI(out)
		var cols []int
		for i, r := range plain {
			if r == '│' {
				cols = append(cols, i)
			}
		}
		perRow = append(perRow, cols)
	}
	if len(perRow[0]) != len(perRow[1]) {
		t.Fatalf("row separator counts differ: %v vs %v", perRow[0], perRow[1])
	}
	for i := range perRow[0] {
		if perRow[0][i] != perRow[1][i] {
			t.Errorf("separator %d misaligned: row 0 col %d, row 1 col %d",
				i, perRow[0][i], perRow[1][i])
		}
	}
}

// stripANSI removes CSI/OSC escape sequences for column-math assertions.
func stripANSI(s string) string {
	var sb strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] == '\x1b' && i+1 < len(r) && (r[i+1] == '[' || r[i+1] == ']') {
			i++ // skip [
			for i+1 < len(r) {
				i++
				c := r[i]
				if r[i-1] == '[' {
					if c >= 0x40 && c <= 0x7E {
						break
					}
				} else {
					if c == '\x07' || (c == '\\' && r[i-1] == '\x1b') {
						break
					}
				}
			}
			continue
		}
		sb.WriteRune(r[i])
	}
	return sb.String()
}

func TestWarningRendering(t *testing.T) {
	l := &line.LogLine{
		Raw:  "Warning: something bad happened",
		Type: line.TypeWarning,
		Meta: &line.WarningMeta{Level: "WARNING"},
	}
	result := RenderLine(l, 80, false, testStyles())
	if result == "" {
		t.Error("expected non-empty render")
	}
}

func TestBuildJSONChildren(t *testing.T) {
	obj := map[string]any{
		"name": "test",
		"age":  float64(30),
		"nested": map[string]any{
			"key": "value",
		},
	}
	children := BuildJSONChildren(obj, 0, nil)
	if len(children) != 3 {
		t.Errorf("expected 3 children, got %d", len(children))
	}

	hasExpandable := false
	for _, c := range children {
		if c.Expandable {
			hasExpandable = true
		}
	}
	if !hasExpandable {
		t.Error("expected at least one expandable child (nested object)")
	}
}

func TestBuildJSONChildrenArray(t *testing.T) {
	arr := []any{"hello", float64(42), true}
	children := BuildJSONChildren(arr, 0, nil)
	if len(children) != 3 {
		t.Errorf("expected 3 children, got %d", len(children))
	}
}
