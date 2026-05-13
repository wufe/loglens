package main

import (
	"fmt"
	"github.com/wufe/loglens/input"
	"github.com/wufe/loglens/line"
	"github.com/wufe/loglens/parser"
	"github.com/wufe/loglens/render"
	"github.com/wufe/loglens/store"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// stripANSIEscapes removes CSI/OSC escape sequences so we can reason about
// visible column positions.
func stripANSIEscapes(s string) string {
	var sb strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] == '\x1b' && i+1 < len(r) && r[i+1] == '[' {
			i++
			for i+1 < len(r) {
				i++
				if c := r[i]; c >= 0x40 && c <= 0x7E {
					break
				}
			}
			continue
		}
		sb.WriteRune(r[i])
	}
	return sb.String()
}

// TestTabDelimitedTableAlignsEndToEnd parses the fixture the user reported
// as broken (loglens-case4.log) through the real parser and renderer, then
// verifies that every │ separator in the first data row lands at the exact
// same visible column as the corresponding one in every other row.
func TestTabDelimitedTableAlignsEndToEnd(t *testing.T) {
	data, err := os.ReadFile("testdata/table_align.txt")
	if err != nil {
		t.Skip("testdata/table_align.txt not present")
	}
	p := parser.New()
	for _, raw := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		p.Parse(raw, false)
	}

	// Build a full render Styles via initialModel; we only need rStyles.
	m := initialModel(nil, false, nil, 0)
	styles := m.rStyles

	var sepCols [][]int
	for _, l := range p.Lines() {
		if l.Type != line.TypeTable {
			t.Fatalf("line was not typed as table: %q", l.Raw)
		}
		out := render.RenderLine(l, 1000, false, styles)
		plain := stripANSIEscapes(out)
		var cols []int
		for i, r := range plain {
			if r == '│' {
				cols = append(cols, i)
			}
		}
		sepCols = append(sepCols, cols)
	}
	if len(sepCols) < 2 {
		t.Fatal("need ≥2 rows to compare alignment")
	}
	want := sepCols[0]
	for i := 1; i < len(sepCols); i++ {
		if len(sepCols[i]) != len(want) {
			t.Errorf("row %d: got %d separators, want %d", i, len(sepCols[i]), len(want))
			continue
		}
		for k := range want {
			if sepCols[i][k] != want[k] {
				t.Errorf("row %d sep %d: col %d, want %d", i, k, sepCols[i][k], want[k])
			}
		}
	}
}

// mockSource is a simple InputSource for testing.
type mockSource struct {
	ch chan input.RawLine
}

func newMockSource() *mockSource {
	return &mockSource{ch: make(chan input.RawLine, 100)}
}

func (m *mockSource) Lines() <-chan input.RawLine { return m.ch }
func (m *mockSource) Stop()                       {}
func (m *mockSource) ExitCode() int               { return -1 }

func setupModel() model {
	src := newMockSource()
	m := initialModel(src, false, nil, 0)
	m.width = 80
	m.height = 24
	return m
}

func sendLine(m model, text string) model {
	m2, _ := m.Update(LineMsg(input.RawLine{Text: text, Source: input.SourceStdin}))
	return m2.(model)
}

func sendKey(m model, key string) model {
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return m2.(model)
}

func sendSpecialKey(m model, keyType tea.KeyType) model {
	m2, _ := m.Update(tea.KeyMsg{Type: keyType})
	return m2.(model)
}

func TestFollowMode(t *testing.T) {
	m := setupModel()

	m = sendLine(m, "line 1")
	m = sendLine(m, "line 2")
	m = sendLine(m, "line 3")

	if !m.follow {
		t.Error("expected follow mode to be on")
	}
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2", m.cursor)
	}
}

func TestFollowDisabledOnUpKey(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")
	m = sendLine(m, "line 2")
	m = sendLine(m, "line 3")

	m = sendKey(m, "k") // up

	if m.follow {
		t.Error("expected follow to be disabled after up key")
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.cursor)
	}
}

func TestFollowReenabledOnG(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")
	m = sendLine(m, "line 2")
	m = sendLine(m, "line 3")
	m = sendKey(m, "k") // up

	m = sendKey(m, "G") // jump to end

	if !m.follow {
		t.Error("expected follow to be re-enabled on G")
	}
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2", m.cursor)
	}
}

func TestCursorBounds(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")

	// Try to go above 0
	m = sendKey(m, "k")
	m = sendKey(m, "k")
	m = sendKey(m, "k")

	if m.cursor < 0 {
		t.Errorf("cursor = %d, should not go below 0", m.cursor)
	}
}

func TestCursorBoundsDown(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")
	m = sendLine(m, "line 2")

	// Try to go past the end
	m = sendKey(m, "j")
	m = sendKey(m, "j")
	m = sendKey(m, "j")

	if m.cursor >= m.store.Len() {
		t.Errorf("cursor = %d, should not exceed lines length %d", m.cursor, m.store.Len())
	}
}

func TestEmptyLinesNoPanic(t *testing.T) {
	m := setupModel()
	// EOF with no content
	m2, _ := m.Update(EOFMsg{ExitCode: 0})
	m = m2.(model)

	// Navigation keys must not panic on empty lines
	m = sendSpecialKey(m, tea.KeyDown)
	m = sendSpecialKey(m, tea.KeyUp)
	m = sendSpecialKey(m, tea.KeyRight)
	m = sendSpecialKey(m, tea.KeyLeft)
	m = sendKey(m, "g")
	m = sendKey(m, "G")
	m = sendKey(m, "w")
	m = sendKey(m, "f")

	// View should show "no content"
	view := m.View()
	if !strings.Contains(view, "no content") {
		t.Error("expected 'no content' in view for empty EOF")
	}
}

