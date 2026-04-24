package parser

import (
	"loglens/line"
	"regexp"
	"strings"
)

// DiffState tracks the state machine for unified diff detection.
type DiffState int

const (
	DiffIdle DiffState = iota
	DiffHeader1
	DiffHeader2
	DiffBody
)

var (
	diffOldRe  = regexp.MustCompile(`---\s+\S+`)
	diffNewRe  = regexp.MustCompile(`^(\s*)\+\+\+\s+\S+`)
	diffHunkRe = regexp.MustCompile(`^(\s*)@@\s`)

	// Go test result pattern to avoid false positive
	goTestDashRe = regexp.MustCompile(`^(\s*)---\s+(PASS|FAIL|SKIP):`)
)

// DiffTracker tracks diff state across lines.
type DiffTracker struct {
	State      DiffState
	GroupID    int
	StartIndex int
	Indent     string // leading whitespace from the @@ hunk line
}

// NewDiffTracker creates a new diff state tracker.
func NewDiffTracker() *DiffTracker {
	return &DiffTracker{State: DiffIdle}
}

// Feed processes a line through the diff state machine.
// Returns the DiffMeta if the line is part of a diff block, nil otherwise.
func (dt *DiffTracker) Feed(raw string, lineIdx int, buf *Buffer) (*line.DiffMeta, int) {
	trimmed := raw

	switch dt.State {
	case DiffIdle:
		// Check for diff header start: --- file
		if diffOldRe.MatchString(trimmed) && !goTestDashRe.MatchString(trimmed) {
			dt.State = DiffHeader1
			dt.StartIndex = lineIdx
			dt.GroupID = buf.AllocGroupID()
			// Capture the indentation prefix from the --- line
			dt.Indent = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " \t"))]
			return &line.DiffMeta{LineKind: "header-old"}, dt.GroupID
		}

	case DiffHeader1:
		if diffNewRe.MatchString(trimmed) {
			dt.State = DiffHeader2
			return &line.DiffMeta{LineKind: "header-new"}, dt.GroupID
		}
		// Not a valid diff, reset
		dt.State = DiffIdle
		return nil, 0

	case DiffHeader2:
		if diffHunkRe.MatchString(trimmed) {
			dt.State = DiffBody
			dt.Indent = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " \t"))]
			return &line.DiffMeta{LineKind: "hunk"}, dt.GroupID
		}
		// Not a valid diff, reset
		dt.State = DiffIdle
		return nil, 0

	case DiffBody:
		// Check for new hunk header
		if diffHunkRe.MatchString(trimmed) {
			// Update indent from the @@ line — body lines share its indentation
			dt.Indent = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " \t"))]
			return &line.DiffMeta{LineKind: "hunk"}, dt.GroupID
		}

		// Diff body lines must start with the exact indent prefix.
		// Lines without the correct indent are not part of this diff.
		if dt.Indent != "" && !strings.HasPrefix(trimmed, dt.Indent) {
			// Not indented correctly — end the diff block
			dt.State = DiffIdle
			return nil, 0
		}

		// Strip the indentation prefix to find the diff marker.
		// The diff body lines have: [indent] [+/-/space] [content]
		stripped := trimmed
		if dt.Indent != "" {
			stripped = trimmed[len(dt.Indent):]
		}

		// Check the first character for diff markers
		if len(stripped) > 0 {
			firstChar := stripped[0]
			if firstChar == '+' {
				return &line.DiffMeta{LineKind: "add"}, dt.GroupID
			}
			if firstChar == '-' {
				return &line.DiffMeta{LineKind: "remove"}, dt.GroupID
			}
			if firstChar == ' ' {
				return &line.DiffMeta{LineKind: "context"}, dt.GroupID
			}
		}

		// Empty line within a diff block — treat as context
		if strings.TrimSpace(trimmed) == "" {
			return &line.DiffMeta{LineKind: "context"}, dt.GroupID
		}

		// Check for new diff block starting
		if diffOldRe.MatchString(trimmed) && !goTestDashRe.MatchString(trimmed) {
			dt.State = DiffHeader1
			dt.StartIndex = lineIdx
			dt.GroupID = buf.AllocGroupID()
			dt.Indent = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " \t"))]
			return &line.DiffMeta{LineKind: "header-old"}, dt.GroupID
		}
		// End of diff block
		dt.State = DiffIdle
		return nil, 0
	}

	return nil, 0
}

// Reset resets the diff tracker state.
func (dt *DiffTracker) Reset() {
	dt.State = DiffIdle
	dt.GroupID = 0
	dt.StartIndex = 0
	dt.Indent = ""
}

// InDiffBlock returns true if we're currently inside a confirmed diff block.
func (dt *DiffTracker) InDiffBlock() bool {
	return dt.State == DiffBody
}
