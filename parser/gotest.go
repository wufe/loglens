package parser

import (
	"github.com/wufe/loglens/line"
	"regexp"
	"strings"
)

var (
	goTestMarkerRe = regexp.MustCompile(`^(\s*)===\s+(RUN|PAUSE|CONT|NAME)\s+(.+)$`)
	goTestResultRe = regexp.MustCompile(`^(\s*)---\s+(PASS|FAIL|SKIP):\s+(.+?)\s+\((.+?)\)$`)
	goTestBareRe   = regexp.MustCompile(`^(PASS|FAIL)$`)
	goTestPkgOkRe  = regexp.MustCompile(`^ok\s+\S+\s+[\d.]+s`)
	goTestPkgFailRe = regexp.MustCompile(`^FAIL\s+\S+\s+[\d.]+s`)
)

// detectGoTest checks if a line is Go test output.
func detectGoTest(raw string) *line.LogLine {
	// Check markers: === RUN, === PAUSE, === CONT, === NAME
	if m := goTestMarkerRe.FindStringSubmatch(raw); m != nil {
		indent := m[1]
		action := m[2]
		testName := m[3]
		depth := len(indent) / 4

		return &line.LogLine{
			Raw:   raw,
			Type:  line.TypeGoTestMarker,
			Depth: depth,
			Meta: &line.GoTestMeta{
				Action:   action,
				TestName: testName,
			},
		}
	}

	// Check results: --- PASS, --- FAIL, --- SKIP
	if m := goTestResultRe.FindStringSubmatch(raw); m != nil {
		indent := m[1]
		action := m[2]
		testName := m[3]
		duration := m[4]
		depth := len(indent) / 4

		return &line.LogLine{
			Raw:   raw,
			Type:  line.TypeGoTestResult,
			Depth: depth,
			Meta: &line.GoTestMeta{
				Action:   action,
				TestName: testName,
				Duration: duration,
				IsPass:   action == "PASS",
				IsFail:   action == "FAIL",
				IsSkip:   action == "SKIP",
			},
		}
	}

	// Bare PASS/FAIL
	trimmed := strings.TrimSpace(raw)
	if goTestBareRe.MatchString(trimmed) {
		return &line.LogLine{
			Raw:  raw,
			Type: line.TypeGoTestResult,
			Meta: &line.GoTestMeta{
				Action: trimmed,
				IsPass: trimmed == "PASS",
				IsFail: trimmed == "FAIL",
			},
		}
	}

	// Package summary: ok  package/path  1.234s
	if goTestPkgOkRe.MatchString(raw) {
		return &line.LogLine{
			Raw:  raw,
			Type: line.TypeGoTestResult,
			Meta: &line.GoTestMeta{
				Action: "PASS",
				IsPass: true,
			},
		}
	}

	// Package failure: FAIL  package/path  1.234s
	if goTestPkgFailRe.MatchString(raw) {
		return &line.LogLine{
			Raw:  raw,
			Type: line.TypeGoTestResult,
			Meta: &line.GoTestMeta{
				Action: "FAIL",
				IsFail: true,
			},
		}
	}

	return nil
}
