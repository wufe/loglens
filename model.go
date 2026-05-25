package main

import (
	"fmt"
	"github.com/wufe/loglens/input"
	"github.com/wufe/loglens/line"
	"github.com/wufe/loglens/pattern"
	"github.com/wufe/loglens/render"
	"github.com/wufe/loglens/stats"
	"github.com/wufe/loglens/store"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tickInterval is the UI refresh cadence. The ingestor goroutine streams
// lines into shared state independently; the UI samples that state at 30 Hz
// to paint new content and run maintenance (offload, parser release). Pick
// ~33 ms so key events still feel instantaneous (they bypass the tick).
const tickInterval = 33 * time.Millisecond

// Message types. LineMsg exists for tests only — production uses the
// ingestor goroutine to populate state and never emits LineMsg.
type LineMsg input.RawLine
type ReparseMsg []int
type EOFMsg struct{ ExitCode int }
type tickMsg time.Time

// FlatEntry represents a visible entry in the flattened view.
type FlatEntry struct {
	Line      *line.LogLine
	Depth     int
	IsChild   bool
	ParentIdx int
	ChildIdx  int
}

type model struct {
	// s holds the mutable caches (store, parser, visRows, visRowsBIT) shared
	// with the ingestor goroutine under s.mu. store/parser accessors go
	// through s to guarantee write-safety across goroutines.
	s        *sharedState
	ingestor *ingestor // nil in tests; started by initialModel in production

	// store/parser are alias pointers to s.store / s.parser — kept as fields
	// so existing test code (m.store.Get, m.store.Len) keeps compiling.
	store *store.LineStore

	cursor      int
	offset      int
	offsetRow   int // visual rows to skip within the line at m.offset (partial tree/wrap display)
	width       int
	height      int
	follow      bool
	wrapMode    bool
	inputSrc    input.InputSource
	searchMode  bool
	searchQuery string
	searchInput textinput.Model
	statusMsg   string
	eof         bool
	exitCode    int
	exitOnEOF   bool      // set from --exit-on-eof / -x
	eofCountdownStart time.Time // non-zero when auto-exit countdown is running
	cursorPath  []int // path within expanded JSON tree; nil = cursor on parent line
	showMinimap bool  // toggled with "m" — overlays a VS-Code-style braille map on the right
	styles      *Styles
	rStyles     *render.Styles
	noFollow    bool

	bench *benchLogger

	// Toast: short-lived informational message rendered in the status bar.
	// statusMsg lives on the existing field; statusMsgUntil bounds its
	// visible lifetime so the renderer auto-clears expired messages without
	// any background timer.
	statusMsgUntil time.Time

	// Modal stack for the field-action / stat-setup wizard. nil = no modal.
	modal *modalState

	// Stats subsystem: when statsMgr is non-nil and has at least one stat,
	// the viewport splits horizontally to expose a stats pane below the logs.
	statsMgr        *stats.Manager
	statsLayout     statsLayoutMode
	statsFocused    bool // true when key events drive the stats pane (tab toggles)
	statsBoxOffset  int  // leftmost stat box currently visible in the row
	statsBoxFocused int  // index of the focused stat box (within statsMgr.All())

	// Pattern panel: toggled with "p". Recomputes from the visible-line
	// window every render (cheap — see pattern package). When focused, the
	// cursor selects one pattern and the log viewport highlights the lines
	// that mask to its skeleton.
	patternsVisible  bool
	patternsFocused  bool
	patternCursor    int // index into the pattern list as rendered this tick
	patternBoxOffset int // top-row scroll offset within the pattern list
}

// statsLayoutMode controls how vertical space is split between the log
// viewport and the stats container.
type statsLayoutMode int

const (
	// statsLayoutSplit is the default once at least one stat exists: logs
	// take 2/3, stats take 1/3 of the available height.
	statsLayoutSplit statsLayoutMode = iota
	// statsLayoutFullLogs hides the stats pane entirely (z toggle while
	// focused on logs). Stats keep updating in the background.
	statsLayoutFullLogs
	// statsLayoutFullStats hides the log viewport (z toggle while focused
	// on stats).
	statsLayoutFullStats
)

func initialModel(src input.InputSource, noFollow bool, bench *benchLogger, maxDiskCap int64) model {
	ti := textinput.New()
	ti.Placeholder = "Search..."
	ti.CharLimit = 256

	s := DefaultStyles()
	rs := &render.Styles{
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

	var lineStore *store.LineStore
	if maxDiskCap > 0 {
		lineStore = store.NewWithDiskCap(maxDiskCap)
	} else {
		lineStore = store.New()
	}

	shared := newSharedState(lineStore)
	statsMgr := stats.NewManager()
	shared.statsMgr.Store(statsMgr)
	var ig *ingestor
	if src != nil {
		ig = newIngestor(shared, src, bench)
		ig.start()
	}

	return model{
		s:           shared,
		ingestor:    ig,
		store:       lineStore,
		follow:      !noFollow,
		inputSrc:    src,
		searchInput: ti,
		exitCode:    -1,
		styles:      s,
		rStyles:     rs,
		noFollow:    noFollow,
		bench:       bench,
		statsMgr:    statsMgr,
		statsLayout: statsLayoutSplit,
	}
}

// showToast surfaces a transient message in the status bar for ~2s.
func (m *model) showToast(msg string) {
	m.statusMsg = msg
	m.statusMsgUntil = time.Now().Add(2 * time.Second)
}

func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case LineMsg:
		// Tests emit LineMsg synchronously (no ingestor goroutine running).
		// Production path never emits LineMsg — the ingestor owns state
		// mutations and the UI samples via tickMsg.
		m.s.mu.Lock()
		m.s.width = m.width
		m.s.wrapMode = m.wrapMode
		m.s.ingestOneLocked(input.RawLine(msg))
		if m.follow && m.s.store.Len() > 0 {
			oldCursor := m.cursor
			m.cursor = m.s.store.Len() - 1
			m.cursorPath = nil
			if oldCursor != m.cursor && oldCursor >= 0 && oldCursor < m.s.store.Len() {
				m.s.recomputeVisRowsLocked(oldCursor, false, nil)
			}
			m.s.recomputeVisRowsLocked(m.cursor, true, nil)
		}
		m.s.mu.Unlock()
		if m.follow {
			m.adjustOffset()
		}
		return m, nil

	case tickMsg:
		// Periodic UI sync: pick up any lines the ingestor appended since
		// the last tick and run maintenance (offload, parser release).
		m.s.mu.Lock()
		m.s.width = m.width
		m.s.wrapMode = m.wrapMode
		oldCursor := m.cursor
		if m.follow && m.s.store.Len() > 0 {
			m.cursor = m.s.store.Len() - 1
			// Only reset cursorPath when the cursor actually moves to a new
			// line. When the cursor stays put (no new lines since last tick),
			// preserve the user's in-tree position — otherwise expanding a
			// JSON line at EOF and then navigating into it is impossible:
			// every tick (~33ms) wipes cursorPath back to the parent line.
			if oldCursor != m.cursor {
				m.cursorPath = nil
				if oldCursor >= 0 && oldCursor < m.s.store.Len() {
					m.s.recomputeVisRowsLocked(oldCursor, false, nil)
				}
			}
			m.s.recomputeVisRowsLocked(m.cursor, true, m.cursorPath)
		} else if m.cursor >= 0 && m.cursor < m.s.store.Len() {
			// Non-follow: cursor line may have been appended as non-cursor by
			// the ingestor; refresh it so wrap counts stay accurate.
			m.s.recomputeVisRowsLocked(m.cursor, true, m.cursorPath)
		}
		m.s.store.RunOffloadCycle(m.cursor, m.offset, m.viewportHeight())
		// parser.ReleaseOldLines now runs on the ingestor goroutine itself —
		// the parser's state is ingestor-exclusive, so no lock needed.
		if !m.eof && m.s.eof.Load() {
			m.eof = true
			m.exitCode = int(m.s.exitCode.Load())
		}
		m.s.mu.Unlock()
		if m.follow {
			m.adjustOffset()
		}

		// Auto-exit on EOF when in follow mode: start a 5s countdown, quit
		// when it elapses. If the user leaves follow mode mid-countdown,
		// cancel it (they're interacting, so require manual close).
		if m.exitOnEOF && m.eof {
			if m.follow {
				if m.eofCountdownStart.IsZero() {
					m.eofCountdownStart = time.Now()
				} else if time.Since(m.eofCountdownStart) >= 5*time.Second {
					if m.ingestor != nil {
						m.ingestor.stop()
					}
					if m.inputSrc != nil {
						m.inputSrc.Stop()
					}
					if m.statsMgr != nil {
						m.statsMgr.Stop()
					}
					m.s.store.Close()
					pattern.ClearCache()
					return m, tea.Quit
				}
			} else {
				m.eofCountdownStart = time.Time{}
			}
		}

		return m, tickCmd()

	case ReparseMsg:
		return m, nil

	case EOFMsg:
		m.eof = true
		m.exitCode = msg.ExitCode
		if m.bench != nil {
			m.s.mu.RLock()
			n := m.s.store.Len()
			m.s.mu.RUnlock()
			m.bench.eofReached(n)
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.s.mu.Lock()
		m.s.width = msg.Width
		m.s.rebuildVisRowsLocked(m.cursor)
		m.s.mu.Unlock()
		m.adjustOffset()
		return m, nil

	case tea.KeyMsg:
		if m.modal != nil {
			return m.updateModal(msg)
		}
		if m.searchMode {
			return m.updateSearchMode(msg)
		}
		if m.statsFocused {
			return m.updateStatsMode(msg)
		}
		if m.patternsFocused {
			return m.updatePatternsMode(msg)
		}
		return m.updateNormalMode(msg)
	}

	return m, nil
}

// rebuildVisRows rebuilds every cached row count from scratch under the
// shared lock. Kept as a model method so benchmarks can drive it directly.
func (m *model) rebuildVisRows() {
	m.s.mu.Lock()
	m.s.width = m.width
	m.s.wrapMode = m.wrapMode
	m.s.rebuildVisRowsLocked(m.cursor)
	m.s.mu.Unlock()
}

func (m model) updateSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.searchMode = false
		m.searchQuery = m.searchInput.Value()
		if m.searchQuery != "" {
			m.s.mu.Lock()
			idx := m.s.store.Search(m.searchQuery, m.cursor+1)
			if idx >= 0 {
				m.cursor = idx
				m.follow = false
				m.adjustOffsetLocked()
			}
			m.s.mu.Unlock()
		}
		return m, nil

	case "esc":
		m.searchMode = false
		return m, nil
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func (m model) updateNormalMode(msg tea.KeyMsg) (retModel tea.Model, retCmd tea.Cmd) {
	// Quit is handled outside the shared lock because ingestor.stop() waits
	// on the ingestor goroutine, which itself needs the lock to drain.
	if isKeyQuit(msg) && m.modal == nil {
		if m.ingestor != nil {
			m.ingestor.stop()
		}
		if m.inputSrc != nil {
			m.inputSrc.Stop()
		}
		if m.statsMgr != nil {
			m.statsMgr.Stop()
		}
		m.s.store.Close()
		pattern.ClearCache()
		return m, tea.Quit
	}

	oldCursor := m.cursor
	var keyStart time.Time
	trackKey := ""
	if m.bench != nil {
		switch s := msg.String(); s {
		case "g", "G", "up", "down", "k", "j", "left", "right", "h", "l",
			"pgup", "pgdown", "ctrl+u", "ctrl+d", "ctrl+k", "ctrl+j":
			keyStart = time.Now()
			trackKey = s
		}
	}

	m.s.mu.Lock()
	// Sync UI hints into shared state so recompute/rebuild use current values.
	m.s.width = m.width
	m.s.wrapMode = m.wrapMode

	// Registered second → runs first (still under lock). Reassigns retModel
	// with up-to-date cache state after the switch below has mutated cursor.
	defer m.s.mu.Unlock()
	defer func() {
		mm, ok := retModel.(model)
		if !ok {
			return
		}
		if oldCursor != mm.cursor && oldCursor >= 0 && oldCursor < mm.s.store.Len() {
			mm.s.recomputeVisRowsLocked(oldCursor, false, nil)
		}
		if mm.cursor >= 0 && mm.cursor < mm.s.store.Len() {
			mm.s.recomputeVisRowsLocked(mm.cursor, true, mm.cursorPath)
		}
		if trackKey != "" && mm.bench != nil {
			mm.bench.keyTimed(trackKey, time.Since(keyStart), mm.cursor, mm.s.store.Len())
		}
		mm.s.store.PrefetchAdjacent(mm.cursor)
		retModel = mm
	}()

	switch {
	case isKeyEnter(msg):
		// Cursor on a JSON child: open the field action modal.
		if len(m.cursorPath) > 0 && m.cursor >= 0 && m.cursor < m.store.Len() {
			if m.openFieldActionModal() {
				break
			}
		}
		// Otherwise, preserve historical behaviour (expand or drill into
		// the JSON tree) so existing muscle memory still works on lines
		// where there's no field selection yet.
		if m.cursor >= 0 && m.cursor < m.store.Len() {
			l := m.store.Get(m.cursor)
			if l.Expandable && !l.Expanded {
				expandAndPopulate(l)
				if l.Children != nil && len(l.Children) > 0 {
					m.cursorPath = []int{0}
				}
				m.s.recomputeVisRowsLocked(m.cursor, true, m.cursorPath)
				m.adjustOffsetLocked()
			}
		}

	case isKeyTab(msg):
		// Cycle focus forward: logs → stats (if any) → patterns (if visible) → logs.
		// Skip panes that aren't actually rendered so the user never lands on
		// an empty focus state.
		switch {
		case m.statsMgr != nil && len(m.statsMgr.All()) > 0:
			m.statsFocused = true
			if m.statsLayout == statsLayoutFullLogs {
				m.statsLayout = statsLayoutSplit
			}
		case m.patternsVisible:
			m.patternsFocused = true
		}

	case isKeyZoom(msg):
		// Toggle full-height for the focused pane (logs side here).
		if m.statsMgr != nil && len(m.statsMgr.All()) > 0 {
			if m.statsLayout == statsLayoutFullLogs {
				m.statsLayout = statsLayoutSplit
			} else {
				m.statsLayout = statsLayoutFullLogs
			}
			m.adjustOffsetLocked()
		}

	case isKeyUp(msg):
		if m.store.Len() == 0 {
			break
		}
		if len(m.cursorPath) > 0 {
			// Navigate within expanded tree
			l := m.store.Get(m.cursor)
			prev := prevVisiblePath(l, m.cursorPath)
			if prev == nil {
				// Exit tree to parent line
				m.cursorPath = nil
			} else {
				m.cursorPath = prev
			}
			m.follow = false
			m.adjustOffsetLocked()
		} else if m.cursor > 0 {
			prevIdx := m.cursor - 1
			// Skip hidden group members
			for prevIdx > 0 && m.store.IsHiddenGroupMember(prevIdx) {
				prevIdx--
			}
			m.cursor = prevIdx
			// If the previous line has an expanded tree, enter at its last visible node
			prevLine := m.store.Get(m.cursor)
			if prevLine.Expanded && prevLine.Children != nil && len(prevLine.Children) > 0 {
				m.cursorPath = lastVisiblePath(prevLine)
			}
			m.follow = false
			m.adjustOffsetLocked()
		}

	case isKeyDown(msg):
		if m.store.Len() == 0 {
			break
		}
		if len(m.cursorPath) > 0 {
			// Navigate within expanded tree
			l := m.store.Get(m.cursor)
			next := nextVisiblePath(l, m.cursorPath)
			if next == nil {
				// Exit tree, move to next top-level line
				m.cursorPath = nil
				if m.cursor < m.store.Len()-1 {
					m.cursor++
					// Skip hidden group members
					for m.cursor < m.store.Len()-1 && m.store.IsHiddenGroupMember(m.cursor) {
						m.cursor++
					}
					// Auto-enter expanded tree on the new line
					nl := m.store.Get(m.cursor)
					if nl.Expanded && nl.Children != nil && len(nl.Children) > 0 {
						m.cursorPath = []int{0}
					}
					if m.cursor == m.store.Len()-1 {
						m.follow = true
					}
				}
			} else {
				m.cursorPath = next
			}
			m.adjustOffsetLocked()
		} else {
			l := m.store.Get(m.cursor)
			if l.Expanded && l.Children != nil && len(l.Children) > 0 {
				// Current line has an expanded tree: enter it
				m.cursorPath = []int{0}
			} else if m.cursor < m.store.Len()-1 {
				m.cursor++
				// Skip hidden group members
				for m.cursor < m.store.Len()-1 && m.store.IsHiddenGroupMember(m.cursor) {
					m.cursor++
				}
				// Auto-enter expanded tree on the new line
				nl := m.store.Get(m.cursor)
				if nl.Expanded && nl.Children != nil && len(nl.Children) > 0 {
					m.cursorPath = []int{0}
				}
				if m.cursor == m.store.Len()-1 {
					m.follow = true
				}
			}
			m.adjustOffsetLocked()
		}

	case isKeyRight(msg):
		if m.cursor >= 0 && m.cursor < m.store.Len() {
			l := m.store.Get(m.cursor)
			if len(m.cursorPath) > 0 {
				node := getChildAtPath(l, m.cursorPath)
				if node != nil && node.Expandable {
					// Drill deeper into tree
					expandAndPopulate(node)
					if node.Children != nil && len(node.Children) > 0 {
						m.cursorPath = append(append([]int{}, m.cursorPath...), 0)
					}
				} else {
					// Non-expandable: jump to next expandable sibling/node
					if next := nextExpandablePath(l, m.cursorPath); next != nil {
						m.cursorPath = next
					}
				}
			} else if l.Expandable {
				// Expand and enter tree
				expandAndPopulate(l)
				if l.Children != nil && len(l.Children) > 0 {
					m.cursorPath = []int{0}
				}
			}
			// Cursor-line structure may have changed (expansion/descendant
			// expansion); refresh its cached visual row count.
			m.s.recomputeVisRowsLocked(m.cursor, true, m.cursorPath)
			m.adjustOffsetLocked()
		}

	case isKeyLeft(msg):
		if m.cursor >= 0 && m.cursor < m.store.Len() {
			l := m.store.Get(m.cursor)
			if len(m.cursorPath) > 0 {
				node := getChildAtPath(l, m.cursorPath)
				if node != nil && node.Expanded {
					// Current node is expanded: collapse it, stay on same node
					collapseAll(node)
				} else if len(m.cursorPath) > 1 {
					// Collapsed/leaf node: move to parent
					m.cursorPath = m.cursorPath[:len(m.cursorPath)-1]
				} else {
					// Top-level child: exit tree to parent line
					m.cursorPath = nil
				}
			} else if l.Expanded {
				// Cursor on parent line: collapse
				collapseAll(l)
			}
			m.s.recomputeVisRowsLocked(m.cursor, true, m.cursorPath)
			m.adjustOffsetLocked()
		}

	case msg.String() == "g":
		m.cursor = 0
		m.offset = 0
		m.offsetRow = 0
		m.follow = false
		m.cursorPath = nil

	case msg.String() == "G":
		if m.store.Len() > 0 {
			m.cursor = m.store.Len() - 1
		}
		m.follow = true
		m.cursorPath = nil
		m.adjustOffsetLocked()

	case msg.String() == "f" || msg.String() == "s":
		m.follow = !m.follow
		m.cursorPath = nil
		if m.follow && m.store.Len() > 0 {
			m.cursor = m.store.Len() - 1
			m.adjustOffsetLocked()
		}

	case msg.String() == "m":
		m.showMinimap = !m.showMinimap

	case msg.String() == "p":
		// Toggle the patterns pane. Recompute clamps the cursor when the
		// pane reopens onto a different visible-line window.
		m.patternsVisible = !m.patternsVisible
		if !m.patternsVisible {
			m.patternsFocused = false
			m.patternCursor = 0
			m.patternBoxOffset = 0
		}
		m.adjustOffsetLocked()

	case msg.String() == "w":
		m.wrapMode = !m.wrapMode
		m.s.wrapMode = m.wrapMode
		// Wrap affects every non-cursor line's row count. Rebuild in one pass.
		m.s.rebuildVisRowsLocked(m.cursor)
		m.adjustOffsetLocked()

	case msg.String() == "pgup" || msg.String() == "ctrl+u" || msg.String() == "ctrl+k":
		half := m.viewportHeight() / 2
		m.cursor -= half
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.follow = false
		m.cursorPath = nil
		m.adjustOffsetLocked()

	case msg.String() == "pgdown" || msg.String() == "ctrl+d" || msg.String() == "ctrl+j":
		half := m.viewportHeight() / 2
		m.cursor += half
		if m.cursor >= m.store.Len() {
			m.cursor = m.store.Len() - 1
		}
		if m.cursor == m.store.Len()-1 {
			m.follow = true
		}
		m.cursorPath = nil
		m.adjustOffsetLocked()

	case msg.String() == "/":
		m.searchMode = true
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		return m, textinput.Blink

	case msg.String() == "n":
		if m.searchQuery != "" {
			idx := m.store.Search(m.searchQuery, m.cursor+1)
			if idx >= 0 {
				m.cursor = idx
				m.follow = false
				m.cursorPath = nil
				m.adjustOffsetLocked()
			}
		}

	case msg.String() == "N":
		if m.searchQuery != "" {
			idx := m.store.SearchReverse(m.searchQuery, m.cursor-1)
			if idx >= 0 {
				m.cursor = idx
				m.follow = false
				m.cursorPath = nil
				m.adjustOffsetLocked()
			}
		}
	}

	return m, nil
}

// adjustOffset takes its own read lock; callers must NOT hold m.s.mu already.
// For call sites already under the lock, use adjustOffsetLocked.
func (m *model) adjustOffset() {
	m.s.mu.RLock()
	m.adjustOffsetLocked()
	m.s.mu.RUnlock()
}

// adjustOffsetLocked is the real implementation. Caller must hold m.s.mu
// (read is sufficient — it only reads visRows/BIT and writes m.offset fields
// which live on the value model).
func (m *model) adjustOffsetLocked() {
	var start time.Time
	if m.bench != nil {
		start = time.Now()
	}
	vh := m.viewportHeight()
	if vh <= 0 {
		if m.bench != nil {
			m.bench.recordAdjust(time.Since(start))
		}
		return
	}

	// Normalize: if offset points to a hidden line (e.g., after a reparse
	// turned it into a hidden group member), back up to the nearest visible
	// line so the viewport includes its content (typically the group's tree).
	if m.offset > 0 && m.offset < m.s.store.Len() && m.cachedVisRows(m.offset) == 0 {
		for m.offset > 0 && m.cachedVisRows(m.offset) == 0 {
			m.offset--
		}
		m.offsetRow = 0
	}

	// Compute absolute visual row for the cursor and viewport top.
	cursorAbsRow := m.absoluteVisualRow(m.cursor, m.cursorPath)
	offsetAbsRow := m.absoluteVisualRow(m.offset, nil) + m.offsetRow

	// Scroll up: cursor above viewport
	if cursorAbsRow < offsetAbsRow {
		m.setAbsoluteOffset(cursorAbsRow)
		if m.bench != nil {
			m.bench.recordAdjust(time.Since(start))
		}
		return
	}

	// Scroll down: cursor's last row below viewport. Use the cursor's full
	// visual extent so multi-row cursor lines (long wrapped JSON, expanded
	// trees) don't get clipped at the bottom — the previous comparison
	// against cursorAbsRow alone left the cursor's wrapped tail off-screen
	// when sitting on the last line of a long log.
	cursorRows := m.cursorVisualHeightLocked()
	if cursorRows < 1 {
		cursorRows = 1
	}
	cursorEndRow := cursorAbsRow + cursorRows
	if cursorEndRow > offsetAbsRow+vh {
		// If the cursor itself is taller than the viewport, anchor at its top
		// so the user always sees the start of the cursor line. Otherwise,
		// scroll just enough to reveal the cursor's last row at the bottom.
		var targetTop int
		if cursorRows >= vh {
			targetTop = cursorAbsRow
		} else {
			targetTop = cursorEndRow - vh
		}
		m.setAbsoluteOffset(targetTop)
		if m.bench != nil {
			m.bench.recordAdjust(time.Since(start))
		}
		return
	}

	if m.bench != nil {
		m.bench.recordAdjust(time.Since(start))
	}
}

// cursorVisualHeightLocked returns the visual row count of the cursor.
func (m *model) cursorVisualHeightLocked() int {
	if m.cursor < 0 || m.cursor >= m.s.store.Len() {
		return 1
	}
	if len(m.cursorPath) > 0 {
		return 1
	}
	if m.cursor < len(m.s.visRows) {
		return m.s.visRows[m.cursor]
	}
	return 1
}

// cachedVisRows returns the cached visual row count for line i, falling back
// to direct computation if the cache isn't populated for that index. Caller
// must hold at least a read lock on m.s.mu.
func (m *model) cachedVisRows(i int) int {
	if i < 0 || i >= m.s.store.Len() {
		return 1
	}
	if i < len(m.s.visRows) {
		return m.s.visRows[i]
	}
	return m.visualRowsForLine(i)
}

// absoluteVisualRow returns the absolute visual row index for a given
// line index and optional cursor path within the tree. O(log N) via the
// Fenwick tree (was O(lineIdx)). Caller must hold m.s.mu (read is enough).
func (m *model) absoluteVisualRow(lineIdx int, path []int) int {
	var row int
	if m.s.visRowsBIT != nil && lineIdx <= len(m.s.visRows) {
		if lineIdx > m.s.store.Len() {
			lineIdx = m.s.store.Len()
		}
		row = m.s.visRowsBIT.prefix(lineIdx)
	} else {
		// Fallback (shouldn't normally happen).
		for i := 0; i < lineIdx && i < m.s.store.Len(); i++ {
			row += m.cachedVisRows(i)
		}
	}
	if len(path) > 0 && lineIdx >= 0 && lineIdx < m.s.store.Len() {
		l := m.s.store.Get(lineIdx)
		if (m.wrapMode || lineIdx == m.cursor) && l.Expanded && l.Children != nil {
			// Wrapping changes row offsets — compute arithmetically instead
			// of re-rendering the whole tree.
			_, cr := m.expandedTreeRows(l, path)
			if cr >= 0 {
				row += cr
			}
		} else {
			row += cursorRowInTree(l, path)
		}
	}
	return row
}

// setAbsoluteOffset sets m.offset and m.offsetRow from an absolute visual row,
// allowing the viewport to start partway through an expanded tree or wrapped
// line. O(log N) via the Fenwick tree (was O(N)).
func (m *model) setAbsoluteOffset(absRow int) {
	if absRow < 0 {
		absRow = 0
	}
	n := m.s.store.Len()
	if n == 0 {
		m.offset = 0
		m.offsetRow = 0
		return
	}
	if m.s.visRowsBIT != nil {
		i := m.s.visRowsBIT.findByPrefix(absRow, n)
		if i >= n {
			// Past the end.
			m.offset = n - 1
			m.offsetRow = 0
			return
		}
		m.offset = i
		m.offsetRow = absRow - m.s.visRowsBIT.prefix(i)
		return
	}
	// Fallback linear scan.
	row := 0
	for i := 0; i < n; i++ {
		lineRows := m.cachedVisRows(i)
		if lineRows == 0 {
			continue
		}
		if row+lineRows > absRow {
			m.offset = i
			m.offsetRow = absRow - row
			return
		}
		row += lineRows
	}
	m.offset = n - 1
	m.offsetRow = 0
}

// visualRowsForLine returns how many terminal rows a line at index i occupies,
// including expanded children. Caller must hold at least a read lock on m.s.mu
// (this reads store state mutated by the ingestor).
func (m model) visualRowsForLine(i int) int {
	if i < 0 || i >= m.s.store.Len() {
		return 1
	}
	l := m.s.store.Get(i)

	// Hidden collapsed group members take no rows
	if m.s.store.IsHiddenGroupMember(i) {
		return 0
	}

	// Expanded lines with children: count parent + all visible descendants
	if l.Expanded && l.Children != nil {
		isCursor := (i == m.cursor)
		if m.wrapMode || isCursor {
			// Arithmetic walk instead of RenderExpanded — ~100× faster on
			// deep trees, so cursor up/down inside a tree stays at µs scale.
			var cp []int
			if isCursor {
				if len(m.cursorPath) > 0 {
					cp = m.cursorPath
				} else {
					cp = []int{-1}
				}
			}
			total, _ := m.expandedTreeRows(l, cp)
			return total
		}
		return totalVisibleRows(l)
	}

	isCursor := (i == m.cursor) && len(m.cursorPath) == 0
	if !isCursor && !m.wrapMode {
		return 1
	}

	// Fast path for wrapped lines (either global wrap mode, or the cursor line
	// which always wraps): compute row count arithmetically from raw content
	// length, avoiding ANSI rendering + wrapping. len(l.Raw) equals visible
	// width for ASCII (overwhelmingly common in log files). This turns each
	// cursor move from a RenderLineWrapped call (~20µs) into integer math, and
	// the O(N × render) rebuildVisRows into O(N) arithmetic.
	if m.width > 3 {
		// Collapsed inline JSON: content is truncated to fit — always 1 row.
		if l.Type == line.TypeJSON && !l.Expanded && len(l.Segments) == 0 {
			return 1
		}
		totalVis := 3 + len(l.Raw) // gutter(1) + indicator(2) + content
		if totalVis <= m.width {
			return 1
		}
		return (totalVis + m.width - 1) / m.width
	}

	rows := render.RenderLineWrapped(l, m.width, isCursor, m.wrapMode, m.rStyles)
	return len(rows)
}

func (m model) viewportHeight() int {
	h := m.height - 1 // status bar
	if m.searchMode {
		h-- // search bar
	}
	// Stats pane (when active) eats into the log viewport. statsAreaHeight
	// returns 0 when no stats exist or layout hides the pane, so this is a
	// no-op in the default single-pane case.
	h -= m.statsAreaHeight()
	// Patterns pane stacks below stats when toggled on; same accounting.
	h -= m.patternsAreaHeight()
	// Stats-only layout drives the logs region to 0; the View skips log
	// rendering entirely in that mode rather than reserving a phantom row.
	if m.statsLayout == statsLayoutFullStats &&
		m.statsMgr != nil && len(m.statsMgr.All()) > 0 {
		if h < 0 {
			h = 0
		}
		return h
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	// View reads store+visRows while the ingestor may be appending. Hold a
	// read lock so Get/Len observations are consistent with each other.
	m.s.mu.RLock()
	defer m.s.mu.RUnlock()

	// Hard clamp: ensure no rendered row exceeds terminal width.
	// Even 1 character too wide causes the terminal to wrap, which
	// cascades into garbled output for all subsequent lines.
	clamp := lipgloss.NewStyle().MaxWidth(m.width)

	var sb strings.Builder
	vh := m.viewportHeight()

	// Show hint when there's no content
	if m.s.store.Len() == 0 && m.eof {
		sb.WriteString(m.styles.Plain.Faint(true).Render("  (no content)"))
		sb.WriteByte('\n')
		for i := 1; i < vh; i++ {
			sb.WriteByte('\n')
		}
		sb.WriteString(m.renderStatusBar())
		return sb.String()
	}

	// Buffer rows + their source line indices so we can overlay the minimap
	// and compute the viewport indicator after rendering.
	rows := make([]string, 0, vh)
	rowLineIdx := make([]int, 0, vh)

	for i := m.offset; i < m.store.Len() && len(rows) < vh; i++ {
		l := m.store.Get(i)

		// Skip non-head members of collapsed multiline JSON groups
		if m.store.IsHiddenGroupMember(i) {
			continue
		}

		// Skip visual rows at the offset line for partial tree/wrap display
		skipRows := 0
		if i == m.offset {
			skipRows = m.offsetRow
		}

		isCursor := (i == m.cursor)

		if l.Expanded && l.Children != nil {
			// Build cursor path for RenderExpanded
			var cp []int
			if isCursor {
				if len(m.cursorPath) > 0 {
					cp = m.cursorPath
				} else {
					cp = []int{-1} // cursor on parent line itself
				}
			}
			expandedLines, _ := render.RenderExpanded(l, m.width, cp, m.wrapMode, m.rStyles)
			for rowIdx := skipRows; rowIdx < len(expandedLines); rowIdx++ {
				if len(rows) >= vh {
					break
				}
				rows = append(rows, clamp.Render(expandedLines[rowIdx]))
				rowLineIdx = append(rowLineIdx, i)
			}
		} else {
			// Render line — wrapping or truncating based on mode
			visualRows := render.RenderLineWrapped(l, m.width, isCursor, m.wrapMode, m.rStyles)
			for rowIdx := skipRows; rowIdx < len(visualRows); rowIdx++ {
				if len(rows) >= vh {
					break
				}
				rows = append(rows, clamp.Render(visualRows[rowIdx]))
				rowLineIdx = append(rowLineIdx, i)
			}
		}
	}

	// Pad empty viewport rows so the minimap aligns against a full column.
	for len(rows) < vh {
		rows = append(rows, "")
		rowLineIdx = append(rowLineIdx, -1)
	}

	// Overlay minimap on the right edge (content is clipped to make room).
	if m.showMinimap && m.s.store.Len() > 0 && m.width >= minimapMinTermWidth {
		rows = m.overlayMinimapRows(rows, rowLineIdx, vh)
	}

	// Pattern computation runs over the visible-line window we just
	// rendered. The result is shared between the row-highlight pass below
	// and the pane render — avoids walking masking twice per tick.
	var pats []pattern.Pattern
	var patStoreIdx []int
	var matched map[int]bool
	if m.patternsVisible {
		pats, patStoreIdx = m.visiblePatterns()
		if m.patternsFocused {
			matched = m.matchedLineIndices(pats, patStoreIdx)
		}
	}
	if len(matched) > 0 {
		// Width (not MaxWidth) so the background extends across the full
		// terminal width — otherwise only the visible characters get the
		// highlight color and short log lines look almost unchanged.
		hl := m.styles.PatternMatch.Width(m.width)
		for i := range rows {
			if matched[rowLineIdx[i]] {
				rows[i] = hl.Render(rows[i])
			}
		}
	}

	for _, row := range rows {
		sb.WriteString(row)
		sb.WriteByte('\n')
	}

	// Stats container (renders as multiple rows already terminated with \n
	// internally except the final row, which we terminate here).
	if pane := m.renderStatsPane(); pane != "" {
		sb.WriteString(pane)
		sb.WriteByte('\n')
	}

	// Patterns container — same wire-up as stats. Built from the pats
	// slice we already computed above so the masking happens once.
	if m.patternsVisible {
		if pane := m.renderPatternsPane(pats); pane != "" {
			sb.WriteString(pane)
			sb.WriteByte('\n')
		}
	}

	// Search bar
	if m.searchMode {
		searchBar := m.styles.SearchBar.Width(m.width).Render("/" + m.searchInput.View())
		sb.WriteString(searchBar)
		sb.WriteByte('\n')
	}

	// Status bar
	sb.WriteString(m.renderStatusBar())

	full := sb.String()

	// Modal overlay: lipgloss.Place centers the modal box on top of the
	// existing viewport. The underlying frame still renders so the user
	// keeps spatial context.
	if modal := m.renderModal(); modal != "" {
		full = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	return full
}

// overlayMinimapRows draws a braille minimap over the right edge of `rows`.
// rowLineIdx[i] is the source line index for row i (-1 for padding rows); it's
// used to compute which minimap rows correspond to currently visible log lines
// and highlight them as the viewport indicator.
//
// Runs under m.s.mu.RLock() held by View(). Reads the pre-populated
// minimapExtents slice maintained by the ingestor — the View path never calls
// store.Get() for the minimap, so offloaded chunks stay on disk and high-rate
// streams don't stall on I/O.
//
// The minimap is bounded to the most recent minimapWindowLines entries, so
// its cost is O(window) regardless of total log size. A full-log view would
// hold the shared read lock for tens of ms on long streams and lock the
// ingestor out of its write lock, tanking throughput.
func (m model) overlayMinimapRows(rows []string, rowLineIdx []int, vh int) []string {
	total := m.s.store.Len()
	if total == 0 || vh <= 0 {
		return rows
	}

	windowStart := total - minimapWindowLines
	if windowStart < 0 {
		windowStart = 0
	}
	windowLen := total - windowStart

	extents := m.s.minimapExtents
	if len(extents) > total {
		extents = extents[:total]
	}
	if windowStart < len(extents) {
		extents = extents[windowStart:]
	} else {
		extents = nil
	}
	maxCol := m.s.minimapMaxCol
	if maxCol < 1 {
		maxCol = 1
	}

	mapW := minimapContentWidth
	// Leave a minimum of 20 content columns before the minimap — if the
	// terminal is too narrow after reserving space, skip the overlay.
	if m.width-mapW-minimapSeparatorWidth < 20 {
		return rows
	}

	mapRows, mapStatuses := buildMinimapRows(extents, vh, mapW, maxCol, windowLen)

	// Viewport indicator: translate visible line indices into window-relative
	// positions. If the viewport is entirely outside the window (user scrolled
	// back past the minimap's range), no highlight is drawn — the silhouette
	// alone still tells the story of recent activity, which is the point.
	visMin, visMax := -1, -1
	for _, idx := range rowLineIdx {
		if idx < windowStart {
			continue
		}
		rel := idx - windowStart
		if visMin < 0 || rel < visMin {
			visMin = rel
		}
		if rel > visMax {
			visMax = rel
		}
	}
	viewStart, viewEnd := 0, 0
	if visMin >= 0 && windowLen > 0 {
		vscale := float64(vh*4) / float64(windowLen)
		if vscale > 1.0 {
			vscale = 1.0
		}
		viewStart = int(float64(visMin)*vscale) / 4
		viewEnd = int(float64(visMax)*vscale)/4 + 1
		if viewStart < 0 {
			viewStart = 0
		}
		if viewEnd > vh {
			viewEnd = vh
		}
	}

	// rowStyles[cursor][status]: the cursor (viewport indicator) row keeps
	// its background highlight, while non-neutral rows tint the foreground
	// red/green so successes and failures pop against the dim silhouette.
	const (
		neutralFG = lipgloss.Color("240")
		successFG = lipgloss.Color("42")
		failureFG = lipgloss.Color("196")
		cursorBG  = lipgloss.Color("237")
		cursorFG  = lipgloss.Color("252")
	)
	base := lipgloss.NewStyle()
	cursorBase := base.Background(cursorBG)
	rowStyles := [2][3]lipgloss.Style{
		{
			base.Foreground(neutralFG),
			base.Foreground(successFG),
			base.Foreground(failureFG),
		},
		{
			cursorBase.Foreground(cursorFG),
			cursorBase.Foreground(successFG),
			cursorBase.Foreground(failureFG),
		},
	}
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	return overlayMinimap(rows, mapRows, mapStatuses, m.width, mapW,
		viewStart, viewEnd, rowStyles, sepStyle)
}

func (m model) renderStatusBar() string {
	w := m.width
	if w <= 0 {
		w = 80
	}

	// Left: [loglens] lines cursor
	left := m.styles.StatusBar.Render(fmt.Sprintf(" [loglens] %d lines  L%d ",
		m.store.Len(), m.cursor+1))

	// Center: follow indicator, wrap indicator, EOF, exit code
	var centerParts []string
	// Toast: short-lived informational message takes priority in the
	// center segment for visibility. Time-based expiration so View doesn't
	// need to mutate state.
	if m.statusMsg != "" && time.Now().Before(m.statusMsgUntil) {
		centerParts = append(centerParts,
			m.styles.StatusEOF.Render(" "+m.statusMsg+" "))
	}
	if m.follow {
		centerParts = append(centerParts, m.styles.StatusFollow.Render(" FOLLOW "))
	}
	if m.wrapMode {
		centerParts = append(centerParts, m.styles.StatusFollow.Render(" WRAP "))
	}
	if m.showMinimap {
		centerParts = append(centerParts, m.styles.StatusFollow.Render(" MAP "))
	}
	if m.patternsVisible {
		centerParts = append(centerParts, m.styles.StatusFollow.Render(" PAT "))
	}
	if diskUsed := m.store.DiskUsed(); diskUsed > 0 {
		centerParts = append(centerParts, m.styles.StatusEOF.Render(fmt.Sprintf(" DISK:%s ", formatBytes(diskUsed))))
	}
	if m.eof {
		centerParts = append(centerParts, m.styles.StatusEOF.Render(" [EOF] "))
		if m.exitCode >= 0 {
			if m.exitCode == 0 {
				centerParts = append(centerParts, m.styles.StatusExitOK.Render(fmt.Sprintf(" [exit %d] ", m.exitCode)))
			} else {
				centerParts = append(centerParts, m.styles.StatusExitFail.Render(fmt.Sprintf(" [exit %d] ", m.exitCode)))
			}
		}
		if m.exitOnEOF {
			if m.follow && !m.eofCountdownStart.IsZero() {
				remaining := 5*time.Second - time.Since(m.eofCountdownStart)
				secs := int(remaining/time.Second) + 1
				if secs < 1 {
					secs = 1
				}
				if secs > 5 {
					secs = 5
				}
				centerParts = append(centerParts, m.styles.StatusExitFail.Render(fmt.Sprintf(" closing in %ds ", secs)))
			} else if !m.follow {
				centerParts = append(centerParts, m.styles.StatusExitFail.Render(" press q to quit (follow off) "))
			}
		}
	}
	center := strings.Join(centerParts, "")

	// Right: help
	right := m.styles.StatusBarKey.Render(" q:quit │ /:search │ p:patterns │ arrows:navigate ")

	// Pad center
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	centerW := lipgloss.Width(center)
	gap := w - leftW - rightW - centerW
	if gap < 0 {
		gap = 0
	}

	statusBar := left +
		strings.Repeat(" ", gap/2) +
		center +
		strings.Repeat(" ", gap-gap/2) +
		right

	// Ensure full width with background
	return m.styles.StatusBar.Width(w).Render(statusBar)
}

// formatBytes returns a human-readable byte size (e.g. "320MB", "1.2GB").
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%dMB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%dKB", b/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// getChildAtPath returns the LogLine child at the given path within root.
func getChildAtPath(root *line.LogLine, path []int) *line.LogLine {
	current := root
	for _, idx := range path {
		if current.Children == nil || idx < 0 || idx >= len(current.Children) {
			return nil
		}
		current = current.Children[idx]
	}
	return current
}

// nextVisiblePath returns the path to the next visible entry in DFS pre-order.
// Returns nil if at the end of the tree.
func nextVisiblePath(root *line.LogLine, path []int) []int {
	// If current node is expanded and has children, go to first child
	node := getChildAtPath(root, path)
	if node != nil && node.Expanded && node.Children != nil && len(node.Children) > 0 {
		return append(append([]int{}, path...), 0)
	}

	// Try next sibling, walking up ancestors if needed
	p := append([]int{}, path...)
	for len(p) > 0 {
		p[len(p)-1]++
		// Get the parent to check sibling count
		var parent *line.LogLine
		if len(p) == 1 {
			parent = root
		} else {
			parent = getChildAtPath(root, p[:len(p)-1])
		}
		if parent != nil && parent.Children != nil && p[len(p)-1] < len(parent.Children) {
			return p
		}
		// No more siblings at this level, go up
		p = p[:len(p)-1]
	}

	// Reached end of tree
	return nil
}

// nextExpandablePath finds the next expandable node after the given path,
// walking forward through visible nodes in DFS order. Returns nil if none found.
func nextExpandablePath(root *line.LogLine, path []int) []int {
	p := append([]int{}, path...)
	for {
		p = nextVisiblePath(root, p)
		if p == nil {
			return nil
		}
		node := getChildAtPath(root, p)
		if node != nil && node.Expandable {
			return p
		}
	}
}

// prevVisiblePath returns the path to the previous visible entry in DFS pre-order.
// Returns nil if at the beginning of the tree (should go to parent line).
func prevVisiblePath(root *line.LogLine, path []int) []int {
	if len(path) == 0 {
		return nil
	}

	lastIdx := path[len(path)-1]
	if lastIdx == 0 {
		// At first sibling: go to parent
		if len(path) == 1 {
			return nil // Exit tree to parent line
		}
		return append([]int{}, path[:len(path)-1]...)
	}

	// Go to previous sibling, then descend to its last visible descendant
	p := append([]int{}, path...)
	p[len(p)-1]--

	for {
		node := getChildAtPath(root, p)
		if node == nil || !node.Expanded || node.Children == nil || len(node.Children) == 0 {
			return p
		}
		p = append(p, len(node.Children)-1)
	}
}

// lastVisiblePath returns the path to the last visible descendant in an expanded tree.
// It descends through the last child of each expanded node.
func lastVisiblePath(root *line.LogLine) []int {
	if !root.Expanded || root.Children == nil || len(root.Children) == 0 {
		return nil
	}
	path := []int{len(root.Children) - 1}
	node := root.Children[len(root.Children)-1]
	for node.Expanded && node.Children != nil && len(node.Children) > 0 {
		path = append(path, len(node.Children)-1)
		node = node.Children[len(node.Children)-1]
	}
	return path
}

// collapseAll recursively collapses a node and all its descendants.
func collapseAll(l *line.LogLine) {
	l.Expanded = false
	for _, child := range l.Children {
		collapseAll(child)
	}
}

// expandAndPopulate expands a JSON line and lazily populates its children.
func expandAndPopulate(l *line.LogLine) {
	l.Expanded = true
	if l.Children == nil && l.Type == line.TypeJSON {
		if meta, ok := l.Meta.(*line.JSONMeta); ok {
			l.Children = render.BuildJSONChildren(meta.Value, l.Depth, meta.Keys, meta.RawJSON)
		}
	}
}

// expandAllDescendants recursively expands all expandable children.
func expandAllDescendants(l *line.LogLine) {
	for _, child := range l.Children {
		if child.Expandable {
			expandAndPopulate(child)
			expandAllDescendants(child)
		}
	}
}

// expandedTreeRows computes visible row count and (optionally) the cursor row
// index for an expanded LogLine tree — arithmetically, without rendering.
// Mirrors the measurement semantics of render.RenderExpanded but skips the
// string building, making it ~100× faster on deep trees.
//
// cursorPath interpretation:
//   - nil            → no cursor in this tree; cursorRow = -1
//   - []int{-1}      → cursor on the parent line itself
//   - []int{i, ...}  → cursor at child path
//
// Visible width is estimated from l.Raw length (exact for ASCII log content);
// for JSON children the estimate is refined using meta.RawJSON so long
// collapsed values don't under-count rows in wrap mode.
func (m *model) expandedTreeRows(l *line.LogLine, cursorPath []int) (total, cursorRow int) {
	cursorRow = -1
	cursorOnParent := len(cursorPath) == 1 && cursorPath[0] == -1
	// Parent row count: the parent line wraps when cursor is on it OR global
	// wrap is on; otherwise a single truncated row.
	if cursorOnParent || m.wrapMode {
		total = rowsForWidth(3+len(l.Raw), m.width)
	} else {
		total = 1
	}
	if cursorOnParent {
		cursorRow = 0
	}
	if !l.Expanded || l.Children == nil {
		return total, cursorRow
	}
	m.walkExpandedChildren(l, cursorPath, &total, &cursorRow)
	return total, cursorRow
}

// walkExpandedChildren recursively sums rows for each visible descendant,
// updating cursorRow when it matches path.
func (m *model) walkExpandedChildren(parent *line.LogLine, path []int, total, cursorRow *int) {
	for i, child := range parent.Children {
		onPath := len(path) > 0 && path[0] == i
		isCursor := onPath && len(path) == 1
		if isCursor {
			*cursorRow = *total
		}
		// Prefix visible width:
		//   non-expandable:  "  " + "  "*child.Depth  = 2 + 2*depth
		//   expandable:      " " + "  "*child.Depth + "▼ "  = 3 + 2*depth
		prefixW := 2 + 2*child.Depth
		if child.Expandable {
			prefixW = 3 + 2*child.Depth
		}
		contentW := prefixW + estimatedContentWidth(child)
		if isCursor || m.wrapMode {
			*total += rowsForWidth(contentW, m.width)
		} else {
			*total++
		}
		if child.Expanded && child.Children != nil {
			var sub []int
			if onPath && len(path) > 1 {
				sub = path[1:]
			}
			m.walkExpandedChildren(child, sub, total, cursorRow)
		}
	}
}

// estimatedContentWidth returns an approximation of the rendered visible width
// of c.Raw. For JSON children the rendered form is `"key": <value>`, and the
// raw value length (`meta.RawJSON`) reconstructs that width accurately — more
// so than len(c.Raw) which caps the summary at 50 chars.
func estimatedContentWidth(c *line.LogLine) int {
	if c.Type != line.TypeJSON {
		return len(c.Raw)
	}
	meta, ok := c.Meta.(*line.JSONMeta)
	if !ok || meta.RawJSON == nil {
		return len(c.Raw)
	}
	// Find "key: " prefix length in c.Raw.
	colonIdx := indexColonSpace(c.Raw)
	if colonIdx <= 0 {
		return len(c.Raw)
	}
	if c.Expanded {
		// Rendered form: `"key": <struct-indicator>` — tight upper bound at ~6 chars.
		est := colonIdx + 2 + 6
		if est < len(c.Raw) {
			return est
		}
		return len(c.Raw)
	}
	// Collapsed: `"key": <full raw JSON>` ≈ colonIdx + 2 + len(raw).
	est := colonIdx + 2 + len(meta.RawJSON)
	if est < len(c.Raw) {
		return len(c.Raw)
	}
	return est
}

// indexColonSpace returns the index of the first ": " in s, or -1.
func indexColonSpace(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] == ' ' {
			return i
		}
	}
	return -1
}

// rowsForWidth returns how many rows of the given width content of length
// contentLen occupies — i.e. ceil(contentLen / width), or 1 if it fits.
func rowsForWidth(contentLen, width int) int {
	if width <= 0 || contentLen <= width {
		return 1
	}
	return (contentLen + width - 1) / width
}

// cursorRowInTree returns the 0-based visual row offset of the cursor
// position within an expanded tree (row 0 = parent line).
func cursorRowInTree(root *line.LogLine, path []int) int {
	if len(path) == 0 {
		return 0
	}
	row := 1 // Start after parent line
	node := root
	for level, targetIdx := range path {
		_ = level
		// Count rows for siblings before the target
		for i := 0; i < targetIdx; i++ {
			row++ // The sibling itself
			if node.Children[i].Expanded && node.Children[i].Children != nil {
				row += countVisibleDescendants(node.Children[i])
			}
		}
		if targetIdx < len(path)-1 {
			row++ // The target node's own line (we're going deeper)
		}
		node = node.Children[targetIdx]
	}
	return row
}

// countVisibleDescendants counts how many visible rows a node's descendants occupy
// (not counting the node itself).
func countVisibleDescendants(l *line.LogLine) int {
	if !l.Expanded || l.Children == nil {
		return 0
	}
	count := len(l.Children)
	for _, child := range l.Children {
		count += countVisibleDescendants(child)
	}
	return count
}

// totalVisibleRows returns the total visible rows for a line, including expanded children.
func totalVisibleRows(l *line.LogLine) int {
	if l.Expanded && l.Children != nil {
		return 1 + countVisibleDescendants(l)
	}
	return 1
}
