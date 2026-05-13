package main

import (
	"fmt"
	"github.com/wufe/loglens/line"
	"strconv"
	"strings"
	"time"
)

// ffmpeg `-progress pipe:1` emits a block of key=value lines terminated by
// `progress=continue` (more coming) or `progress=end` (final block). Each key
// appears on its own line. We recognize these keys and coalesce the whole
// stream into a single TypeFFmpegProgress line whose bar fills up as more
// blocks arrive — instead of letting every block add a fresh log entry.

// ffmpegKnownKeys lists the fixed-name keys emitted by ffmpeg -progress.
// stream_* and dup/drop_frames vary per run but share a stable prefix; they're
// handled separately by isFFmpegKey.
var ffmpegKnownKeys = map[string]struct{}{
	"frame":       {},
	"fps":         {},
	"bitrate":     {},
	"total_size":  {},
	"out_time_us": {},
	"out_time_ms": {},
	"out_time":    {},
	"dup_frames":  {},
	"drop_frames": {},
	"speed":       {},
	"progress":    {},
}

// parseFFmpegKV recognizes a single ffmpeg progress line and returns its
// key/value. Tolerates any leading prefix that doesn't contain `=` — real
// pipelines often prepend timestamps or source tags (e.g. `docker logs`
// timestamps, loggen's `[LLB:<nanos>] `, k8s `stern`-style `pod | `), so we
// locate the key by walking backwards from the first `=` through identifier
// characters only. Returns ok=false for any line that doesn't match.
func parseFFmpegKV(raw string) (key, value string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	eq := strings.IndexByte(s, '=')
	if eq <= 0 || eq == len(s)-1 {
		return "", "", false
	}
	left := s[:eq]
	// Walk backwards through [A-Za-z0-9_] — whatever comes before that is
	// treated as a prefix and discarded.
	keyStart := len(left)
	for keyStart > 0 {
		c := left[keyStart-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			keyStart--
			continue
		}
		break
	}
	key = left[keyStart:]
	value = strings.TrimSpace(s[eq+1:])
	if key == "" || !isFFmpegKey(key) {
		return "", "", false
	}
	return key, value, true
}

func isFFmpegKey(k string) bool {
	if _, ok := ffmpegKnownKeys[k]; ok {
		return true
	}
	// stream_0_0_q, stream_0_1_q, etc.
	if strings.HasPrefix(k, "stream_") && strings.HasSuffix(k, "_q") {
		return true
	}
	return false
}

// applyFFmpegKV updates meta with a single key/value pair. Returns true when
// the key is `progress` — that is the block terminator and the point at which
// the line should re-render.
func applyFFmpegKV(meta *line.FFmpegMeta, key, value string) (blockEnd bool, ended bool) {
	switch key {
	case "frame":
		if n, err := strconv.Atoi(value); err == nil {
			meta.Frame = n
		}
	case "fps":
		meta.Fps = value
	case "bitrate":
		meta.Bitrate = value
	case "total_size":
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			meta.TotalSize = n
		}
	case "out_time_us":
		if us, err := strconv.ParseInt(value, 10, 64); err == nil {
			meta.OutTimeUs = us
		}
	case "out_time_ms":
		// ffmpeg emits both; only fall back when _us is absent.
		if meta.OutTimeUs == 0 {
			if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
				meta.OutTimeUs = ms * 1000
			}
		}
	case "out_time":
		meta.OutTime = value
	case "dup_frames":
		if n, err := strconv.Atoi(value); err == nil {
			meta.DupFrames = n
		}
	case "drop_frames":
		if n, err := strconv.Atoi(value); err == nil {
			meta.DropFrames = n
		}
	case "speed":
		meta.Speed = value
	case "progress":
		meta.BlockCount++
		updateFFmpegPercent(meta)
		if value == "end" {
			meta.Percent = 1.0
			meta.Ended = true
			return true, true
		}
		return true, false
	}
	return false, false
}

// updateFFmpegPercent assigns a pseudo-percentage based on the number of
// completed blocks. ffmpeg's progress stream carries no total duration, so we
// fill the bar on a linear ramp that caps at 99% — only `progress=end` is
// allowed to snap to 100%. The slope is calibrated so a typical transcode
// approaches full by the time it emits ~48 blocks, which matches the cadence
// of `-stats_period 1` on a minute-long clip.
func updateFFmpegPercent(meta *line.FFmpegMeta) {
	if meta.Ended {
		meta.Percent = 1.0
		return
	}
	p := 0.02 + 0.02*float64(meta.BlockCount)
	if p > 0.99 {
		p = 0.99
	}
	if p > meta.Percent {
		meta.Percent = p
	}
}

// renderFFmpegRaw produces the plain-text summary stored on the LogLine. It is
// what search, copy, and the minimap see; the TUI replaces it at render time
// with a styled progress bar (see render.renderFFmpegProgress).
func renderFFmpegRaw(meta *line.FFmpegMeta) string {
	var sb strings.Builder
	sb.WriteString("[ffmpeg]")
	if meta.OutTime != "" {
		sb.WriteString(" time=")
		sb.WriteString(meta.OutTime)
	} else if meta.OutTimeUs > 0 {
		sb.WriteString(" time=")
		sb.WriteString(formatFFmpegDuration(meta.OutTimeUs))
	}
	if meta.Frame > 0 {
		fmt.Fprintf(&sb, " frame=%d", meta.Frame)
	}
	if meta.Speed != "" {
		sb.WriteString(" speed=")
		sb.WriteString(meta.Speed)
	}
	if meta.Bitrate != "" {
		sb.WriteString(" bitrate=")
		sb.WriteString(strings.TrimSpace(meta.Bitrate))
	}
	if meta.TotalSize > 0 {
		sb.WriteString(" size=")
		sb.WriteString(formatFFmpegSize(meta.TotalSize))
	}
	fmt.Fprintf(&sb, " %.1f%%", meta.Percent*100)
	if meta.Ended {
		sb.WriteString(" done")
	} else if meta.Frozen {
		sb.WriteString(" aborted")
	}
	return sb.String()
}

func formatFFmpegDuration(us int64) string {
	d := time.Duration(us) * time.Microsecond
	return d.Round(time.Second).String()
}

func formatFFmpegSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
