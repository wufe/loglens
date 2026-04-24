package line

// LineType classifies a log line.
type LineType int

const (
	TypePlain LineType = iota
	TypeJSON
	TypeTable
	TypeDiff
	TypeGoTestMarker
	TypeGoTestResult
	TypeWarning
	TypeKubeResource
)

// Source indicates where a line originated.
type Source int

const (
	SourceStdin  Source = iota // pipe mode
	SourceStdout              // wrapper mode — child's stdout
	SourceStderr              // wrapper mode — child's stderr
)

// LogLine represents a single line of log output with parsed metadata.
type LogLine struct {
	Raw         string
	Type        LineType
	Segments    []Segment
	Expandable  bool
	Expanded    bool
	Children []*LogLine
	Depth    int
	GroupID     int
	GroupHead   bool
	FromStderr  bool
	Meta        interface{}
}

// Segment represents a styled span within a single line.
type Segment struct {
	Text  string
	Style string // style key referencing styles definitions
}

// JSONMeta holds parsed JSON metadata.
type JSONMeta struct {
	Value    interface{}
	Summary  string
	Keys     []string // ordered keys for objects (preserves original order)
	RawJSON  []byte   // original JSON bytes (for preserving nested key order)
	Prefix   string   // non-empty for multiline JSON with text before the opening brace
}

// GoTestMeta holds parsed Go test metadata.
type GoTestMeta struct {
	Action   string // RUN, PAUSE, CONT, NAME, PASS, FAIL, SKIP
	TestName string
	Duration string
	IsPass   bool
	IsFail   bool
	IsSkip   bool
}

// DiffMeta holds diff line metadata.
type DiffMeta struct {
	LineKind string // header-old, header-new, hunk, add, remove, context
}

// TabGap marks a single tab's expansion in an expanded line. Start is the
// first column of the expansion; End is the column where non-whitespace
// content resumes. Used to carry authoritative column-separator positions
// from the parser to the renderer when tabs were the original delimiter.
type TabGap struct {
	Start int
	End   int
}

// TableGroupState is shared across every row in a tab-delimited table group.
// ColWidths[i] is the maximum content width observed for cell i across all
// rows fed so far; the renderer pads each cell to that width so columns
// align vertically even when individual fields vary in length. Lives on the
// parser's TableTracker and is pointed to by each row's TableMeta, so new
// rows that widen a column automatically reflow every previously-rendered
// row on the next View() tick.
type TableGroupState struct {
	ColWidths []int
}

// TableMeta holds table column metadata.
type TableMeta struct {
	Columns    []int             // column start positions
	IsHeader   bool
	TabGaps    []TabGap          // authoritative gap regions when the source used tabs; nil when detected purely from multi-space gaps
	GroupState *TableGroupState  // shared max-width tracker for tab-delimited groups; nil for legacy (space-gap) tables
}

// WarningMeta holds warning/error prefix metadata.
type WarningMeta struct {
	Level string // Warning, Error, FATAL, INFO, DEBUG
}

// LineStub holds lightweight per-line metadata that stays in memory even when
// the full LogLine is offloaded to disk. Enough for isHiddenGroupMember,
// visualRowsForLine fast path, and hot-zone decisions.
type LineStub struct {
	RawLen      int
	Type        LineType
	GroupID     int
	GroupHead   bool
	Expandable  bool
	Expanded    bool
	FromStderr  bool
	HasSegments bool // len(Segments) > 0
	HasChildren bool // Children != nil && len(Children) > 0
}

// MakeStub creates a LineStub from a LogLine's current state.
func MakeStub(l *LogLine) LineStub {
	return LineStub{
		RawLen:      len(l.Raw),
		Type:        l.Type,
		GroupID:     l.GroupID,
		GroupHead:   l.GroupHead,
		Expandable:  l.Expandable,
		Expanded:    l.Expanded,
		FromStderr:  l.FromStderr,
		HasSegments: len(l.Segments) > 0,
		HasChildren: l.Children != nil && len(l.Children) > 0,
	}
}
