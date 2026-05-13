package store

import (
	"fmt"
	"github.com/wufe/loglens/line"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ChunkSize is the number of lines per chunk.
const ChunkSize = 8192

// OffloadThreshold is the minimum total line count before offloading begins.
const OffloadThreshold = 50000

// DefaultDiskCap is the maximum disk usage for offloaded chunks (1 GB).
const DefaultDiskCap int64 = 1 << 30

// ChunkState tracks whether a chunk is in memory, on disk, or evicted.
type ChunkState int

const (
	ChunkHot        ChunkState = iota // in memory
	ChunkOffloading                   // queued for async disk write (still in memory)
	ChunkCold                         // serialized to disk, loadable
	ChunkEvicted                      // disk file deleted, data lost
)

// chunk holds a contiguous group of lines.
type chunk struct {
	state    ChunkState
	lines    []*line.LogLine // nil when Cold or Evicted
	diskPath string          // temp file path (empty when Hot)
	diskSize int64           // bytes on disk
}

// evictedLine is returned when accessing an evicted chunk.
var evictedLine = &line.LogLine{
	Raw:  "[line evicted from disk cache]",
	Type: line.TypePlain,
}

// LineStore manages log line storage with lightweight stubs always in memory.
// Lines are organized into fixed-size chunks. Each chunk can be independently
// offloaded to disk and reloaded on demand.
type LineStore struct {
	stubs    []line.LineStub // always in memory, indexed by line index
	chunks   []*chunk        // indexed by chunk ID (lineIdx / ChunkSize)
	totalLen int             // total number of lines

	// Disk offloading
	tmpDir   string // temp directory for chunk files (created on first offload)
	diskUsed int64  // total bytes on disk
	diskCap  int64  // max disk usage

	// Async offload: serialize + WriteFile happens on a background goroutine
	// so the caller (UI tick under the shared write lock) isn't blocked on
	// disk I/O, which can take tens of ms per chunk and starves the ingestor.
	offloadCh     chan offloadJob    // queued jobs for the worker
	offloadDoneCh chan offloadResult // completed jobs, drained by ApplyOffloadResults
	offloadWG     sync.WaitGroup     // tracks in-flight jobs (for test draining)

	// Pre-fetching
	prefetchCh chan int // chunk IDs to load in background
	closeCh    chan struct{}
	closeOnce  sync.Once
}

// offloadJob carries the work needed to serialize + write a chunk to disk.
// The worker reads from c.lines via the captured reference (non-tail chunks
// are immutable after Append stops writing to them, so no lock needed).
type offloadJob struct {
	ci    int
	c     *chunk
	lines []*line.LogLine
}

// offloadResult is posted back after a chunk has been written to disk.
// ApplyOffloadResults applies it under the caller's shared lock, flipping
// c.state to Cold and updating diskUsed.
type offloadResult struct {
	ci       int
	c        *chunk
	diskPath string
	diskSize int64
	err      error
}

// offloadQueueCap bounds the in-flight offload pipeline. Small because each
// job represents 8192 lines ~= a few MB after compression; buffering too
// many keeps old chunk references alive (foiling GC).
const offloadQueueCap = 8

// New creates an empty LineStore.
func New() *LineStore {
	s := newLineStore(DefaultDiskCap)
	return s
}

// NewWithDiskCap creates an empty LineStore with a custom disk cap.
func NewWithDiskCap(cap int64) *LineStore {
	s := newLineStore(cap)
	return s
}

// newLineStore is the shared constructor used by New/NewWithDiskCap/NewFromSlice.
// It wires up the prefetch + async-offload background goroutines.
func newLineStore(diskCap int64) *LineStore {
	s := &LineStore{
		diskCap:       diskCap,
		offloadCh:     make(chan offloadJob, offloadQueueCap),
		offloadDoneCh: make(chan offloadResult, offloadQueueCap),
		prefetchCh:    make(chan int, 2),
		closeCh:       make(chan struct{}),
	}
	go s.prefetchLoop()
	go s.offloadLoop()
	return s
}

// NewFromSlice creates a LineStore pre-populated from an existing slice.
// Used for benchmarks and tests that construct models directly.
func NewFromSlice(lines []*line.LogLine) *LineStore {
	s := newLineStore(DefaultDiskCap)
	s.stubs = make([]line.LineStub, len(lines))
	s.totalLen = len(lines)
	for i, l := range lines {
		s.stubs[i] = line.MakeStub(l)
	}
	// Partition into chunks
	for start := 0; start < len(lines); start += ChunkSize {
		end := start + ChunkSize
		if end > len(lines) {
			end = len(lines)
		}
		c := &chunk{
			state: ChunkHot,
			lines: lines[start:end:end],
		}
		s.chunks = append(s.chunks, c)
	}
	return s
}

// Len returns the total number of lines.
func (s *LineStore) Len() int {
	return s.totalLen
}

// Get returns the LogLine at index i. If the chunk is cold, it loads it from
// disk first. If evicted, returns a placeholder.
func (s *LineStore) Get(i int) *line.LogLine {
	ci := i / ChunkSize
	c := s.chunks[ci]
	switch c.state {
	case ChunkHot, ChunkOffloading:
		// Offloading chunks still have c.lines populated — the worker reads
		// but doesn't clear them until the completion is applied.
		return c.lines[i%ChunkSize]
	case ChunkCold:
		if err := s.reloadChunk(ci); err != nil {
			return evictedLine
		}
		return s.chunks[ci].lines[i%ChunkSize]
	default: // ChunkEvicted
		return evictedLine
	}
}

// Stub returns the lightweight stub for line i.
func (s *LineStore) Stub(i int) line.LineStub {
	return s.stubs[i]
}

// Append adds a new line to the store.
func (s *LineStore) Append(l *line.LogLine) {
	s.stubs = append(s.stubs, line.MakeStub(l))

	ci := s.totalLen / ChunkSize
	if ci >= len(s.chunks) {
		s.chunks = append(s.chunks, &chunk{
			state: ChunkHot,
			lines: make([]*line.LogLine, 0, ChunkSize),
		})
	}
	s.chunks[ci].lines = append(s.chunks[ci].lines, l)
	s.totalLen++
}

// UpdateStub re-syncs the stub for line i from the actual LogLine data.
// Call after in-place mutations (e.g., parser multiline JSON detection,
// expand/collapse).
func (s *LineStore) UpdateStub(i int) {
	if i >= 0 && i < s.totalLen {
		ci := i / ChunkSize
		c := s.chunks[ci]
		if c.state == ChunkHot || c.state == ChunkOffloading {
			s.stubs[i] = line.MakeStub(c.lines[i%ChunkSize])
		}
	}
}

// IsHiddenGroupMember returns true if line i is a non-head member of a
// multiline JSON group. Uses only stub data (no I/O).
func (s *LineStore) IsHiddenGroupMember(i int) bool {
	if i < 0 || i >= len(s.stubs) {
		return false
	}
	st := s.stubs[i]
	return !st.GroupHead && st.GroupID != 0 && st.Type == line.TypeJSON
}

// ChunkCount returns the number of chunks.
func (s *LineStore) ChunkCount() int {
	return len(s.chunks)
}

// ChunkStateAt returns the state of chunk ci.
func (s *LineStore) ChunkStateAt(ci int) ChunkState {
	if ci < 0 || ci >= len(s.chunks) {
		return ChunkHot
	}
	return s.chunks[ci].state
}

// DiskUsed returns the current disk usage in bytes.
func (s *LineStore) DiskUsed() int64 {
	return s.diskUsed
}

// RunOffloadCycle evaluates which chunks should be offloaded or reloaded
// based on the current cursor position and viewport. Only runs when the
// total line count exceeds OffloadThreshold.
//
// Offloads are queued asynchronously — the actual serialize + WriteFile
// happens on the background worker goroutine (see offloadLoop), so the
// caller isn't blocked on disk I/O. Completions are applied via
// ApplyOffloadResults on the next cycle.
func (s *LineStore) RunOffloadCycle(cursor, offset, viewportH int) {
	// Apply any pending completions from the worker before deciding new work,
	// so diskUsed and chunk states reflect the latest committed state.
	s.ApplyOffloadResults()

	if s.totalLen < OffloadThreshold {
		return
	}
	nChunks := len(s.chunks)
	if nChunks <= 1 {
		return
	}

	hot := s.computeHotChunks(cursor, offset, viewportH)

	// Queue hot→cold offloads for chunks no longer hot (skip the last chunk — may be incomplete)
	for ci := 0; ci < nChunks-1; ci++ {
		c := s.chunks[ci]
		if c.state == ChunkHot && !hot[ci] {
			s.queueOffload(ci)
		}
	}

	// Reload cold→hot for newly hot chunks
	for ci := range hot {
		if ci < nChunks && s.chunks[ci].state == ChunkCold {
			s.reloadChunk(ci)
		}
	}

	// Evict: if disk > cap, evict coldest chunks (lowest chunk ID first).
	// Offloading chunks are skipped — their disk file doesn't exist yet.
	for s.diskUsed > s.diskCap {
		evicted := false
		for ci := 0; ci < nChunks; ci++ {
			if s.chunks[ci].state == ChunkCold && !hot[ci] {
				s.evictChunk(ci)
				evicted = true
				if s.diskUsed <= s.diskCap {
					break
				}
			}
		}
		if !evicted {
			break // nothing left to evict
		}
	}
}

// queueOffload marks a chunk as offloading and hands it to the worker.
// Non-blocking: if the queue is full, the chunk stays Hot and will be
// retried on the next cycle.
func (s *LineStore) queueOffload(ci int) {
	c := s.chunks[ci]
	if c.state != ChunkHot || c.lines == nil {
		return
	}
	job := offloadJob{ci: ci, c: c, lines: c.lines}
	s.offloadWG.Add(1)
	select {
	case s.offloadCh <- job:
		// Mark Offloading only after successfully queueing. c.lines is
		// retained — Get() still returns from memory during this window
		// (see Get's Hot+Offloading case). The worker will set c.lines=nil
		// via ApplyOffloadResults once the disk write succeeds.
		c.state = ChunkOffloading
	default:
		// Queue full — retry on next cycle.
		s.offloadWG.Done()
	}
}

// ApplyOffloadResults drains completion messages from the worker and flips
// their chunks from Offloading to Cold (or back to Hot on write error).
// Caller must hold the store's external synchronization (e.g. s.mu in
// sharedState) because this mutates c.state, c.lines, c.diskPath, diskUsed —
// fields read by other goroutines under the same lock.
func (s *LineStore) ApplyOffloadResults() {
	for {
		select {
		case res := <-s.offloadDoneCh:
			c := res.c
			if res.err != nil {
				// Write failed — put the chunk back in memory.
				c.state = ChunkHot
				continue
			}
			c.diskPath = res.diskPath
			c.diskSize = res.diskSize
			c.lines = nil
			c.state = ChunkCold
			s.diskUsed += res.diskSize
		default:
			return
		}
	}
}

// offloadLoop is the worker goroutine: it serializes + writes chunks without
// holding any external lock. The chunk's lines slice is captured by the
// offloadJob and is immutable for non-tail chunks, so no synchronization is
// needed during serialization.
func (s *LineStore) offloadLoop() {
	for {
		select {
		case job := <-s.offloadCh:
			s.doOffload(job)
			s.offloadWG.Done()
		case <-s.closeCh:
			return
		}
	}
}

// WaitOffloadsForTest blocks until all queued offloads have been written by
// the worker, then applies their results. For use in tests that need
// deterministic "chunk is Cold now" behavior after RunOffloadCycle.
func (s *LineStore) WaitOffloadsForTest() {
	s.offloadWG.Wait()
	s.ApplyOffloadResults()
}

func (s *LineStore) doOffload(job offloadJob) {
	if err := s.ensureTmpDir(); err != nil {
		s.postOffloadResult(offloadResult{ci: job.ci, c: job.c, err: err})
		return
	}
	data, err := serializeChunk(job.lines)
	if err != nil {
		s.postOffloadResult(offloadResult{ci: job.ci, c: job.c, err: err})
		return
	}
	path := filepath.Join(s.tmpDir, fmt.Sprintf("chunk_%d.gob.flate", job.ci))
	if err := os.WriteFile(path, data, 0600); err != nil {
		s.postOffloadResult(offloadResult{ci: job.ci, c: job.c, err: err})
		return
	}
	s.postOffloadResult(offloadResult{
		ci:       job.ci,
		c:        job.c,
		diskPath: path,
		diskSize: int64(len(data)),
	})
}

func (s *LineStore) postOffloadResult(res offloadResult) {
	select {
	case s.offloadDoneCh <- res:
	case <-s.closeCh:
	}
}

// computeHotChunks returns the set of chunk IDs that should be kept in memory.
func (s *LineStore) computeHotChunks(cursor, offset, viewportH int) map[int]bool {
	hot := make(map[int]bool)
	nChunks := len(s.chunks)
	if nChunks == 0 {
		return hot
	}
	last := nChunks - 1

	// 1. First and last chunks always hot
	hot[0] = true
	hot[last] = true

	// 2. Cursor ± 2 chunks
	cursorChunk := cursor / ChunkSize
	for ci := max(0, cursorChunk-2); ci <= min(last, cursorChunk+2); ci++ {
		hot[ci] = true
	}

	// 3. Viewport: offset through enough chunks to cover viewport
	startChunk := offset / ChunkSize
	endLine := offset + viewportH + ChunkSize // buffer
	if endLine > s.totalLen {
		endLine = s.totalLen
	}
	endChunk := endLine / ChunkSize
	if endChunk > last {
		endChunk = last
	}
	for ci := startChunk; ci <= endChunk; ci++ {
		hot[ci] = true
	}

	// 4. Any chunk containing an expanded line (check stubs)
	for i, st := range s.stubs {
		if st.Expanded && st.HasChildren {
			hot[i/ChunkSize] = true
		}
	}

	return hot
}

// offloadChunk serializes a chunk to disk and frees its in-memory lines.
func (s *LineStore) offloadChunk(ci int) error {
	c := s.chunks[ci]
	if c.state != ChunkHot || c.lines == nil {
		return nil
	}

	if err := s.ensureTmpDir(); err != nil {
		return err
	}

	data, err := serializeChunk(c.lines)
	if err != nil {
		return err
	}

	path := filepath.Join(s.tmpDir, fmt.Sprintf("chunk_%d.gob.flate", ci))
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	c.diskPath = path
	c.diskSize = int64(len(data))
	c.lines = nil
	c.state = ChunkCold
	s.diskUsed += c.diskSize
	return nil
}

// reloadChunk loads a chunk from disk back into memory.
func (s *LineStore) reloadChunk(ci int) error {
	c := s.chunks[ci]
	if c.state != ChunkCold {
		return nil
	}

	data, err := os.ReadFile(c.diskPath)
	if err != nil {
		// File missing — treat as evicted
		c.state = ChunkEvicted
		s.diskUsed -= c.diskSize
		c.diskSize = 0
		return err
	}

	lines, err := deserializeChunk(data)
	if err != nil {
		c.state = ChunkEvicted
		s.diskUsed -= c.diskSize
		c.diskSize = 0
		return err
	}

	c.lines = lines
	c.state = ChunkHot
	// Keep the disk file for now (can be re-offloaded later)
	return nil
}

// evictChunk deletes a cold chunk's disk file and marks it evicted.
func (s *LineStore) evictChunk(ci int) {
	c := s.chunks[ci]
	if c.state != ChunkCold {
		return
	}
	os.Remove(c.diskPath)
	s.diskUsed -= c.diskSize
	c.diskPath = ""
	c.diskSize = 0
	c.state = ChunkEvicted
}

func (s *LineStore) ensureTmpDir() error {
	if s.tmpDir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "loglens-offload-*")
	if err != nil {
		return err
	}
	s.tmpDir = dir
	return nil
}

