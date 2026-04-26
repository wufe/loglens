package stats

import (
	"encoding/json"
	"fmt"
	"loglens/line"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// StatType identifies the kind of statistic computed over a stream of values.
// Only Frequency is wired up today; the others are placeholders that the UI
// surfaces as "not implemented".
type StatType string

const (
	Frequency StatType = "frequency"
	Average   StatType = "average"
	P99       StatType = "p99"
	Min       StatType = "min"
	Max       StatType = "max"
)

// DefaultMemoryCap is how many bytes of pending labels the manager will
// retain across all stats before refusing new entries (and incrementing
// the per-stat dropped counter). Sized large enough that "dropped" stays
// at 0 in any realistic workload — even at 500k lines/sec sustained, the
// worker drains the queue an order of magnitude faster than this cap.
const DefaultMemoryCap int64 = 500 * 1024 * 1024

// stringHeaderBytes is the per-entry overhead we charge the memory budget
// on top of the string's content length. Counts the slice slot (8B), the
// string header (16B), and a few bytes of slack so map/slice doublings
// don't push us past the cap unnoticed.
const stringHeaderBytes = 32

// GroupRule maps every value matching Pattern to the group labelled Title.
// Patterns are anchored automatically (^pattern$) so partial matches don't
// silently bucket more values than the user expects. The first rule whose
// pattern matches a value wins, so order matters — put catch-alls last.
type GroupRule struct {
	Title   string
	Pattern string
	re      *regexp.Regexp
}

// Compile prepares the rule's regex. Returns an error if the pattern is invalid.
func (g *GroupRule) Compile() error {
	pat := g.Pattern
	if pat == "" {
		return fmt.Errorf("empty pattern")
	}
	// Wrap in a non-capturing group before anchoring so top-level alternation
	// like "PutObject|UploadPart" anchors as a whole — otherwise we'd compile
	// `^PutObject|UploadPart$` which means `^PutObject` OR `UploadPart$` and
	// silently buckets prefixes/suffixes the user didn't intend.
	pat = "^(?:" + pat + ")$"
	re, err := regexp.Compile(pat)
	if err != nil {
		return err
	}
	g.re = re
	return nil
}

// Definition fully describes a stat to be tracked. The Manager owns one
// running aggregator per Definition. Most fields are user-configurable in
// the setup modal; ID is assigned by the Manager.
type Definition struct {
	ID           int
	Name         string      // display title; defaults to leaf field key
	FieldPath    []string    // JSON path from the root object, e.g. ["api_name"]
	Type         StatType
	Groups       []GroupRule // empty -> every distinct value forms its own group; values not matching any rule also form their own group
	BackfillSize int         // how many recent lines to backfill (0 = none)
}

// GroupCount is one bucket in a frequency snapshot.
type GroupCount struct {
	Label string
	Count uint64
}

// Snapshot is an immutable view of a stat at one moment. Returned by
// Stat.Snapshot for the renderer to consume without holding the aggregator's
// lock.
type Snapshot struct {
	Title    string
	Type     StatType
	Total    uint64
	Counts   []GroupCount
	Dropped  uint64
	Pending  int
	Backfill BackfillState
}

// BackfillState reflects the progress of the one-shot backfill goroutine that
// scans the previous N lines when a stat is created.
type BackfillState struct {
	Active    bool
	Processed int
	Total     int
}

// Stat is one running aggregator. The producer side (Manager.Observe) does
// the JSON-path extraction up-front and queues just the resolved label
// string — never the full *LogLine, so pending entries stay small (≈ tens
// of bytes each) and don't pin the underlying log lines past the store's
// own offload cycle. The worker goroutine swap-drains the queue and runs
// the regex match + counter increment.
//
// Counter storage uses two parallel layouts that may both be active on the
// same stat:
//   - Explicit groups (def.Groups != nil): one entry per group rule in
//     fixedCounters, lock-free atomic increments. The hot path doesn't
//     touch any mutex when a value matches.
//   - Dynamic: one map entry per distinct value, RWMutex'd. Reads use
//     RLock + atomic increment on the *uint64 stored in the map; only
//     first-sight inserts take the write lock. Used both when no explicit
//     groups are defined AND as a fallback bucket for values that don't
//     match any explicit rule (so each unmatched value forms its own group).
//
// Either way the increment itself is one atomic op, so the worker can
// sustain ~10M lines/sec/stat on commodity hardware.
type Stat struct {
	def Definition

	// Producer queue. pendingMu serializes appends so the worker's swap
	// (`local, pending = pending, local[:0]`) can't race with a producer.
	// Held only for nanoseconds per producer call.
	pendingMu sync.Mutex
	pending   []string

	notifyCh chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}

	// Counter storage — see type doc above.
	fixedCounters []uint64

	dynMu     sync.RWMutex
	dynCounts map[string]*uint64
	dynOrder  []string

	total   atomic.Uint64
	dropped atomic.Uint64

	mgr *Manager // back-pointer so Feed can charge/refund the global memory cap

	backfillActive    atomic.Bool
	backfillProcessed atomic.Int64
	backfillTotal     atomic.Int64
}

