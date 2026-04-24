package main

import (
	"loglens/input"
	"loglens/line"
	"loglens/parser"
	"loglens/store"
	"sync"
	"sync/atomic"
)

// sharedState holds all data that both the UI goroutine (Bubble Tea Update +
// View) and the background ingestor goroutine touch. Any access to these
// fields must hold mu.
//
// The design splits loglens into two independent worlds:
//
//	ingestor goroutine  ──► writes (parser, store, Fenwick)
//	UI goroutine        ──► reads (View) + cursor/offset mutations
//
// Keeping ingestion off the Bubble Tea event loop is what makes the producer
// non-blocking: render-time spikes no longer stall pipe reads, because the
// ingestor drains input.Lines() independently of how long View or a key
// handler takes.
//
// Atomic counters (totalLen, eof, exitCode) let the UI peek at coarse state
// without taking the lock — useful for follow-mode and the status bar, which
// tick at 30 Hz and don't need perfect consistency.
type sharedState struct {
	mu sync.RWMutex

	store      *store.LineStore
	parser     *parser.Parser
	visRows    []int
	visRowsBIT *fenwick

	// Width/wrapMode are set by the UI (WindowSizeMsg / 'w' toggle) and read
	// by the ingestor to size each new line's visRows entry. Ingestor always
	// treats appended lines as non-cursor; the UI fixes up the cursor line
	// specifically inside its tick handler.
	width    int
	wrapMode bool

	// Minimap per-line silhouette cache, maintained incrementally at
	// append/reparse time so View never needs to call store.Get (which
	// triggers disk reloads on offloaded chunks) or re-scan raw strings.
	// minimapMaxCol is a running maximum of extent.end — monotonic by
	// design; a slight over-estimate after a reparse that would shrink it
	// just compresses the horizontal scale a hair, which is invisible.
	minimapExtents []lineExtent
	minimapMaxCol  int

	totalLen atomic.Int64
	eof      atomic.Bool
	exitCode atomic.Int32
}

func newSharedState(ls *store.LineStore) *sharedState {
	s := &sharedState{
		store:      ls,
		parser:     parser.New(),
		visRowsBIT: newFenwick(1024),
	}
	s.exitCode.Store(-1)
	return s
}

// appendVisRowsLocked records the visual row count for a newly appended line.
// Must be called with s.mu held (write).
func (s *sharedState) appendVisRowsLocked(idx, rows int) {
	if idx < len(s.visRows) {
		s.applyVisRowsDeltaLocked(idx, rows)
		return
	}
	for len(s.visRows) < idx {
		s.visRows = append(s.visRows, 0)
	}
	s.visRows = append(s.visRows, rows)
	if s.visRowsBIT == nil || idx >= s.visRowsBIT.n {
		s.visRowsBIT = buildFenwick(s.visRows)
		return
	}
	s.visRowsBIT.update(idx, rows)
}

// recomputeVisRowsLocked re-measures line idx using the current UI hints and
// updates the BIT with the delta. Must be called with s.mu held (write).
// isCursor controls whether the wrapped-rows path is used; cursorPath is the
// UI's current tree path (nil when cursor is not on this line or on parent).
func (s *sharedState) recomputeVisRowsLocked(idx int, isCursor bool, cursorPath []int) {
	if idx < 0 || idx >= s.store.Len() {
		return
	}
	if idx >= len(s.visRows) {
		for len(s.visRows) < idx {
			s.visRows = append(s.visRows, 0)
		}
		s.visRows = append(s.visRows, 0)
		if s.visRowsBIT == nil || idx >= s.visRowsBIT.n {
			s.visRowsBIT = buildFenwick(s.visRows)
		}
	}
	newRows := visualRowsForLineStatic(s.store, idx, s.width, s.wrapMode, isCursor, cursorPath)
	s.applyVisRowsDeltaLocked(idx, newRows)
}

func (s *sharedState) applyVisRowsDeltaLocked(idx, newRows int) {
	delta := newRows - s.visRows[idx]
	if delta == 0 {
		return
	}
	s.visRows[idx] = newRows
	s.visRowsBIT.update(idx, delta)
}

// rebuildVisRowsLocked recomputes visRows + BIT from scratch. Called on events
// that invalidate every line's wrap calculation (width change, wrap toggle).
// Cursor row is computed with isCursor=true; caller provides cursor index.
func (s *sharedState) rebuildVisRowsLocked(cursor int) {
	n := s.store.Len()
	s.visRows = make([]int, n)
	for i := range n {
		s.visRows[i] = visualRowsForLineStatic(s.store, i, s.width, s.wrapMode, i == cursor, nil)
	}
	s.visRowsBIT = buildFenwick(s.visRows)
}