// Search finds the next line containing query (case-insensitive) starting
// from startIdx, wrapping around. For hot chunks, searches in memory. For
// cold chunks, loads them from disk temporarily. Evicted chunks are skipped.
func (s *LineStore) Search(query string, startIdx int) int {
	q := strings.ToLower(query)
	n := s.totalLen
	// Forward from startIdx
	for i := startIdx; i < n; i++ {
		if s.matchLine(i, q) {
			return i
		}
	}
	// Wrap around
	for i := 0; i < startIdx && i < n; i++ {
		if s.matchLine(i, q) {
			return i
		}
	}
	return -1
}

// SearchReverse finds the previous line containing query starting from startIdx.
func (s *LineStore) SearchReverse(query string, startIdx int) int {
	q := strings.ToLower(query)
	for i := startIdx; i >= 0; i-- {
		if s.matchLine(i, q) {
			return i
		}
	}
	// Wrap around
	for i := s.totalLen - 1; i > startIdx; i-- {
		if s.matchLine(i, q) {
			return i
		}
	}
	return -1
}

// matchLine checks if line i contains the (already-lowered) query.
func (s *LineStore) matchLine(i int, lowerQuery string) bool {
	l := s.Get(i) // transparently loads cold chunks
	if l == evictedLine {
		return false
	}
	return strings.Contains(strings.ToLower(l.Raw), lowerQuery)
}