func TestEOFMessage(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")

	m2, _ := m.Update(EOFMsg{ExitCode: 1})
	m = m2.(model)

	if !m.eof {
		t.Error("expected eof = true")
	}
	if m.exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", m.exitCode)
	}
}

func TestExitOnEOFFollow(t *testing.T) {
	m := setupModel()
	m.exitOnEOF = true
	m = sendLine(m, "line 1")
	if !m.follow {
		t.Fatal("expected follow mode on")
	}

	// EOF first, then a tick → countdown starts.
	m2, _ := m.Update(EOFMsg{ExitCode: 0})
	m = m2.(model)
	m2, cmd := m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if m.eofCountdownStart.IsZero() {
		t.Fatal("expected countdown to start in follow mode")
	}
	if cmd == nil {
		t.Fatal("expected tickCmd to keep firing while countdown runs")
	}

	// Backdate the countdown by >5s; next tick should return tea.Quit.
	m.eofCountdownStart = time.Now().Add(-6 * time.Second)
	_, cmd = m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd after countdown elapsed")
	}
	// tea.Quit is a function value; calling it returns tea.QuitMsg.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}

func TestExitOnEOFNoFollowDoesNotExit(t *testing.T) {
	m := setupModel()
	m.exitOnEOF = true
	m = sendLine(m, "line 1")
	m = sendKey(m, "f") // toggle follow off
	if m.follow {
		t.Fatal("expected follow off after 'f'")
	}

	m2, _ := m.Update(EOFMsg{ExitCode: 0})
	m = m2.(model)
	// Even with a stale countdown start, a tick while !follow must cancel it
	// and must not quit.
	m.eofCountdownStart = time.Now().Add(-10 * time.Second)
	m2, cmd := m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if !m.eofCountdownStart.IsZero() {
		t.Error("expected countdown cancelled when follow is off")
	}
	if cmd == nil {
		t.Fatal("expected tickCmd to keep firing")
	}
	if _, ok := cmd().(tea.QuitMsg); ok {
		t.Error("must not quit when follow is off")
	}

	// Status bar should show the manual-close hint.
	m.width = 120
	m.height = 24
	view := m.View()
	if !strings.Contains(view, "follow off") {
		t.Error("expected status bar to mention manual close / follow off")
	}
}

func TestExitOnEOFCountdownCancelledWhenFollowToggledOff(t *testing.T) {
	m := setupModel()
	m.exitOnEOF = true
	m = sendLine(m, "line 1")

	m2, _ := m.Update(EOFMsg{ExitCode: 0})
	m = m2.(model)
	m2, _ = m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if m.eofCountdownStart.IsZero() {
		t.Fatal("expected countdown to start")
	}

	m = sendKey(m, "f") // toggle follow off mid-countdown
	m2, _ = m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if !m.eofCountdownStart.IsZero() {
		t.Error("expected countdown cancelled after follow turned off")
	}
}

func TestWindowResize(t *testing.T) {
	m := setupModel()

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = m2.(model)

	if m.width != 120 {
		t.Errorf("width = %d, want 120", m.width)
	}
	if m.height != 40 {
		t.Errorf("height = %d, want 40", m.height)
	}
}

func TestGoToTop(t *testing.T) {
	m := setupModel()
	for i := 0; i < 10; i++ {
		m = sendLine(m, "line")
	}

	m = sendKey(m, "g") // go to top

	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
	if m.follow {
		t.Error("expected follow = false after g")
	}
}

func TestToggleFollow(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "line 1")

	original := m.follow
	m = sendKey(m, "f")

	if m.follow == original {
		t.Error("expected follow to toggle")
	}
}

func TestWrapModeToggle(t *testing.T) {
	m := setupModel()
	if m.wrapMode {
		t.Error("expected wrapMode = false by default")
	}

	m = sendKey(m, "w")
	if !m.wrapMode {
		t.Error("expected wrapMode = true after w")
	}

	m = sendKey(m, "w")
	if m.wrapMode {
		t.Error("expected wrapMode = false after second w")
	}
}

func TestViewWithWrapMode(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "short line")
	m = sendLine(m, "this is a very long line that should wrap when wrap mode is enabled because it exceeds the terminal width of 80 characters significantly")

	m = sendKey(m, "w")
	view := m.View()
	if view == "" {
		t.Error("expected non-empty view in wrap mode")
	}
}

func TestNoFollowFlag(t *testing.T) {
	src := newMockSource()
	m := initialModel(src, true, nil, 0) // noFollow = true
	m.width = 80
	m.height = 24

	if m.follow {
		t.Error("expected follow = false with noFollow flag")
	}
}

func TestViewDoesNotPanic(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "hello world")
	m = sendLine(m, `{"key":"value"}`)
	m = sendLine(m, "--- PASS: TestFoo (1.23s)")

	// Should not panic
	view := m.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestSearchMode(t *testing.T) {
	m := setupModel()
	m = sendLine(m, "hello world")
	m = sendLine(m, "find this line")
	m = sendLine(m, "another line")

	// Enter search mode
	m = sendKey(m, "/")
	if !m.searchMode {
		t.Error("expected searchMode = true after /")
	}
}

