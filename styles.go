package main

import "github.com/charmbracelet/lipgloss"

// Styles holds all lipgloss styles for the application.
type Styles struct {
	CursorLine lipgloss.Style

	// JSON
	JSONKey    lipgloss.Style
	JSONString lipgloss.Style
	JSONNumber lipgloss.Style
	JSONBool   lipgloss.Style
	JSONNull   lipgloss.Style
	JSONBrace  lipgloss.Style

	// Diff
	DiffAdd    lipgloss.Style
	DiffRemove lipgloss.Style
	DiffHunk   lipgloss.Style
	DiffHeader lipgloss.Style

	// Go test
	GoTestPass     lipgloss.Style
	GoTestFail     lipgloss.Style
	GoTestSkip     lipgloss.Style
	GoTestRun      lipgloss.Style
	GoTestDuration lipgloss.Style

	// Warnings
	WarnPrefix  lipgloss.Style
	ErrorPrefix lipgloss.Style
	InfoPrefix  lipgloss.Style
	DebugPrefix lipgloss.Style

	// Inline highlights
	Timestamp  lipgloss.Style
	Datetime   lipgloss.Style
	SourceRef       lipgloss.Style
	K8sResource     lipgloss.Style
	K8sEventNormal  lipgloss.Style
	K8sEventWarning lipgloss.Style

	// Severity levels — used for nginx error_log brackets ([crit], [error], ...)
	// and klog single-letter prefixes (W0424, I0425, ...).
	LevelError lipgloss.Style
	LevelWarn  lipgloss.Style
	LevelInfo  lipgloss.Style
	LevelDebug lipgloss.Style

	// nginx field marker (client:, server:, upstream:, host:, ...)
	NginxField lipgloss.Style

	// IPv4 address
	IPAddr lipgloss.Style

	// Table
	TableHeader lipgloss.Style
	TableCell   lipgloss.Style
	TableSep    lipgloss.Style

	// Gutter
	StderrGutter lipgloss.Style

	// Expand indicators
	ExpandIndicator lipgloss.Style

	// Status bar
	StatusBar      lipgloss.Style
	StatusBarKey   lipgloss.Style
	StatusFollow   lipgloss.Style
	StatusEOF      lipgloss.Style
	StatusExitOK   lipgloss.Style
	StatusExitFail lipgloss.Style

	// Search
	SearchMatch lipgloss.Style
	SearchBar   lipgloss.Style

	// Plain
	Plain lipgloss.Style
}

// DefaultStyles returns the default dark color scheme.
func DefaultStyles() *Styles {
	return &Styles{
		CursorLine: lipgloss.NewStyle().Background(lipgloss.Color("236")),

		JSONKey:    lipgloss.NewStyle().Foreground(lipgloss.Color("86")),
		JSONString: lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		JSONNumber: lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		JSONBool:   lipgloss.NewStyle().Foreground(lipgloss.Color("170")),
		JSONNull:   lipgloss.NewStyle().Faint(true),
		JSONBrace:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")),

		DiffAdd:    lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		DiffRemove: lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		DiffHunk:   lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true),
		DiffHeader: lipgloss.NewStyle().Bold(true),

		GoTestPass:     lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true),
		GoTestFail:     lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		GoTestSkip:     lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		GoTestRun:      lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true),
		GoTestDuration: lipgloss.NewStyle().Faint(true),

		WarnPrefix:  lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		ErrorPrefix: lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		InfoPrefix:  lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true),
		DebugPrefix: lipgloss.NewStyle().Faint(true),

		Timestamp:   lipgloss.NewStyle().Faint(true).Italic(true),
		Datetime:    lipgloss.NewStyle().Faint(true).Italic(true),
		SourceRef:   lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Underline(true),
		K8sResource:     lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Underline(true),
		K8sEventNormal:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		K8sEventWarning: lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),

		LevelError: lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		LevelWarn:  lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		LevelInfo:  lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true),
		LevelDebug: lipgloss.NewStyle().Faint(true),

		NginxField: lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
		IPAddr:     lipgloss.NewStyle().Foreground(lipgloss.Color("141")),

		TableHeader: lipgloss.NewStyle().Bold(true),
		TableCell:   lipgloss.NewStyle(),
		TableSep:    lipgloss.NewStyle().Faint(true),

		StderrGutter: lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),

		ExpandIndicator: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),

		StatusBar:      lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255")),
		StatusBarKey:   lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("245")),
		StatusFollow:   lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("220")).Bold(true),
		StatusEOF:      lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("245")),
		StatusExitOK:   lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("42")).Bold(true),
		StatusExitFail: lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("196")).Bold(true),

		SearchMatch: lipgloss.NewStyle().Background(lipgloss.Color("220")).Foreground(lipgloss.Color("0")),
		SearchBar:   lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("255")),

		Plain: lipgloss.NewStyle(),
	}
}
