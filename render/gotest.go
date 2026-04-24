package render

import (
	"loglens/line"
	"regexp"
	"strings"
)

var (
	goTestMarkerRenderRe = regexp.MustCompile(`^(\s*)(===\s+(?:RUN|PAUSE|CONT|NAME))\s+(.+)$`)
	goTestResultRenderRe = regexp.MustCompile(`^(\s*)(---\s+(?:PASS|FAIL|SKIP):)\s+(.+?)\s+(\(.+?\))$`)
)

func renderGoTest(l *line.LogLine, styles *Styles) string {
	meta, ok := l.Meta.(*line.GoTestMeta)
	if !ok {
		return l.Raw
	}

	// Bare PASS/FAIL
	trimmed := strings.TrimSpace(l.Raw)
	if trimmed == "PASS" {
		return styles.GoTestPass.Render(l.Raw)
	}
	if trimmed == "FAIL" {
		return styles.GoTestFail.Render(l.Raw)
	}

	// Package summary lines
	if strings.HasPrefix(l.Raw, "ok ") || strings.HasPrefix(l.Raw, "FAIL\t") || strings.HasPrefix(l.Raw, "FAIL ") {
		if meta.IsPass {
			return styles.GoTestPass.Render(l.Raw)
		}
		return styles.GoTestFail.Render(l.Raw)
	}

	// Marker: === RUN/PAUSE/CONT/NAME
	if m := goTestMarkerRenderRe.FindStringSubmatch(l.Raw); m != nil {
		indent := m[1]
		marker := m[2]
		name := m[3]
		return indent + styles.GoTestRun.Render(marker) + " " + name
	}

	// Result: --- PASS/FAIL/SKIP: TestName (duration)
	if m := goTestResultRenderRe.FindStringSubmatch(l.Raw); m != nil {
		indent := m[1]
		prefix := m[2]
		name := m[3]
		duration := m[4]

		var styledPrefix string
		switch {
		case meta.IsPass:
			styledPrefix = styles.GoTestPass.Render(prefix)
		case meta.IsFail:
			styledPrefix = styles.GoTestFail.Render(prefix)
		case meta.IsSkip:
			styledPrefix = styles.GoTestSkip.Render(prefix)
		default:
			styledPrefix = prefix
		}

		return indent + styledPrefix + " " + name + " " + styles.GoTestDuration.Render(duration)
	}

	return l.Raw
}