func TestRightArrowJumpsToNextExpandable(t *testing.T) {
	// When on a non-expandable node in a JSON tree, right arrow should
	// jump to the next expandable sibling.
	m := setupModel()
	m.width = 200
	m.height = 40
	m.follow = false

	m = sendLine(m, `{"tests": 6, "failures": 2, "name": "foo", "testsuite": [{"a": 1}]}`)

	// Expand the root
	expandAndPopulate(m.store.Get(0))
	m.cursor = 0
	m.cursorPath = []int{0} // "tests": 6 (not expandable)

	node := getChildAtPath(m.store.Get(0), m.cursorPath)
	if node.Expandable {
		t.Fatal("'tests' should not be expandable")
	}

	// Press right arrow — should jump to "testsuite" (the next expandable)
	m = sendSpecialKey(m, tea.KeyRight)

	if m.cursorPath == nil {
		t.Fatal("cursorPath should not be nil")
	}
	target := getChildAtPath(m.store.Get(0), m.cursorPath)
	if target == nil {
		t.Fatal("target node should not be nil")
	}
	if !target.Expandable {
		t.Error("right arrow should have jumped to an expandable node")
	}
	if !strings.Contains(target.Raw, "testsuite") {
		t.Errorf("expected to land on 'testsuite', got %q", target.Raw)
	}
}

func TestRightArrowNoExpandableDoesNothing(t *testing.T) {
	// When there's no expandable node after the current one, right arrow does nothing.
	m := setupModel()
	m.width = 200
	m.height = 40
	m.follow = false

	m = sendLine(m, `{"a": 1, "b": 2, "c": 3}`)
	expandAndPopulate(m.store.Get(0))
	m.cursor = 0
	m.cursorPath = []int{0} // "a": 1

	origPath := append([]int{}, m.cursorPath...)
	m = sendSpecialKey(m, tea.KeyRight)

	// No expandable siblings, cursor should stay
	if len(m.cursorPath) != len(origPath) || m.cursorPath[0] != origPath[0] {
		t.Errorf("cursorPath should be unchanged, got %v", m.cursorPath)
	}
}

func TestMultiLineJSONWithPrefixRendersSourceLines(t *testing.T) {
	// This test verifies that multiline JSON embedded after prefix text
	// (e.g., "reportData:{...}") is detected as a single JSON group and
	// rendered with all original source lines visible (expanded by default).
	inputLines := []string{
		`    kuttl_ctrf_converter.go:21: reportData:{`,
		`           "name": "",`,
		`           "tests": 3,`,
		`           "failures": 1,`,
		`           "time": "13.341",`,
		`           "testsuite": [`,
		`             {`,
		`               "tests": 3,`,
		`               "failures": 1,`,
		`               "timestamp": "2026-04-14T15:44:39.936680398+02:00",`,
		`               "time": "13.327",`,
		`               "name": "/home/user/project/tests/e2e/myapp-install/kuttl",`,
		`               "testsuite": [`,
		`                 {`,
		`                   "tests": 3,`,
		`                   "failures": 1,`,
		`                   "timestamp": "2026-04-14T15:44:39.93773611+02:00",`,
		`                   "time": "13.326",`,
		`                   "name": "myapp-install-create-default",`,
		`                   "testcase": [`,
		`                     {`,
		`                       "classname": "myapp-install-create-default",`,
		`                       "name": "setup",`,
		`                       "timestamp": "2026-04-14T15:44:53.263658312+02:00",`,
		`                       "time": "0.000"`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-install-create-default",`,
		`                       "name": "step 0-install",`,
		`                       "timestamp": "2026-04-14T15:44:53.263662381+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 2`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-install-create-default",`,
		`                       "name": "step 1-create-cr",`,
		`                       "timestamp": "2026-04-14T15:44:53.263663888+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 3,`,
		`                       "failure": {`,
		`                         "text": "command \"echo \\\"Starting port-forward to my-service...\\\"\\\\n kubectl por...\" failed, exit status 1",`,
		`                         "message": "failed in step 1-create-cr"`,
		`                       }`,
		`                     }`,
		`                   ]`,
		`                 }`,
		`               ]`,
		`             }`,
		`           ]`,
		`         }`,
	}

	m := setupModel()
	m.width = 200 // wide enough to avoid truncation
	m.height = 60 // tall enough to show all lines
	m.follow = false
	for _, l := range inputLines {
		m = sendLine(m, l)
	}

	// Process reparse — in the real app this happens via tea.Cmd,
	// but in tests we need to trigger it manually
	m2, _ := m.Update(ReparseMsg(nil))
	m = m2.(model)

	// Verify detection: all lines should be in one JSON group
	if m.store.Len() != len(inputLines) {
		t.Fatalf("expected %d lines, got %d", len(inputLines), m.store.Len())
	}

	// First line should be GroupHead
	head := m.store.Get(0)
	if !head.GroupHead {
		t.Error("first line should be GroupHead")
	}
	if head.GroupID == 0 {
		t.Error("first line should have a non-zero GroupID")
	}
	if !head.Expandable {
		t.Error("first line should be Expandable")
	}
	if !head.Expanded {
		t.Error("first line should start Expanded (multiline JSON defaults to expanded)")
	}

	// All other lines should be in the same group
	groupID := head.GroupID
	for i := 1; i < m.store.Len(); i++ {
		if m.store.Get(i).GroupID != groupID {
			t.Errorf("line %d: GroupID = %d, want %d", i, m.store.Get(i).GroupID, groupID)
		}
		if m.store.Get(i).GroupHead {
			t.Errorf("line %d: should not be GroupHead", i)
		}
	}

	// Verify prefix is stored in JSONMeta
	meta, ok := head.Meta.(*line.JSONMeta)
	if !ok {
		t.Fatal("first line Meta should be *JSONMeta")
	}
	if !strings.Contains(meta.Prefix, "reportData:") {
		t.Errorf("expected prefix containing 'reportData:', got %q", meta.Prefix)
	}

	// Verify tree is fully expanded with correct structure
	if head.Children == nil {
		t.Fatal("GroupHead should have children (tree auto-expanded)")
	}
	// Top-level keys: name, tests, failures, time, testsuite
	if len(head.Children) != 5 {
		t.Errorf("expected 5 top-level children, got %d", len(head.Children))
	}

	// Verify all expandable descendants are expanded (fully un-collapsed)
	var checkAllExpanded func(l *line.LogLine, path string)
	checkAllExpanded = func(l *line.LogLine, path string) {
		for i, child := range l.Children {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if child.Expandable && !child.Expanded {
				t.Errorf("child %s should be expanded (fully un-collapsed default)", childPath)
			}
			if child.Children != nil {
				checkAllExpanded(child, childPath)
			}
		}
	}
	checkAllExpanded(head, "root")

	// Verify rendered output contains key JSON values
	m.cursor = 0
	view := m.View()
	expectedContent := []string{
		"reportData:", "name", "tests", "failures", "time",
		"testsuite", "myapp-install-create-default",
		"setup", "step 0-install", "step 1-create-cr",
		"failed in step 1-create-cr",
	}
	for _, expected := range expectedContent {
		if !strings.Contains(view, expected) {
			t.Errorf("rendered view missing expected content %q", expected)
		}
	}
}