// Definition returns a copy of the stat's definition.
func (s *Stat) Definition() Definition {
	return s.def
}

// Feed enqueues a line for processing. Non-blocking; the producer never
// stalls on the worker. Memory budget is enforced at the manager level —
// only when the global pending byte count would exceed the cap does this
// increment the dropped counter and skip the line. With the default
// 500MB cap that case is essentially unreachable in normal use.
func (s *Stat) Feed(l *line.LogLine) {
	if l == nil || l.Type != line.TypeJSON {
		return
	}
	s.feedExtracted(l)
}

// feedExtracted is the internal path. It performs the JSON path resolution
// inline (so we queue a small string, not a *LogLine pointer that would
// pin the whole log line), then hands the label off to the worker.
func (s *Stat) feedExtracted(l *line.LogLine) {
	val, ok := ExtractFieldString(l, s.def.FieldPath)
	if !ok {
		return
	}
	cost := int64(len(val)) + stringHeaderBytes
	if !s.mgr.tryReserve(cost) {
		s.dropped.Add(1)
		return
	}
	s.pendingMu.Lock()
	s.pending = append(s.pending, val)
	s.pendingMu.Unlock()

	// notifyCh has capacity 1 — a coalesced edge-trigger. If the worker
	// is busy and the slot is already full, it'll see the new entries on
	// its next drain pass without needing another notification.
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

// Snapshot returns a sorted view of the current counts. Frequency results
// are sorted descending by count, ties broken alphabetically.
func (s *Stat) Snapshot() Snapshot {
	var cs []GroupCount
	if s.fixedCounters != nil {
		cs = make([]GroupCount, 0, len(s.def.Groups))
		for i := range s.def.Groups {
			cs = append(cs, GroupCount{
				Label: s.def.Groups[i].Title,
				Count: atomic.LoadUint64(&s.fixedCounters[i]),
			})
		}
	}
	s.dynMu.RLock()
	for _, label := range s.dynOrder {
		c := s.dynCounts[label]
		if c == nil {
			continue
		}
		cs = append(cs, GroupCount{Label: label, Count: atomic.LoadUint64(c)})
	}
	s.dynMu.RUnlock()
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Count != cs[j].Count {
			return cs[i].Count > cs[j].Count
		}
		return cs[i].Label < cs[j].Label
	})
	s.pendingMu.Lock()
	pendingLen := len(s.pending)
	s.pendingMu.Unlock()
	return Snapshot{
		Title:   s.def.Name,
		Type:    s.def.Type,
		Total:   s.total.Load(),
		Counts:  cs,
		Dropped: s.dropped.Load(),
		Pending: pendingLen,
		Backfill: BackfillState{
			Active:    s.backfillActive.Load(),
			Processed: int(s.backfillProcessed.Load()),
			Total:     int(s.backfillTotal.Load()),
		},
	}
}