// RequestPrefetch asks the background goroutine to load a chunk. Non-blocking:
// if the prefetch channel is full, the request is dropped.
func (s *LineStore) RequestPrefetch(ci int) {
	if ci < 0 || ci >= len(s.chunks) {
		return
	}
	if s.chunks[ci].state != ChunkCold {
		return
	}
	select {
	case s.prefetchCh <- ci:
	default:
	}
}

// prefetchLoop runs in a background goroutine, loading cold chunks on demand.
func (s *LineStore) prefetchLoop() {
	for {
		select {
		case ci := <-s.prefetchCh:
			if ci >= 0 && ci < len(s.chunks) && s.chunks[ci].state == ChunkCold {
				s.reloadChunk(ci)
			}
		case <-s.closeCh:
			return
		}
	}
}

// PrefetchAdjacent pre-loads chunks adjacent to the cursor zone. Call after
// cursor movement to reduce latency when scrolling into cold regions.
func (s *LineStore) PrefetchAdjacent(cursor int) {
	if s.totalLen < OffloadThreshold {
		return
	}
	cursorChunk := cursor / ChunkSize
	// Pre-fetch chunks just beyond the hot zone (±3 from cursor)
	s.RequestPrefetch(cursorChunk - 3)
	s.RequestPrefetch(cursorChunk + 3)
}

// Close releases resources: stops the prefetch goroutine and removes temp files.
// Safe to call multiple times.
func (s *LineStore) Close() {
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	if s.tmpDir != "" {
		os.RemoveAll(s.tmpDir)
		s.tmpDir = ""
	}
}
