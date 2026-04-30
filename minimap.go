package main

import (
	"strings"

	"loglens/line"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Per-line status tracked alongside the silhouette so the minimap can tint
// rows by outcome. Failure outranks success during row aggregation, so a
// single error in a busy row stays visible.
const (
	statusNeutral uint8 = 0
	statusSuccess uint8 = 1
	statusFailure uint8 = 2
)

// Minimap constants — tuned for VS-Code-like proportions in a terminal.
const (
	minimapContentWidth   = 20 // braille chars across
	minimapSeparatorWidth = 1  // " " gap between content and map
	minimapMinTermWidth   = 60 // don't show the map below this terminal width

	// minimapWindowLines caps how many of the most recent log lines the
	// minimap represents. Without this, the per-frame scan over every line
	// holds m.s.mu.RLock for tens of ms at 100k+ lines, starving the
	// ingestor of its write lock and crushing throughput. The cap makes
	// every frame O(1) in total log size. 4096 lines at typical vh≈80
	// yields ~13 log lines per braille sub-row — detailed enough to read
	// the silhouette of recent activity, which is what the minimap is for.
	minimapWindowLines = 4096
)

// lineExtent describes the horizontal span of non-whitespace content on a
// single log line. beg == -1 means the line is empty/whitespace-only or
// hidden, and should contribute no dots to the minimap. status carries the
// success/failure classification for row tinting.
type lineExtent struct {
	beg    int
	end    int
	status uint8
}

// classifyMinimapStatus inspects a parsed line and returns whether it
// represents a clear success, a clear failure, or neither.
//
// Success is reserved for explicit pass markers (test PASS, package "ok ...")
// — informational severities like INFO or k8s "Normal" events are routine
// activity, not outcomes, so they stay neutral rather than flooding the map
// with green.
//
// Failure covers anything the renderer already flags as a problem: error/
// warning prefixes, failed test results, e2e step failures, nginx error/warn
// brackets, klog W/E/F, and k8s "Warning" events.
func classifyMinimapStatus(l *line.LogLine) uint8 {
	if l == nil {
		return statusNeutral
	}
	switch l.Type {
	case line.TypeWarning:
		// detectWarning only emits TypeWarning for ERROR/FATAL/WARN/WARNING
		// prefixes — all failure-class.
		return statusFailure
	case line.TypeGoTestResult:
		if meta, ok := l.Meta.(*line.GoTestMeta); ok {
			if meta.IsFail {
				return statusFailure
			}
			if meta.IsPass {
				return statusSuccess
			}
		}
	}
	for _, seg := range l.Segments {
		switch seg.Style {
		case "level-error", "level-warn", "failed-step", "k8s-event-warning":
			return statusFailure
		}
	}
	return statusNeutral
}

// nonWSRange returns the [beg, end) range of non-whitespace characters in s.
// Returns (-1, 0) for empty/whitespace-only strings. Operates on bytes, which
// is fine for the ASCII-dominated log content this tool targets (matches the
// assumption already made by visualRowsForLine in model.go).
func nonWSRange(s string) (int, int) {
	beg := -1
	end := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != ' ' && c != '\t' {
			if beg < 0 {
				beg = i
			}
			end = i + 1
		}
	}
	return beg, end
}

