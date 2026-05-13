package main

import (
	"github.com/wufe/loglens/input"
	"github.com/wufe/loglens/line"
	"testing"
)

// feedLines pushes each raw line through the shared-state ingest path exactly
// like the production ingestor does, bypassing the Bubble Tea message loop.
func feedLines(m model, lines ...string) model {
	m.s.mu.Lock()
	for _, txt := range lines {
		m.s.ingestOneLocked(input.RawLine{Text: txt, Source: input.SourceStdin})
	}
	m.s.mu.Unlock()
	return m
}

// ffmpegBlock returns the key=value lines for one progress block. The caller
// decides whether to terminate with progress=continue or progress=end.
func ffmpegBlock(frame int, outTime string, terminator string) []string {
	return []string{
		"frame=" + itoaSmall(frame),
		"fps=60",
		"stream_0_0_q=25.0",
		"bitrate= 400kbits/s",
		"total_size=1024",
		"out_time_us=1000000",
		"out_time_ms=1000000",
		"out_time=" + outTime,
		"dup_frames=0",
		"drop_frames=0",
		"speed=1.0x",
		"progress=" + terminator,
	}
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestFFmpegCoalescesIntoSingleLine(t *testing.T) {
	m := setupModel()

	// Three back-to-back progress blocks should all collapse into ONE line.
	for i, frame := range []int{100, 200, 300} {
		b := ffmpegBlock(frame, "00:00:0"+itoaSmall(i+1), "continue")
		m = feedLines(m, b...)
	}

	if got := m.store.Len(); got != 1 {
		t.Fatalf("expected 1 line after 3 ffmpeg blocks, got %d", got)
	}
	l := m.store.Get(0)
	if l.Type != line.TypeFFmpegProgress {
		t.Fatalf("expected TypeFFmpegProgress, got %v", l.Type)
	}
	meta, ok := l.Meta.(*line.FFmpegMeta)
	if !ok {
		t.Fatalf("expected *FFmpegMeta, got %T", l.Meta)
	}
	if meta.BlockCount != 3 {
		t.Errorf("BlockCount = %d, want 3", meta.BlockCount)
	}
	if meta.Frame != 300 {
		t.Errorf("Frame = %d, want 300 (most recent)", meta.Frame)
	}
	if meta.Ended {
		t.Errorf("Ended should be false after only `continue` blocks")
	}
	if meta.Frozen {
		t.Errorf("Frozen should be false while the bar is live")
	}
	if meta.Percent <= 0 || meta.Percent >= 1 {
		t.Errorf("Percent = %f, want (0,1) while live", meta.Percent)
	}
}

func TestFFmpegEndSnapsTo100AndClosesActive(t *testing.T) {
	m := setupModel()

	m = feedLines(m, ffmpegBlock(100, "00:00:01", "continue")...)
	m = feedLines(m, ffmpegBlock(200, "00:00:02", "end")...)

	if got := m.store.Len(); got != 1 {
		t.Fatalf("expected 1 line, got %d", got)
	}
	l := m.store.Get(0)
	meta := l.Meta.(*line.FFmpegMeta)
	if !meta.Ended {
		t.Errorf("Ended should be true after progress=end")
	}
	if meta.Percent != 1.0 {
		t.Errorf("Percent = %f, want 1.0", meta.Percent)
	}
	if m.s.activeFFmpegIdx != -1 {
		t.Errorf("activeFFmpegIdx = %d, want -1 after end", m.s.activeFFmpegIdx)
	}

	// A subsequent block must start a fresh line, not revive the frozen one.
	m = feedLines(m, ffmpegBlock(300, "00:00:03", "continue")...)
	if got := m.store.Len(); got != 2 {
		t.Fatalf("expected 2 lines after new stream, got %d", got)
	}
}

func TestFFmpegAbortFreezesAndLetsNormalLogsThrough(t *testing.T) {
	m := setupModel()

	// Feed 2 partial blocks, then abandon the stream with an ordinary log.
	m = feedLines(m, ffmpegBlock(100, "00:00:01", "continue")...)
	m = feedLines(m, ffmpegBlock(200, "00:00:02", "continue")...)

	const marker = "RECOGNIZABLE_MARKER_LOG"
	m = feedLines(m, marker)

	if got := m.store.Len(); got != 2 {
		t.Fatalf("expected 2 lines (frozen bar + marker), got %d", got)
	}
	bar := m.store.Get(0)
	if bar.Type != line.TypeFFmpegProgress {
		t.Fatalf("first line type = %v, want TypeFFmpegProgress", bar.Type)
	}
	meta := bar.Meta.(*line.FFmpegMeta)
	if !meta.Frozen {
		t.Errorf("expected frozen bar after non-ffmpeg line")
	}
	if meta.Ended {
		t.Errorf("Ended should stay false when aborted")
	}
	if meta.Percent >= 1.0 {
		t.Errorf("Percent should stay < 1.0 when aborted, got %f", meta.Percent)
	}

	second := m.store.Get(1)
	if second.Raw != marker {
		t.Errorf("marker line mangled: got %q, want %q", second.Raw, marker)
	}

	if m.s.activeFFmpegIdx != -1 {
		t.Errorf("activeFFmpegIdx must be cleared after freeze; got %d", m.s.activeFFmpegIdx)
	}
}

func TestFFmpegToleratesPrefixedLines(t *testing.T) {
	// Whatever sits in front of `key=` (a bench [LLB:...] tag, a docker
	// logs timestamp, a `stern pod | ` prefix) must be ignored so the bar
	// still coalesces when ffmpeg output passes through a log pipeline.
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "LLB bench prefix",
			lines: []string{
				"[LLB:1777067614779878782] frame=100",
				"[LLB:1777067614784169590] fps=60",
				"[LLB:1777067614789579867] speed=1.0x",
				"[LLB:1777067614794962754] out_time=00:00:01.000000",
				"[LLB:1777067614799226852] progress=continue",
			},
		},
		{
			name: "docker-like timestamp",
			lines: []string{
				"2024-01-01T10:00:00.000Z frame=100",
				"2024-01-01T10:00:00.010Z progress=continue",
			},
		},
		{
			name: "stern-like pod tag",
			lines: []string{
				"encoder-pod | frame=100",
				"encoder-pod | progress=end",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := setupModel()
			m = feedLines(m, tc.lines...)
			if got := m.store.Len(); got != 1 {
				t.Fatalf("expected 1 coalesced progress line, got %d", got)
			}
			if m.store.Get(0).Type != line.TypeFFmpegProgress {
				t.Fatalf("prefix stripped but wrong line type: %v", m.store.Get(0).Type)
			}
		})
	}
}

func TestFFmpegRawContainsReadableSummary(t *testing.T) {
	m := setupModel()
	m = feedLines(m, ffmpegBlock(123, "00:00:05", "continue")...)

	raw := m.store.Get(0).Raw
	if !containsAll(raw, "[ffmpeg]", "frame=123", "1.0x") {
		t.Errorf("raw summary missing expected fragments: %q", raw)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !stringContains(s, sub) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