func TestMultiLineJSONCollapseHidesGroupMembers(t *testing.T) {
	inputLines := []string{
		`prefix:{`,
		`  "key": "value",`,
		`  "num": 42`,
		`}`,
	}

	m := setupModel()
	m.width = 200
	m.height = 20
	m.follow = false
	for _, l := range inputLines {
		m = sendLine(m, l)
	}
	m2, _ := m.Update(ReparseMsg(nil))
	m = m2.(model)

	// Should start expanded
	if !m.store.Get(0).Expanded {
		t.Fatal("expected group head to start expanded")
	}

	// Count content lines (non-empty, non-status-bar)
	countContentLines := func(v string) int {
		count := 0
		for _, vl := range strings.Split(v, "\n") {
			trimmed := strings.TrimSpace(vl)
			if trimmed != "" && !strings.Contains(trimmed, "[loglens]") {
				count++
			}
		}
		return count
	}

	// Expanded: parent line + 2 children (key, num) = 3 visible lines
	m.cursor = 0
	view := m.View()
	expandedCount := countContentLines(view)
	if expandedCount != 3 {
		t.Errorf("expanded view: expected 3 content lines (parent + 2 children), got %d", expandedCount)
	}

	// Collapse with left arrow — first press exits tree if in it, or collapses
	m = sendSpecialKey(m, tea.KeyLeft)
	if m.store.Get(0).Expanded {
		t.Error("expected group head to be collapsed after left arrow")
	}

	// Collapsed: only 1 line (the GroupHead summary)
	view = m.View()
	collapsedCount := countContentLines(view)
	if collapsedCount != 1 {
		t.Errorf("collapsed view: expected 1 content line, got %d", collapsedCount)
	}

	// Re-expand with right arrow and enter tree
	m = sendSpecialKey(m, tea.KeyRight)
	if !m.store.Get(0).Expanded {
		t.Error("expected group head to be expanded after right arrow")
	}
	view = m.View()
	reExpandedCount := countContentLines(view)
	if reExpandedCount != 3 {
		t.Errorf("re-expanded view: expected 3 content lines, got %d", reExpandedCount)
	}
}

func TestCursorAutoEntersExpandedTree(t *testing.T) {
	// Navigating down from a plain line into an expanded JSON should auto-enter the tree.
	// Navigating up from a plain line into an expanded JSON should enter at its last visible node.
	m := setupModel()
	m.width = 200
	m.height = 40
	m.follow = false

	m = sendLine(m, "plain line before")
	m = sendLine(m, `{"alpha": 1, "beta": 2, "gamma": 3}`)
	m = sendLine(m, "plain line after")

	// Cursor is at line 2 (follow mode placed it there; disable follow)
	m.cursor = 0
	m.cursorPath = nil
	m.follow = false

	// Line 1 is inline JSON — expand it manually
	expandAndPopulate(m.store.Get(1))
	if !m.store.Get(1).Expanded || m.store.Get(1).Children == nil {
		t.Fatal("line 1 should be expanded with children")
	}

	// Move down from line 0 to line 1: should auto-enter the tree at first child
	m = sendSpecialKey(m, tea.KeyDown)
	if m.cursor != 1 {
		t.Fatalf("cursor should be on line 1, got %d", m.cursor)
	}
	if m.cursorPath == nil || len(m.cursorPath) != 1 || m.cursorPath[0] != 0 {
		t.Errorf("expected cursorPath=[0] (first child), got %v", m.cursorPath)
	}

	// Navigate down through all children
	m = sendSpecialKey(m, tea.KeyDown) // child [1]
	if m.cursorPath == nil || m.cursorPath[0] != 1 {
		t.Errorf("expected cursorPath=[1], got %v", m.cursorPath)
	}
	m = sendSpecialKey(m, tea.KeyDown) // child [2]
	if m.cursorPath == nil || m.cursorPath[0] != 2 {
		t.Errorf("expected cursorPath=[2], got %v", m.cursorPath)
	}

	// Down from last child exits tree and moves to line 2
	m = sendSpecialKey(m, tea.KeyDown)
	if m.cursor != 2 {
		t.Errorf("expected cursor on line 2, got %d", m.cursor)
	}
	if m.cursorPath != nil {
		t.Errorf("expected nil cursorPath after exiting tree, got %v", m.cursorPath)
	}

	// Now go back up: should auto-enter tree at last visible node
	m = sendSpecialKey(m, tea.KeyUp)
	if m.cursor != 1 {
		t.Fatalf("cursor should be on line 1, got %d", m.cursor)
	}
	// Last visible child should be [2] (gamma)
	if m.cursorPath == nil || m.cursorPath[0] != 2 {
		t.Errorf("expected cursorPath=[2] (last child), got %v", m.cursorPath)
	}

	// Navigate up through all children back to parent line
	m = sendSpecialKey(m, tea.KeyUp) // child [1]
	m = sendSpecialKey(m, tea.KeyUp) // child [0]
	m = sendSpecialKey(m, tea.KeyUp) // exit tree to parent line
	if m.cursor != 1 || m.cursorPath != nil {
		t.Errorf("expected cursor=1, cursorPath=nil, got cursor=%d path=%v", m.cursor, m.cursorPath)
	}

	// Up again goes to line 0
	m = sendSpecialKey(m, tea.KeyUp)
	if m.cursor != 0 {
		t.Errorf("expected cursor=0, got %d", m.cursor)
	}
}

