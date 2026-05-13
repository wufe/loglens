package main

import (
	"github.com/wufe/loglens/input"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRealIngestEOFViewport drives the model with the real ingestor goroutine
// and simulated tick messages, mirroring the production pipe-mode flow as
// closely as possible. The synchronous LineMsg path used by setupModel
// finalizes the cursor + adjust on every line — production batches lines and
// only the trailing tickMsg pulls the cursor to the new tail. This test
// exercises that batched flow end-to-end so we catch races that the simpler
// LineMsg-driven tests miss.
func TestRealIngestEOFViewport(t *testing.T) {
	data, err := os.ReadFile("testdata/eof_repro_logs.txt")
	if err != nil {
		t.Skip("testdata/eof_repro_logs.txt not present")
	}

	src := newMockSource()
	// Match production: model starts with width=0, ingestor begins streaming
	// immediately. WindowSizeMsg arrives before the first tick and rebuilds
	// visRows. This catches bugs where lines ingested with width=0 leave the
	// visRows slice in a state that the subsequent rebuild misses.
	m := initialModel(src, false, nil, 0)

	// Push some lines BEFORE the WindowSizeMsg arrives so the ingestor sees
	// a 0-width sharedState and writes visRows[i]=1 entries.
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n") //nolint:stringsseq
	half := len(lines) / 2
	go func() {
		for _, raw := range lines[:half] {
			src.ch <- input.RawLine{Text: raw, Source: input.SourceStdin}
		}
		// Brief gap so the ingestor can drain before WindowSize fires.
		time.Sleep(10 * time.Millisecond)
		for _, raw := range lines[half:] {
			src.ch <- input.RawLine{Text: raw, Source: input.SourceStdin}
		}
		close(src.ch)
	}()

	// Let the ingestor pick up some lines while width is still 0.
	time.Sleep(5 * time.Millisecond)

	// Now WindowSizeMsg arrives, mirroring tea's startup ordering.
	var mod0 tea.Model = m
	mod0, _ = mod0.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mod0.(model)

	// Pump tickMsgs until store length stabilizes AND eof is observed by the
	// model. The production tea loop fires these every 33ms; we can fire
	// faster in the test, but keep small sleeps so the ingestor goroutine
	// gets scheduled between ticks.
	deadline := time.Now().Add(2 * time.Second)
	stable := 0
	prevLen := -1
	var mod tea.Model = m
	for time.Now().Before(deadline) {
		mod, _ = mod.Update(tickMsg(time.Now()))
		mm := mod.(model)
		// Production reads store length under s.mu; the ingestor is still
		// running here, so a bare mm.store.Len() races with Append.
		mm.s.mu.RLock()
		curLen := mm.s.store.Len()
		mm.s.mu.RUnlock()
		if curLen == prevLen && mm.eof {
			stable++
			if stable > 3 {
				m = mm
				break
			}
		} else {
			stable = 0
		}
		prevLen = curLen
		time.Sleep(2 * time.Millisecond)
	}
	if m.ingestor != nil {
		m.ingestor.stop()
	}

	if m.store.Len() == 0 {
		t.Fatal("ingestor produced no lines")
	}
	if !m.eof {
		t.Fatalf("expected eof reached, store.Len=%d", m.store.Len())
	}
	if m.cursor != m.store.Len()-1 {
		t.Errorf("cursor=%d, want last (%d)", m.cursor, m.store.Len()-1)
	}

	out := m.View()
	plain := stripANSIEscapes(out)
	if !flatContains(plain, "FINAL_MARKER reconcile cycle complete") {
		t.Errorf("expected last log line's distinctive marker in viewport at EOF.\nViewport:\n%s",
			plain)
	}

	// Now reproduce the user's follow-up complaint: pressing up several times
	// after EOF should keep the cursor inside the viewport, not collapse the
	// page to a single line.
	for range 5 {
		mod0, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyUp})
		m = mod0.(model)
	}

	if m.follow {
		t.Errorf("up-arrow must disable follow mode, got follow=true")
	}
	if m.cursor != 256 {
		t.Errorf("cursor=%d after 5 up presses from 261, want 256", m.cursor)
	}

	out2 := m.View()
	plain2 := stripANSIEscapes(out2)
	// Count visible log-content rows (skip the status bar). The user's bug
	// collapsed the viewport to a single line. We expect the full vh worth
	// of rows to carry log content.
	rows := strings.Split(plain2, "\n")
	contentRows := 0
	for i, row := range rows {
		// The status bar is the last non-empty row and starts with " [loglens]".
		if strings.Contains(row, "[loglens]") {
			break
		}
		if strings.TrimSpace(row) != "" {
			contentRows++
		}
		_ = i
	}
	if contentRows < 10 {
		t.Errorf("after 5 up presses viewport collapsed to %d content rows; expected ~vh worth.\nViewport:\n%s",
			contentRows, plain2)
	}
}

// flatContains compares haystack to needle ignoring all whitespace, so a
// substring split across wrap boundaries still matches.
func flatContains(haystack, needle string) bool {
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
				return -1
			}
			return r
		}, s)
	}
	return strings.Contains(strip(haystack), strip(needle))
}
