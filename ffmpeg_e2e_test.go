package main

import (
	"github.com/wufe/loglens/input"
	"github.com/wufe/loglens/line"
	"os"
	"strings"
	"testing"
)

// TestFFmpegExampleFixtureCoalescesToOneLine feeds the real-world fixture in
// testdata/ffmpeg-out.example.txt through the ingest path and verifies that
// the whole multi-block stream collapses into a single TypeFFmpegProgress
// line which ends at 100% (the fixture terminates with progress=end).
func TestFFmpegExampleFixtureCoalescesToOneLine(t *testing.T) {
	data, err := os.ReadFile("testdata/ffmpeg-out.example.txt")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	m := setupModel()
	m.s.mu.Lock()
	for _, txt := range lines {
		m.s.ingestOneLocked(input.RawLine{Text: txt, Source: input.SourceStdin})
	}
	m.s.mu.Unlock()

	if got := m.store.Len(); got != 1 {
		t.Fatalf("expected 1 store line for the whole fixture, got %d", got)
	}
	l := m.store.Get(0)
	if l.Type != line.TypeFFmpegProgress {
		t.Fatalf("type = %v, want TypeFFmpegProgress", l.Type)
	}
	meta := l.Meta.(*line.FFmpegMeta)
	if !meta.Ended {
		t.Errorf("fixture ends with progress=end; expected Ended=true")
	}
	if meta.Percent != 1.0 {
		t.Errorf("Percent after end = %f, want 1.0", meta.Percent)
	}
}

// TestFFmpegInterleavedWithNormalLogsRendersBothKinds walks through a
// representative interleaving: normal log → ffmpeg session aborted at 90%
// → recognizable marker → more normal logs. The marker must survive as its
// own line with the original text intact, and the bar must stay frozen.
func TestFFmpegInterleavedWithNormalLogsRendersBothKinds(t *testing.T) {
	m := setupModel()

	m = feedLines(m, "startup log line 1", "startup log line 2")

	// Mid-run ffmpeg session, aborted.
	for i := 0; i < 5; i++ {
		m = feedLines(m, ffmpegBlock(100+i*50, "00:00:0"+itoaSmall(i+1), "continue")...)
	}

	const marker = "!!! marker following abort !!!"
	m = feedLines(m, marker, "trailing log 1", "trailing log 2")

	// Expected layout:
	//   [0] startup 1
	//   [1] startup 2
	//   [2] frozen ffmpeg progress line
	//   [3] marker
	//   [4] trailing 1
	//   [5] trailing 2
	if got := m.store.Len(); got != 6 {
		t.Fatalf("expected 6 lines, got %d", got)
	}
	bar := m.store.Get(2)
	if bar.Type != line.TypeFFmpegProgress {
		t.Fatalf("line[2] type = %v, want TypeFFmpegProgress", bar.Type)
	}
	meta := bar.Meta.(*line.FFmpegMeta)
	if !meta.Frozen {
		t.Errorf("bar not frozen after non-ffmpeg line")
	}
	if meta.Ended {
		t.Errorf("bar should not be Ended when aborted")
	}
	if m.store.Get(3).Raw != marker {
		t.Errorf("marker mangled: got %q", m.store.Get(3).Raw)
	}
	if m.store.Get(5).Raw != "trailing log 2" {
		t.Errorf("last line mangled: got %q", m.store.Get(5).Raw)
	}
}