// In follow mode, expanding the last log line (right key) and then receiving
// a tickMsg used to wipe cursorPath back to nil — making it impossible to
// navigate into the freshly-expanded tree at EOF, since every ~33ms tick
// reset the cursor to the parent line.
func TestFollowModeTickPreservesCursorPathOnLastLine(t *testing.T) {
	m := setupModel()
	m.width = 200
	m.height = 40

	m = sendLine(m, "plain line before")
	m = sendLine(m, `{"alpha": 1, "beta": 2, "gamma": 3}`)

	if !m.follow || m.cursor != 1 {
		t.Fatalf("expected follow mode with cursor on last line, got follow=%v cursor=%d", m.follow, m.cursor)
	}

	// Expand the JSON line via right arrow — should set cursorPath=[0].
	m = sendSpecialKey(m, tea.KeyRight)
	if !m.store.Get(1).Expanded {
		t.Fatal("expected line 1 to be expanded after right arrow")
	}
	if m.cursorPath == nil || len(m.cursorPath) != 1 || m.cursorPath[0] != 0 {
		t.Fatalf("expected cursorPath=[0] after right arrow, got %v", m.cursorPath)
	}

	// A tickMsg in follow mode must NOT clobber cursorPath when the cursor
	// hasn't moved (no new lines have arrived).
	m2, _ := m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if m.cursorPath == nil || len(m.cursorPath) != 1 || m.cursorPath[0] != 0 {
		t.Fatalf("tickMsg wiped cursorPath in follow mode at EOF: got %v", m.cursorPath)
	}

	// Now down should navigate within the tree, not be undone by ticks.
	m = sendSpecialKey(m, tea.KeyDown)
	if m.cursorPath == nil || m.cursorPath[0] != 1 {
		t.Errorf("expected cursorPath=[1] after down, got %v", m.cursorPath)
	}
	m2, _ = m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if m.cursorPath == nil || m.cursorPath[0] != 1 {
		t.Errorf("tickMsg wiped cursorPath after down: got %v", m.cursorPath)
	}
}

// When a new line arrives in follow mode, cursorPath SHOULD be cleared — the
// cursor jumps to the new last line and any prior in-tree position is gone.
func TestFollowModeNewLineResetsCursorPath(t *testing.T) {
	m := setupModel()
	m.width = 200
	m.height = 40

	m = sendLine(m, "plain line")
	m = sendLine(m, `{"alpha": 1, "beta": 2}`)
	m = sendSpecialKey(m, tea.KeyRight) // expand → cursorPath=[0]
	if m.cursorPath == nil {
		t.Fatal("expected cursorPath to be set after right")
	}

	// New line arrives → follow yanks cursor to it, cursorPath must clear.
	m = sendLine(m, "another plain line")
	if m.cursor != 2 {
		t.Errorf("expected cursor=2 after new line, got %d", m.cursor)
	}
	if m.cursorPath != nil {
		t.Errorf("expected cursorPath=nil after follow-jump to new line, got %v", m.cursorPath)
	}
}

func TestCursorSkipsHiddenGroupMembers(t *testing.T) {
	// Multiline JSON group members (non-head) should be skipped during navigation.
	m := setupModel()
	m.width = 200
	m.height = 40
	m.follow = false

	m = sendLine(m, "plain line before")
	// Multiline JSON spanning 4 lines
	m = sendLine(m, `{`)
	m = sendLine(m, `  "key": "value"`)
	m = sendLine(m, `}`)
	m = sendLine(m, "plain line after")

	// Lines 1-3 should be a JSON group, line 1 is head, 2-3 are hidden members
	head := m.store.Get(1)
	if !head.GroupHead || head.GroupID == 0 {
		t.Fatal("line 1 should be GroupHead with non-zero GroupID")
	}

	// Start at line 0, navigate down
	m.cursor = 0
	m.cursorPath = nil

	// Down should go to line 1 (group head) and auto-enter its tree
	m = sendSpecialKey(m, tea.KeyDown)
	if m.cursor != 1 {
		t.Errorf("expected cursor=1, got %d", m.cursor)
	}

	// Navigate through tree, then exit — should skip to line 4 (past hidden members)
	// First collapse the tree so we can test line skipping
	collapseAll(m.store.Get(1))
	m.cursorPath = nil

	// Now down from collapsed line 1 should skip hidden lines 2,3 and go to line 4
	m = sendSpecialKey(m, tea.KeyDown)
	if m.cursor != 4 {
		t.Errorf("expected cursor=4 (skipping hidden members), got %d", m.cursor)
	}

	// Up from line 4 should go back to line 1 (skipping hidden 2,3)
	m = sendSpecialKey(m, tea.KeyUp)
	if m.cursor != 1 {
		t.Errorf("expected cursor=1 (skipping hidden members backwards), got %d", m.cursor)
	}
}