// buildMinimapRows generates `height` braille-encoded rows of `width` chars.
// Each char encodes a 4×2 grid of input sub-rows/sub-cols, following the
// wfxr/code-minimap algorithm. Vertical and horizontal scales are capped at
// 1.0 so short/narrow content never zooms in.
//
// totalLines is the authoritative line count (extents may lag behind briefly
// if a caller pre-seeded the store without going through the ingestor); it
// anchors the vertical scale so the map stays aligned with the viewport
// indicator, which is computed against total line count.
//
// rowStatus[r] aggregates the success/failure classification of every line
// that landed in output row r, with failure outranking success so a single
// error stays visible even in a row dominated by passing lines.
func buildMinimapRows(extents []lineExtent, height, width, maxCol, totalLines int) (out []string, rowStatus []uint8) {
	out = make([]string, height)
	rowStatus = make([]uint8, height)
	if height <= 0 || width <= 0 || totalLines <= 0 {
		return
	}
	if maxCol <= 0 {
		maxCol = 1
	}

	subRows := height * 4
	vscale := float64(subRows) / float64(totalLines)
	if vscale > 1.0 {
		vscale = 1.0
	}
	subCols := width * 2
	hscale := float64(subCols) / float64(maxCol)
	if hscale > 1.0 {
		hscale = 1.0
	}

	frame := make([][4]rng, height)
	for r := range frame {
		for j := range frame[r] {
			frame[r][j] = rng{beg: -1, end: 0}
		}
	}

	for i, ext := range extents {
		if ext.beg < 0 {
			continue
		}
		scaledI := int(float64(i) * vscale)
		if scaledI >= subRows {
			scaledI = subRows - 1
		}
		outRow := scaledI / 4
		brow := scaledI % 4

		sBeg := int(float64(ext.beg) * hscale)
		sEnd := int(float64(ext.end) * hscale)
		if sEnd <= sBeg {
			sEnd = sBeg + 1
		}
		if sEnd > subCols {
			sEnd = subCols
		}

		cur := &frame[outRow][brow]
		if cur.beg < 0 {
			cur.beg, cur.end = sBeg, sEnd
		} else {
			if sBeg < cur.beg {
				cur.beg = sBeg
			}
			if sEnd > cur.end {
				cur.end = sEnd
			}
		}

		if ext.status > rowStatus[outRow] {
			rowStatus[outRow] = ext.status
		}
	}

	for r := 0; r < height; r++ {
		var sb strings.Builder
		sb.Grow(width * 3)
		for c := 0; c < width; c++ {
			left := dotBitsAt(&frame[r], c*2)
			right := dotBitsAt(&frame[r], c*2+1)
			sb.WriteRune(brailleMatrix[left|(right<<4)])
		}
		out[r] = sb.String()
	}
	return
}

// dotBitsAt returns a 4-bit mask of which of the 4 vertical braille sub-rows
// have content at horizontal sub-column `col`. Bit i is set iff frame[i]'s
// [beg, end) range contains col.
func dotBitsAt(f *[4]rng, col int) int {
	bits := 0
	for i := 0; i < 4; i++ {
		if f[i].beg >= 0 && col >= f[i].beg && col < f[i].end {
			bits |= 1 << i
		}
	}
	return bits
}

type rng struct{ beg, end int }

// overlayMinimap composes `viewRows` with `mapRows` on the right side of the
// terminal. Each rendered content row is truncated to `contentW = totalW -
// mapW - 1` with a faint separator column, then the corresponding minimap row
// is appended — styled by `rowStyles[cursor][status]` to mark both the
// viewport position (cursor row) and the success/failure outcome of the lines
// represented in that row.
func overlayMinimap(
	viewRows []string,
	mapRows []string,
	mapStatuses []uint8,
	totalW int,
	mapW int,
	viewStartRow, viewEndRow int,
	rowStyles [2][3]lipgloss.Style,
	sepStyle lipgloss.Style,
) []string {
	if len(mapRows) == 0 {
		return viewRows
	}
	contentW := totalW - mapW - minimapSeparatorWidth
	if contentW < 1 {
		contentW = 1
	}
	out := make([]string, len(viewRows))
	sep := sepStyle.Render("│")
	for i, row := range viewRows {
		clipped := ansi.Truncate(row, contentW, "")
		pad := contentW - ansi.StringWidth(clipped)
		if pad < 0 {
			pad = 0
		}
		var mapPart string
		if i < len(mapRows) {
			cursor := 0
			if i >= viewStartRow && i < viewEndRow {
				cursor = 1
			}
			status := uint8(0)
			if i < len(mapStatuses) {
				status = mapStatuses[i]
			}
			if status > statusFailure {
				status = statusNeutral
			}
			mapPart = rowStyles[cursor][status].Render(mapRows[i])
		} else {
			mapPart = strings.Repeat(" ", mapW)
		}
		out[i] = clipped + strings.Repeat(" ", pad) + sep + mapPart
	}
	return out
}