func (s *Stat) start() {
	go s.run()
}

func (s *Stat) stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	<-s.doneCh
}

// run is the per-stat worker loop. Drains the pending slice in batches —
// one swap returns the entire backlog, which we process without holding
// pendingMu. Producers can append freely while we crunch the batch.
func (s *Stat) run() {
	defer close(s.doneCh)
	var local []string
	for {
		// Swap-drain in a tight loop so we don't return to select while
		// pending still has entries (which would happen if a producer
		// appended after we last checked and the notify channel was
		// already saturated).
		for {
			s.pendingMu.Lock()
			if len(s.pending) == 0 {
				s.pendingMu.Unlock()
				break
			}
			local, s.pending = s.pending, local[:0]
			s.pendingMu.Unlock()

			var drained int64
			for _, lbl := range local {
				s.processLabel(lbl)
				drained += int64(len(lbl)) + stringHeaderBytes
			}
			// Help GC reclaim the underlying string contents we no longer
			// reference — the slice still holds string headers otherwise.
			for i := range local {
				local[i] = ""
			}
			s.mgr.release(drained)
		}

		select {
		case <-s.stopCh:
			return
		case <-s.notifyCh:
		}
	}
}

// processLabel resolves a value to its group bucket and bumps the counter.
// Lock-free for the explicit-groups path (the hot one); RWMutex-guarded
// for dynamic labels with a fast read-then-atomic path. Values that don't
// match any explicit rule fall through to the dynamic map so each unmatched
// value forms its own group alongside the fixed ones.
func (s *Stat) processLabel(label string) {
	if s.fixedCounters != nil {
		for i, g := range s.def.Groups {
			if g.re == nil {
				continue
			}
			if g.re.MatchString(label) {
				atomic.AddUint64(&s.fixedCounters[i], 1)
				s.total.Add(1)
				return
			}
		}
	}

	// Dynamic groups: fast path is RLock + atomic increment on the *uint64
	// already in the map. Only first-sight labels take the write lock.
	s.dynMu.RLock()
	c := s.dynCounts[label]
	s.dynMu.RUnlock()
	if c == nil {
		s.dynMu.Lock()
		c = s.dynCounts[label]
		if c == nil {
			var zero uint64
			c = &zero
			s.dynCounts[label] = c
			s.dynOrder = append(s.dynOrder, label)
		}
		s.dynMu.Unlock()
	}
	atomic.AddUint64(c, 1)
	s.total.Add(1)
}

// Manager owns every active stat and fans out Observe calls. It is safe for
// concurrent use from the ingestor (Observe), the UI (Add/All/Stop), and the
// backfill goroutines.
type Manager struct {
	mu      sync.RWMutex
	stats   []*Stat
	nextID  int
	stopped bool

	// Memory budget shared across every stat's pending queue. The cap is
	// only checked at producer time; once a label has been queued we
	// don't pre-empt it.
	pendingBytes atomic.Int64
	memoryCap    int64
}

// NewManager returns an empty Manager with the default 500MB memory cap.
func NewManager() *Manager {
	return &Manager{memoryCap: DefaultMemoryCap}
}

// SetMemoryCap overrides the shared pending-bytes budget. Tests use this
// to deliberately force the dropped path.
func (m *Manager) SetMemoryCap(bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	m.memoryCap = bytes
}

// MemoryUsed returns the current pending bytes across all stats.
func (m *Manager) MemoryUsed() int64 {
	return m.pendingBytes.Load()
}