func TestViewportPartialTreeDisplay(t *testing.T) {
	// Regression test: when an expanded JSON tree has more rows than the viewport,
	// the viewport should show the relevant portion (not all-or-nothing).
	// Scenario: ~42-row tree + 1 trailing line, 16-row viewport, follow mode.
	// Expected: last 16 rows visible (bottom of tree + trailing line), NOT just
	// the trailing line with 15 blank lines.
	m := setupModel()
	m.width = 200
	m.height = 17 // 16 viewport + 1 status bar
	m.follow = true

	// Feed multiline JSON that will produce a large expanded tree (~42 rows)
	m = sendLine(m, `{`)
	for i := 0; i < 40; i++ {
		m = sendLine(m, fmt.Sprintf(`  "key%d": "value%d",`, i, i))
	}
	m = sendLine(m, `  "last": "item"`)
	m = sendLine(m, `}`)
	m = sendLine(m, "plain line after json")

	// Follow mode should have cursor on the trailing line.
	if m.cursor != m.store.Len()-1 {
		t.Fatalf("follow mode: cursor=%d, want %d", m.cursor, m.store.Len()-1)
	}

	// The view should contain the trailing line (cursor line)
	view := m.View()
	if !strings.Contains(view, "plain line after json") {
		t.Error("viewport should show the trailing line (cursor position)")
	}

	// Count non-empty content lines in the viewport
	contentLines := 0
	for _, vl := range strings.Split(view, "\n") {
		trimmed := strings.TrimSpace(vl)
		if trimmed != "" && !strings.Contains(trimmed, "[loglens]") {
			contentLines++
		}
	}

	// The viewport (16 rows) should be mostly filled with tree content + trailing line.
	// Before the fix, only 1 line was visible (the trailing line) + 15 blank lines.
	if contentLines < 10 {
		t.Errorf("viewport should be mostly filled, got only %d content lines (tree exceeds viewport, so partial tree should be visible)", contentLines)
	}

	// Now move cursor up — should stay visible, not jump to tree top
	m = sendSpecialKey(m, tea.KeyUp)
	view = m.View()

	// The cursor is now inside the JSON tree (last visible node).
	if m.cursorPath == nil {
		t.Error("cursor should have entered the expanded tree")
	}

	// The trailing plain line should still be visible (it was 1 row below cursor)
	if !strings.Contains(view, "plain line after json") {
		t.Error("after moving up once, trailing line should still be visible")
	}
}

func TestMultiLineJSONCase2TwoTestsuites(t *testing.T) {
	// Case 2: multiline JSON with TWO testsuite entries (84 lines total).
	// Tests that the buffer scan limit doesn't prevent outer JSON detection.
	inputLines := []string{
		`    kuttl_ctrf_converter.go:21: reportData:{`,
		`           "name": "",`,
		`           "tests": 6,`,
		`           "failures": 2,`,
		`           "time": "267.231",`,
		`           "testsuite": [`,
		`             {`,
		`               "tests": 6,`,
		`               "failures": 2,`,
		`               "timestamp": "2026-04-14T16:51:27.149525559+02:00",`,
		`               "time": "267.221",`,
		`               "name": "/home/user/project/tests/e2e/myapp/kuttl",`,
		`               "testsuite": [`,
		`                 {`,
		`                   "tests": 3,`,
		`                   "failures": 1,`,
		`                   "timestamp": "2026-04-14T16:51:27.150797783+02:00",`,
		`                   "time": "133.675",`,
		`                   "name": "myapp-create-default",`,
		`                   "testcase": [`,
		`                     {`,
		`                       "classname": "myapp-create-default",`,
		`                       "name": "setup",`,
		`                       "timestamp": "2026-04-14T16:53:40.825315562+02:00",`,
		`                       "time": "0.000"`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-create-default",`,
		`                       "name": "step 0-install",`,
		`                       "timestamp": "2026-04-14T16:53:40.825321013+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 1`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-create-default",`,
		`                       "name": "step 1-gotest",`,
		`                       "timestamp": "2026-04-14T16:53:40.825323451+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 3,`,
		`                       "failure": {`,
		`                         "text": "resource Deployment:my-operator-system/my-deployment: .spec.template.spec.containers.ports: slice length mismatch: 3 != 1",`,
		`                         "message": "failed in step 1-gotest"`,
		`                       }`,
		`                     }`,
		`                   ]`,
		`                 },`,
		`                 {`,
		`                   "tests": 3,`,
		`                   "failures": 1,`,
		`                   "timestamp": "2026-04-14T16:53:40.826257656+02:00",`,
		`                   "time": "133.544",`,
		`                   "name": "myapp-readiness-liveness",`,
		`                   "testcase": [`,
		`                     {`,
		`                       "classname": "myapp-readiness-liveness",`,
		`                       "name": "setup",`,
		`                       "timestamp": "2026-04-14T16:55:54.370187417+02:00",`,
		`                       "time": "0.000"`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-readiness-liveness",`,
		`                       "name": "step 0-install",`,
		`                       "timestamp": "2026-04-14T16:55:54.370191708+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 1`,
		`                     },`,
		`                     {`,
		`                       "classname": "myapp-readiness-liveness",`,
		`                       "name": "step 1-gotest",`,
		`                       "timestamp": "2026-04-14T16:55:54.370193785+02:00",`,
		`                       "time": "0.000",`,
		`                       "assertions": 3,`,
		`                       "failure": {`,
		`                         "text": "resource Deployment:my-operator-system/my-deployment: .spec.template.spec.containers.livenessProbe.httpGet.path: value mismatch, expected: /api/custom-method-and-status/livez != actual: /health",`,
		`                         "message": "failed in step 1-gotest"`,
		`                       }`,
		`                     }`,
		`                   ]`,
		`                 }`,
		`               ]`,
		`             }`,
		`           ]`,
		`         }`,
		`    kuttl_ctrf_converter.go:34: testsuite from report kuttl:1`,
	}

	m := setupModel()
	m.width = 300
	m.height = 120
	m.follow = false
	for _, l := range inputLines {
		m = sendLine(m, l)
	}
	m2, _ := m.Update(ReparseMsg(nil))
	m = m2.(model)

	// All JSON lines (0-82) should be in one group; line 83 is plain
	if m.store.Len() != len(inputLines) {
		t.Fatalf("expected %d lines, got %d", len(inputLines), m.store.Len())
	}

	head := m.store.Get(0)
	if !head.GroupHead {
		t.Fatal("line 0 should be GroupHead")
	}
	if head.GroupID == 0 {
		t.Fatal("line 0 should have non-zero GroupID")
	}

	// All JSON lines (0 through 82) should share the same group
	groupID := head.GroupID
	for i := 1; i <= 82; i++ {
		if m.store.Get(i).GroupID != groupID {
			t.Errorf("line %d: GroupID = %d, want %d (same group as head)", i, m.store.Get(i).GroupID, groupID)
			break
		}
	}

	// Line 83 should NOT be in the JSON group
	if m.store.Get(83).GroupID == groupID {
		t.Error("line 83 should not be in the JSON group")
	}

	// Tree should be fully expanded with correct structure
	if head.Children == nil {
		t.Fatal("GroupHead should have children")
	}
	if len(head.Children) != 5 {
		t.Errorf("expected 5 top-level children (name, tests, failures, time, testsuite), got %d", len(head.Children))
	}

	// Navigate to the "testsuite" array child and verify it has 1 element
	// which in turn has a "testsuite" array with 2 entries
	var testsuiteChild *line.LogLine
	for _, c := range head.Children {
		if strings.Contains(c.Raw, "testsuite") {
			testsuiteChild = c
			break
		}
	}
	if testsuiteChild == nil {
		t.Fatal("missing 'testsuite' child")
	}
	if !testsuiteChild.Expanded {
		t.Error("testsuite child should be expanded")
	}
	if testsuiteChild.Children == nil || len(testsuiteChild.Children) != 1 {
		t.Fatalf("testsuite should have 1 element, got %d", len(testsuiteChild.Children))
	}

	// The inner element [0] should have a nested "testsuite" with 2 entries
	innerElement := testsuiteChild.Children[0]
	if innerElement.Children == nil {
		t.Fatal("inner testsuite element should have children")
	}
	var innerTestsuite *line.LogLine
	for _, c := range innerElement.Children {
		if strings.Contains(c.Raw, "testsuite") {
			innerTestsuite = c
			break
		}
	}
	if innerTestsuite == nil {
		t.Fatal("missing inner 'testsuite' child")
	}
	if innerTestsuite.Children == nil || len(innerTestsuite.Children) != 2 {
		t.Fatalf("inner testsuite should have 2 entries, got %v", len(innerTestsuite.Children))
	}

	// Both entries should be expanded and contain "testcase" arrays
	for idx, entry := range innerTestsuite.Children {
		if !entry.Expanded {
			t.Errorf("inner testsuite[%d] should be expanded", idx)
		}
	}

	// Verify the view contains content from both testsuite entries
	view := m.View()
	if !strings.Contains(view, "myapp-create-default") {
		t.Error("view should contain 'myapp-create-default'")
	}
	if !strings.Contains(view, "myapp-readiness-liveness") {
		t.Error("view should contain 'myapp-readiness-liveness'")
	}
}