// brailleMatrix maps an 8-bit dot pattern to the corresponding Unicode braille
// glyph. Bits 0..3 are the left column (top→bottom); bits 4..7 are the right
// column. This is a direct port of BRAILLE_MATRIX from wfxr/code-minimap.
var brailleMatrix = [256]rune{
	'⠀', '⠁', '⠂', '⠃', '⠄', '⠅', '⠆', '⠇', '⡀', '⡁', '⡂', '⡃', '⡄', '⡅', '⡆', '⡇',
	'⠈', '⠉', '⠊', '⠋', '⠌', '⠍', '⠎', '⠏', '⡈', '⡉', '⡊', '⡋', '⡌', '⡍', '⡎', '⡏',
	'⠐', '⠑', '⠒', '⠓', '⠔', '⠕', '⠖', '⠗', '⡐', '⡑', '⡒', '⡓', '⡔', '⡕', '⡖', '⡗',
	'⠘', '⠙', '⠚', '⠛', '⠜', '⠝', '⠞', '⠟', '⡘', '⡙', '⡚', '⡛', '⡜', '⡝', '⡞', '⡟',
	'⠠', '⠡', '⠢', '⠣', '⠤', '⠥', '⠦', '⠧', '⡠', '⡡', '⡢', '⡣', '⡤', '⡥', '⡦', '⡧',
	'⠨', '⠩', '⠪', '⠫', '⠬', '⠭', '⠮', '⠯', '⡨', '⡩', '⡪', '⡫', '⡬', '⡭', '⡮', '⡯',
	'⠰', '⠱', '⠲', '⠳', '⠴', '⠵', '⠶', '⠷', '⡰', '⡱', '⡲', '⡳', '⡴', '⡵', '⡶', '⡷',
	'⠸', '⠹', '⠺', '⠻', '⠼', '⠽', '⠾', '⠿', '⡸', '⡹', '⡺', '⡻', '⡼', '⡽', '⡾', '⡿',
	'⢀', '⢁', '⢂', '⢃', '⢄', '⢅', '⢆', '⢇', '⣀', '⣁', '⣂', '⣃', '⣄', '⣅', '⣆', '⣇',
	'⢈', '⢉', '⢊', '⢋', '⢌', '⢍', '⢎', '⢏', '⣈', '⣉', '⣊', '⣋', '⣌', '⣍', '⣎', '⣏',
	'⢐', '⢑', '⢒', '⢓', '⢔', '⢕', '⢖', '⢗', '⣐', '⣑', '⣒', '⣓', '⣔', '⣕', '⣖', '⣗',
	'⢘', '⢙', '⢚', '⢛', '⢜', '⢝', '⢞', '⢟', '⣘', '⣙', '⣚', '⣛', '⣜', '⣝', '⣞', '⣟',
	'⢠', '⢡', '⢢', '⢣', '⢤', '⢥', '⢦', '⢧', '⣠', '⣡', '⣢', '⣣', '⣤', '⣥', '⣦', '⣧',
	'⢨', '⢩', '⢪', '⢫', '⢬', '⢭', '⢮', '⢯', '⣨', '⣩', '⣪', '⣫', '⣬', '⣭', '⣮', '⣯',
	'⢰', '⢱', '⢲', '⢳', '⢴', '⢵', '⢶', '⢷', '⣰', '⣱', '⣲', '⣳', '⣴', '⣵', '⣶', '⣷',
	'⢸', '⢹', '⢺', '⢻', '⢼', '⢽', '⢾', '⢿', '⣸', '⣹', '⣺', '⣻', '⣼', '⣽', '⣾', '⣿',
}
