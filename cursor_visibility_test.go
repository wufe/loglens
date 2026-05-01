package main

import (
	"os"
	"strings"
	"testing"
)

// TestCursorVisibleAtEOFLongLine reproduces the EOF-pagination bug: when the
// last line is long enough to wrap into multiple visual rows, the cursor's
// wrapped tail must fit inside the viewport, not extend past the bottom.
func TestCursorVisibleAtEOFLongLine(t *testing.T) {
	m := setupModel() // width=80, height=24 → vh=23

	// 30 short lines (each 1 visual row).
	for range 30 {
		m = sendLine(m, "short")
	}
	// One long line at the end. ~600 chars + 3-char prefix = ~8 wrapped rows.
	m = sendLine(m, strings.Repeat("x", 600))

	if !m.follow {
		t.Fatalf("expected follow mode")
	}
	if m.cursor != m.store.Len()-1 {
		t.Fatalf("cursor=%d, want %d (last)", m.cursor, m.store.Len()-1)
	}

	vh := m.viewportHeight()
	cursorAbsRow := m.absoluteVisualRow(m.cursor, m.cursorPath)
	cursorRows := m.cursorVisualHeightLocked()
	offsetAbsRow := m.absoluteVisualRow(m.offset, nil) + m.offsetRow

	cursorEndRow := cursorAbsRow + cursorRows
	viewportEnd := offsetAbsRow + vh
	if cursorEndRow > viewportEnd {
		t.Errorf("cursor's last row (%d) is below viewport end (%d): cursor=[%d..%d), viewport=[%d..%d), vh=%d, cursorRows=%d",
			cursorEndRow-1, viewportEnd-1,
			cursorAbsRow, cursorEndRow,
			offsetAbsRow, viewportEnd,
			vh, cursorRows)
	}
}

// TestCursorVisibleAtEOFWrapMode verifies the same property in wrap mode,
// where every line — not just the cursor — wraps. Pre-fix the offset math
// stopped at the cursor's first row, so toggling wrap with `w` left the
// last line ending mid-page rather than aligning at the viewport bottom.
func TestCursorVisibleAtEOFWrapMode(t *testing.T) {
	m := setupModel()
	m.wrapMode = true
	m.s.wrapMode = true

	long := strings.Repeat("z", 400) // ~5 wrapped rows each
	for range 25 {
		m = sendLine(m, long)
	}

	if !m.follow {
		t.Fatalf("expected follow mode")
	}

	vh := m.viewportHeight()
	cursorAbsRow := m.absoluteVisualRow(m.cursor, m.cursorPath)
	cursorRows := m.cursorVisualHeightLocked()
	offsetAbsRow := m.absoluteVisualRow(m.offset, nil) + m.offsetRow

	cursorEndRow := cursorAbsRow + cursorRows
	if cursorEndRow > offsetAbsRow+vh {
		t.Errorf("wrap-mode EOF: cursor end row (%d) past viewport end (%d): cursor=[%d..%d), viewport=[%d..%d)",
			cursorEndRow-1, offsetAbsRow+vh-1,
			cursorAbsRow, cursorEndRow,
			offsetAbsRow, offsetAbsRow+vh)
	}
}

// TestEOFViewportShowsLastLineFromTestdata reproduces the exact scenario the
// user reported: piping a 262-line k8s-style log into loglens left the cursor
// invisible at EOF because the long final lines wrap past the viewport
// bottom. The synthetic input in testdata/eof_repro_logs.txt mirrors the
// shape of the original repro (line count, average length, last-line wrap)
// without any real-world identifiers.
func TestEOFViewportShowsLastLineFromTestdata(t *testing.T) {
	data, err := os.ReadFile("testdata/eof_repro_logs.txt")
	if err != nil {
		t.Skip("testdata/eof_repro_logs.txt not present")
	}
	m := setupModel()
	for _, raw := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		m = sendLine(m, raw)
	}

	out := m.View()
	rows := strings.Split(out, "\n")

	// The generator stamps the last line with a stable distinctive marker.
	const want = "FINAL_MARKER"

	var found bool
	for _, row := range rows {
		if strings.Contains(stripANSIEscapes(row), want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected the last log line's marker %q to appear in the rendered viewport at EOF", want)
	}
}

// TestCursorVisibleAfterUpFromEOF presses up from the end of a stream of
// long lines and verifies the cursor stays inside the viewport. The bug
// before was that visRows for the new cursor didn't get refreshed before
// adjustOffsetLocked ran, so the offset was computed against a stale BIT.
func TestCursorVisibleAfterUpFromEOF(t *testing.T) {
	m := setupModel()
	long := strings.Repeat("y", 500) // ~7 wrapped rows each
	for range 40 {
		m = sendLine(m, long)
	}
	for range 5 {
		m = sendKey(m, "k")
	}

	vh := m.viewportHeight()
	cursorAbsRow := m.absoluteVisualRow(m.cursor, m.cursorPath)
	cursorRows := m.cursorVisualHeightLocked()
	offsetAbsRow := m.absoluteVisualRow(m.offset, nil) + m.offsetRow

	if cursorAbsRow < offsetAbsRow {
		t.Errorf("cursor (%d) above viewport top (%d)", cursorAbsRow, offsetAbsRow)
	}
	cursorEndRow := cursorAbsRow + cursorRows
	if cursorEndRow > offsetAbsRow+vh {
		t.Errorf("cursor's last row (%d) below viewport end (%d): cursor=[%d..%d), viewport=[%d..%d), vh=%d",
			cursorEndRow-1, offsetAbsRow+vh-1,
			cursorAbsRow, cursorEndRow,
			offsetAbsRow, offsetAbsRow+vh,
			vh)
	}
}