// benchRenderStyles returns a render.Styles built from DefaultStyles. Shared by
// benchmarks below so each new bench case can reuse the same setup.
func benchRenderStyles() *render.Styles {
	s := DefaultStyles()
	return &render.Styles{
		CursorLine:      s.CursorLine,
		JSONKey:         s.JSONKey,
		JSONString:      s.JSONString,
		JSONNumber:      s.JSONNumber,
		JSONBool:        s.JSONBool,
		JSONNull:        s.JSONNull,
		JSONBrace:       s.JSONBrace,
		DiffAdd:         s.DiffAdd,
		DiffRemove:      s.DiffRemove,
		DiffHunk:        s.DiffHunk,
		DiffHeader:      s.DiffHeader,
		GoTestPass:      s.GoTestPass,
		GoTestFail:      s.GoTestFail,
		GoTestSkip:      s.GoTestSkip,
		GoTestRun:       s.GoTestRun,
		GoTestDuration:  s.GoTestDuration,
		WarnPrefix:      s.WarnPrefix,
		ErrorPrefix:     s.ErrorPrefix,
		InfoPrefix:      s.InfoPrefix,
		DebugPrefix:     s.DebugPrefix,
		Timestamp:       s.Timestamp,
		Datetime:        s.Datetime,
		SourceRef:       s.SourceRef,
		K8sResource:     s.K8sResource,
		K8sEventNormal:  s.K8sEventNormal,
		K8sEventWarning: s.K8sEventWarning,
		LevelError:      s.LevelError,
		LevelWarn:       s.LevelWarn,
		LevelInfo:       s.LevelInfo,
		LevelDebug:      s.LevelDebug,
		NginxField:      s.NginxField,
		IPAddr:          s.IPAddr,
		FailedStep:      s.FailedStep,
		TableHeader:     s.TableHeader,
		TableCell:       s.TableCell,
		TableSep:        s.TableSep,
		StderrGutter:    s.StderrGutter,
		ExpandIndicator: s.ExpandIndicator,
		SearchMatch:     s.SearchMatch,
		Plain:           s.Plain,
	}
}