// ingestor drains input.Lines() in its own goroutine, parsing each line and
// appending to the store. It owns the parser entirely (no other goroutine
// touches it) and holds s.mu only for the narrow window of state mutations —
// append + Fenwick update + multi-line group fixup + UpdateStub.
type ingestor struct {
	state    *sharedState
	inputSrc input.InputSource
	bench    *benchLogger

	stopCh chan struct{}
	doneCh chan struct{}
}

func newIngestor(s *sharedState, src input.InputSource, bench *benchLogger) *ingestor {
	return &ingestor{
		state:    s,
		inputSrc: src,
		bench:    bench,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (ig *ingestor) start() {
	go ig.run()
}

func (ig *ingestor) stop() {
	select {
	case <-ig.stopCh:
	default:
		close(ig.stopCh)
	}
	<-ig.doneCh
}

// ingestBatchCap caps how many lines the ingestor drains from the input
// channel per single lock acquisition. Batching amortizes lock acquire/release
// overhead, atomic totalLen writes, and the CPU cost of acquiring the RWMutex
// write lock against UI read-lock contention. A batch of 32 bounds worst-case
// UI stall to ~30µs × 32 ≈ 1 ms — well below the 33 ms tick interval so UI
// frames never drop, while slashing lock-acquisition count ~30× at saturation.
const ingestBatchCap = 32

// releaseInterval is how often (in lines) the ingestor calls
// parser.ReleaseOldLines to drop references to offloaded lines. Running this
// on the ingestor (instead of the UI tick) keeps parser state entirely
// ingestor-owned, so the UI tick stops paying for parser maintenance under
// the write lock.
const releaseInterval = 500

func (ig *ingestor) run() {
	defer close(ig.doneCh)
	lines := ig.inputSrc.Lines()
	batch := make([]input.RawLine, 0, ingestBatchCap)
	linesSinceRelease := 0
	for {
		// Block until at least one line is available (or stop/EOF).
		select {
		case <-ig.stopCh:
			return
		case raw, ok := <-lines:
			if !ok {
				ig.state.eof.Store(true)
				ig.state.exitCode.Store(int32(ig.inputSrc.ExitCode()))
				if ig.bench != nil {
					ig.bench.eofReached(int(ig.state.totalLen.Load()))
				}
				return
			}
			batch = append(batch, ig.prepBench(raw))
		}

		// Opportunistically drain up to ingestBatchCap-1 more lines without
		// blocking, so a saturated pipe fills a full batch in one lock cycle.
	drain:
		for len(batch) < ingestBatchCap {
			select {
			case raw, ok := <-lines:
				if !ok {
					// Channel closed mid-batch: flush what we have, then let
					// the next outer iteration observe the close and emit EOF.
					break drain
				}
				batch = append(batch, ig.prepBench(raw))
			default:
				break drain
			}
		}

		ig.ingestBatch(batch)
		linesSinceRelease += len(batch)
		batch = batch[:0]

		if linesSinceRelease >= releaseInterval {
			ig.state.parser.ReleaseOldLines(releaseInterval)
			linesSinceRelease = 0
		}
	}
}

// prepBench strips the bench lag prefix (if present) and records the arrival
// bucket. Runs outside the shared lock because benchLogger is independently
// synchronized.
func (ig *ingestor) prepBench(raw input.RawLine) input.RawLine {
	if ig.bench == nil {
		return raw
	}
	if stripped, lagNs, ok := ig.bench.parsePrefix(raw.Text); ok {
		raw.Text = stripped
		ig.bench.lineReceivedWithLag(lagNs)
	} else {
		ig.bench.lineReceived()
	}
	return raw
}

// ingestBatch processes a slice of already-bench-accounted lines under a
// single write lock acquisition.
func (ig *ingestor) ingestBatch(batch []input.RawLine) {
	if len(batch) == 0 {
		return
	}
	s := ig.state
	s.mu.Lock()
	for _, raw := range batch {
		s.ingestOneLocked(raw)
	}
	s.mu.Unlock()
}

// ingestOneLocked performs the core parse + append + visRows update under
// the caller-held write lock. Shared by the ingestor goroutine and the
// synchronous LineMsg path used by tests.
func (s *sharedState) ingestOneLocked(raw input.RawLine) {
	fromStderr := raw.Source == input.SourceStderr

	result := s.parser.Parse(raw.Text, fromStderr)
	l := s.parser.LastLine()
	s.store.Append(l)
	newIdx := s.store.Len() - 1

	// New line is appended as non-cursor (UI decides follow-mode cursor on
	// the next tick). Cursor line row count is corrected later by the UI.
	rows := visualRowsForLineStatic(s.store, newIdx, s.width, s.wrapMode, false, nil)
	s.appendVisRowsLocked(newIdx, rows)

	// Cache the minimap silhouette while l.Raw is cheaply in hand.
	beg, end := nonWSRange(l.Raw)
	s.minimapExtents = append(s.minimapExtents, lineExtent{beg: beg, end: end})
	if end > s.minimapMaxCol {
		s.minimapMaxCol = end
	}

	// Multi-line JSON coalescing mutated earlier lines (GroupID/GroupHead/
	// Type/Expanded). Their visRows entries must be refreshed so scrolling
	// doesn't visually jitter.
	for _, idx := range result.ReparseIndices {
		if idx < 0 || idx >= s.store.Len() {
			continue
		}
		el := s.store.Get(idx)
		if el.GroupHead && el.GroupID != 0 && el.Expanded && el.Children == nil {
			expandAndPopulate(el)
			expandAllDescendants(el)
		}
		s.store.UpdateStub(idx)
		s.recomputeVisRowsLocked(idx, false, nil)

		// If coalescing turned this line into a hidden non-head group
		// member, drop its silhouette so the minimap stops showing it.
		if idx < len(s.minimapExtents) && s.store.IsHiddenGroupMember(idx) {
			s.minimapExtents[idx] = lineExtent{beg: -1}
		}
	}

	s.totalLen.Store(int64(s.store.Len()))
}

// visualRowsForLineStatic is the lock-free equivalent of model.visualRowsForLine.
// It takes the store, width, wrapMode and a cursor flag explicitly so it can
// be called from both the ingestor (non-cursor) and the UI handlers.
//
// The arithmetic matches visualRowsForLine exactly; keep them in sync.
func visualRowsForLineStatic(st *store.LineStore, i, width int, wrapMode, isCursor bool, cursorPath []int) int {
	if i < 0 || i >= st.Len() {
		return 1
	}
	l := st.Get(i)

	if st.IsHiddenGroupMember(i) {
		return 0
	}

	if l.Expanded && l.Children != nil {
		if wrapMode || isCursor {
			var cp []int
			if isCursor {
				if len(cursorPath) > 0 {
					cp = cursorPath
				} else {
					cp = []int{-1}
				}
			}
			total, _ := expandedTreeRowsStatic(l, width, wrapMode, cp)
			return total
		}
		return totalVisibleRows(l)
	}

	// Parent line: cursor or global wrap both cause wrapping.
	if !isCursor && !wrapMode {
		return 1
	}

	if width > 3 {
		if l.Type == line.TypeJSON && !l.Expanded && len(l.Segments) == 0 {
			return 1
		}
		totalVis := 3 + len(l.Raw)
		if totalVis <= width {
			return 1
		}
		return (totalVis + width - 1) / width
	}

	// Width is 0 or absurdly small (e.g. ingestor running before the first
	// WindowSizeMsg). Assume a single row — the UI rebuilds every row on the
	// window-size event, so provisional counts only need to be non-panicking.
	// Calling render.RenderLineWrapped here would require a non-nil *Styles,
	// which the ingestor doesn't have.
	return 1
}

// expandedTreeRowsStatic mirrors model.expandedTreeRows without depending on
// model fields; used by both the ingestor and UI.
func expandedTreeRowsStatic(l *line.LogLine, width int, wrapMode bool, cursorPath []int) (total, cursorRow int) {
	cursorRow = -1
	cursorOnParent := len(cursorPath) == 1 && cursorPath[0] == -1
	if cursorOnParent || wrapMode {
		total = rowsForWidth(3+len(l.Raw), width)
	} else {
		total = 1
	}
	if cursorOnParent {
		cursorRow = 0
	}
	if !l.Expanded || l.Children == nil {
		return total, cursorRow
	}
	walkExpandedChildrenStatic(l, cursorPath, width, wrapMode, &total, &cursorRow)
	return total, cursorRow
}

func walkExpandedChildrenStatic(parent *line.LogLine, path []int, width int, wrapMode bool, total, cursorRow *int) {
	for i, child := range parent.Children {
		onPath := len(path) > 0 && path[0] == i
		isCursor := onPath && len(path) == 1
		if isCursor {
			*cursorRow = *total
		}
		prefixW := 2 + 2*child.Depth
		if child.Expandable {
			prefixW = 3 + 2*child.Depth
		}
		contentW := prefixW + estimatedContentWidth(child)
		if isCursor || wrapMode {
			*total += rowsForWidth(contentW, width)
		} else {
			*total++
		}
		if child.Expanded && child.Children != nil {
			var sub []int
			if onPath && len(path) > 1 {
				sub = path[1:]
			}
			walkExpandedChildrenStatic(child, sub, width, wrapMode, total, cursorRow)
		}
	}
}