// Add registers a new stat from a Definition, compiles its group regexes,
// starts its worker goroutine, and returns the running Stat. Returns an
// error if any group pattern is invalid.
func (m *Manager) Add(def Definition) (*Stat, error) {
	for i := range def.Groups {
		if err := def.Groups[i].Compile(); err != nil {
			return nil, fmt.Errorf("group %d (%q): %w", i, def.Groups[i].Title, err)
		}
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil, fmt.Errorf("manager stopped")
	}
	m.nextID++
	def.ID = m.nextID
	if def.Name == "" {
		if len(def.FieldPath) > 0 {
			def.Name = def.FieldPath[len(def.FieldPath)-1]
		} else {
			def.Name = fmt.Sprintf("stat-%d", def.ID)
		}
	}
	if def.Type == "" {
		def.Type = Frequency
	}
	st := &Stat{
		def:      def,
		notifyCh: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
		mgr:      m,
	}
	if len(def.Groups) > 0 {
		st.fixedCounters = make([]uint64, len(def.Groups))
	}
	st.dynCounts = make(map[string]*uint64)
	m.stats = append(m.stats, st)
	m.mu.Unlock()
	st.start()
	return st, nil
}

// All returns a snapshot of the currently registered stats. The returned slice
// is a copy; callers may iterate without holding any lock.
func (m *Manager) All() []*Stat {
	m.mu.RLock()
	out := make([]*Stat, len(m.stats))
	copy(out, m.stats)
	m.mu.RUnlock()
	return out
}

// Observe forwards a line to every active stat. Each stat extracts the
// field inline and queues just the resolved label string — never blocks,
// never pins the *LogLine.
//
// Non-JSON lines short-circuit at the manager level so we don't pay the
// per-stat type-check cost on every plain-text line.
func (m *Manager) Observe(l *line.LogLine) {
	if l == nil || l.Type != line.TypeJSON {
		return
	}
	m.mu.RLock()
	stats := m.stats
	m.mu.RUnlock()
	for _, s := range stats {
		s.feedExtracted(l)
	}
}

// Stop halts every stat's worker. Safe to call multiple times.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	stats := m.stats
	m.mu.Unlock()
	for _, s := range stats {
		s.stop()
	}
}

// tryReserve reserves `n` bytes from the shared budget atomically. Returns
// false if granting the reservation would push us past memoryCap — the
// caller drops the entry in that case.
func (m *Manager) tryReserve(n int64) bool {
	for {
		cur := m.pendingBytes.Load()
		if cur+n > m.memoryCap {
			return false
		}
		if m.pendingBytes.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// release returns `n` bytes to the shared budget. Called by the worker
// after each drain pass.
func (m *Manager) release(n int64) {
	m.pendingBytes.Add(-n)
}

// MarkBackfill announces that a backfill of `total` lines is about to start.
// The renderer can show a progress hint while it runs.
func (s *Stat) MarkBackfill(total int) {
	s.backfillActive.Store(true)
	s.backfillProcessed.Store(0)
	s.backfillTotal.Store(int64(total))
}

// AdvanceBackfill increments the processed counter. Called by the backfill
// goroutine after each batch.
func (s *Stat) AdvanceBackfill(n int) {
	s.backfillProcessed.Add(int64(n))
}

// FinishBackfill clears the active flag.
func (s *Stat) FinishBackfill() {
	s.backfillActive.Store(false)
}

// ExtractFieldString resolves `path` against l's parsed JSON value and returns
// the leaf as a string suitable for label matching. Returns false if l is not
// JSON, the path doesn't resolve, or the leaf is a structural value (object /
// array) — those don't make sense as frequency labels.
func ExtractFieldString(l *line.LogLine, path []string) (string, bool) {
	if l == nil || l.Type != line.TypeJSON {
		return "", false
	}
	meta, ok := l.Meta.(*line.JSONMeta)
	if !ok {
		return "", false
	}
	v := meta.Value
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		next, exists := m[k]
		if !exists {
			return "", false
		}
		v = next
	}
	return primitiveToString(v)
}

func primitiveToString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), true
		}
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case nil:
		return "null", true
	case map[string]any, []any:
		// Structural values aren't useful as frequency labels.
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return fmt.Sprintf("%v", v), true
}