// benchSampleLines is a representative mix of log line shapes the parser
// handles: logger-prefixed, K8s resource, inline JSON, go-test marker, case.go.
var benchSampleLines = []string{
	"    logger.go:42: 15:44:39 | myapp-install-create-default/0-install | starting test step 0-install",
	"    logger.go:42: 15:44:39 | myapp-install-create-default/0-install | ConfigMap:my-operator-system/my-operator-cm created",
	`{"level":"info","ts":"2026-04-14T15:47:00Z","msg":"reconciling myapp","ns":"my-operator-system"}`,
	"=== RUN   TestE2eKuttlMyAppInstall/harness/myapp-install-create-default",
	"    case.go:364: myapp-install-create-default | 0-install: starting test step",
}

// buildBenchModel constructs a model populated with N parsed sample lines.
// Width/height are fixed to typical values; wrapMode is configurable.
func buildBenchModel(n int, wrapMode bool) model {
	p := parser.New()
	for i := 0; i < n; i++ {
		p.Parse(benchSampleLines[i%len(benchSampleLines)], false)
	}
	lines := p.Lines()
	rs := benchRenderStyles()
	ls := store.NewFromSlice(lines)
	shared := newSharedState(ls)
	shared.parser = p
	m := model{
		s:        shared,
		store:    ls,
		width:    200,
		height:   50,
		wrapMode: wrapMode,
		cursor:   0,
		rStyles:  rs,
	}
	m.rebuildVisRows()
	return m
}

func BenchmarkRebuildVisRows(b *testing.B) {
	// Measures the wrap-toggle / resize hot path on a 100K-line file.
	m := buildBenchModel(100000, true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.rebuildVisRows()
	}
}

// BenchmarkCursorMoveDown measures the cost of a single down-key press on a
// large file (recomputeVisRows for old/new cursor line + adjustOffset). Run
// with: `go test -bench BenchmarkCursorMove -benchtime=100000x -run=^$`.
func BenchmarkCursorMoveDown(b *testing.B) {
	benchCursorMove(b, false, tea.KeyDown)
}

func BenchmarkCursorMoveUp(b *testing.B) {
	benchCursorMove(b, false, tea.KeyUp)
}

func BenchmarkCursorMoveDownWrap(b *testing.B) {
	benchCursorMove(b, true, tea.KeyDown)
}

func BenchmarkCursorMoveUpWrap(b *testing.B) {
	benchCursorMove(b, true, tea.KeyUp)
}

func benchCursorMove(b *testing.B, wrapMode bool, key tea.KeyType) {
	const N = 100000
	m := buildBenchModel(N, wrapMode)
	// Start mid-file with follow off so up/down walks real lines.
	m.follow = false
	m.cursor = N / 2
	m.adjustOffset()
	keyMsg := tea.KeyMsg{Type: key}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m2, _ := m.Update(keyMsg)
		m = m2.(model)
		// Bounce when we hit either end so the bench keeps measuring real moves
		// instead of no-ops against the boundary.
		if m.cursor <= 0 || m.cursor >= m.store.Len()-1 {
			m.cursor = N / 2
			m.cursorPath = nil
			m.follow = false
		}
	}
}

// BenchmarkCursorMoveInTree measures the hot path the user hit in
// `--bench out.txt`: cursor is sitting on an expanded JSON line and each
// up/down press only moves cursorPath within the tree. This exercises
// absoluteVisualRow + visualRowsForLine on an expanded parent — previously
// RenderExpanded was called twice per press (~1 ms each); the arithmetic
// path should drop both to µs.
func BenchmarkCursorMoveInTree(b *testing.B) {
	m := buildBenchInTreeModel()
	keyMsg := tea.KeyMsg{Type: tea.KeyDown}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m2, _ := m.Update(keyMsg)
		m = m2.(model)
		// If we walked off the end of the tree (cursorPath==nil, cursor moved
		// to next line), reset back onto the first child of the tree.
		if m.cursorPath == nil {
			l := m.store.Get(inTreeLineIdx)
			m.cursor = inTreeLineIdx
			if len(l.Children) > 0 {
				m.cursorPath = []int{0}
			}
		}
	}
}

// inTreeLineIdx is the index in buildBenchInTreeModel where the expanded
// JSON tree lives. Kept as a package-level const so the bench reset path
// after overshooting stays in sync with setup.
const inTreeLineIdx = 10

// buildBenchInTreeModel returns a model positioned on an expanded JSON tree
// with ~40 visible descendants — representative of the failure case the user
// hit in the runtime bench (cursor stuck at 141795 for 26 up-presses).
func buildBenchInTreeModel() model {
	const N = 50000
	p := parser.New()
	for i := 0; i < inTreeLineIdx; i++ {
		p.Parse(benchSampleLines[i%len(benchSampleLines)], false)
	}
	// A JSON line with enough keys that its expanded tree is deep enough to
	// exercise both arithmetic walks and cursorRow computation.
	jsonLine := `{"tests":6,"failures":2,"name":"bench","ts":"2026-04-16T12:00:00Z","meta":{"a":1,"b":2,"c":3,"d":4},"list":[{"k":"v0"},{"k":"v1"},{"k":"v2"},{"k":"v3"}],"err":null,"details":"some fairly long descriptive text about the failure for good measure"}`
	p.Parse(jsonLine, false)
	for i := inTreeLineIdx + 1; i < N; i++ {
		p.Parse(benchSampleLines[i%len(benchSampleLines)], false)
	}
	lines := p.Lines()
	rs := benchRenderStyles()
	ls := store.NewFromSlice(lines)
	shared := newSharedState(ls)
	shared.parser = p
	m := model{
		s:       shared,
		store:   ls,
		width:   200,
		height:  50,
		cursor:  inTreeLineIdx,
		rStyles: rs,
	}
	expandAndPopulate(m.store.Get(inTreeLineIdx))
	expandAllDescendants(m.store.Get(inTreeLineIdx))
	m.rebuildVisRows()
	m.cursorPath = []int{0}
	return m
}
