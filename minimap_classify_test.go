package main

import (
	"loglens/line"
	"testing"
)

func TestClassifyJSONLevelError(t *testing.T) {
	l := &line.LogLine{
		Type: line.TypeJSON,
		Meta: &line.JSONMeta{Level: "error"},
	}
	if got := classifyMinimapStatus(l); got != statusFailure {
		t.Errorf("error JSON: got %d, want statusFailure (%d)", got, statusFailure)
	}
}

func TestClassifyJSONLevelFatal(t *testing.T) {
	l := &line.LogLine{
		Type: line.TypeJSON,
		Meta: &line.JSONMeta{Level: "fatal"},
	}
	if got := classifyMinimapStatus(l); got != statusFailure {
		t.Errorf("fatal JSON: got %d, want statusFailure", got)
	}
}

func TestClassifyJSONLevelWarnSuppressed(t *testing.T) {
	// Even though the embedded JSON path emits a level-warn segment for the
	// value, JSON warn entries must not surface in the minimap.
	l := &line.LogLine{
		Type: line.TypeJSON,
		Meta: &line.JSONMeta{Level: "warn"},
		Segments: []line.Segment{
			{Text: "prefix ", Style: "plain"},
			{Text: `{"level":`, Style: "json"},
			{Text: `"warn"`, Style: "level-warn"},
			{Text: `,"msg":"x"}`, Style: "json"},
		},
	}
	if got := classifyMinimapStatus(l); got != statusNeutral {
		t.Errorf("warn JSON: got %d, want statusNeutral", got)
	}
}

func TestClassifyJSONLevelInfoSuppressed(t *testing.T) {
	l := &line.LogLine{
		Type: line.TypeJSON,
		Meta: &line.JSONMeta{Level: "info"},
	}
	if got := classifyMinimapStatus(l); got != statusNeutral {
		t.Errorf("info JSON: got %d, want statusNeutral", got)
	}
}

func TestClassifyPlainLevelWarnStillFlags(t *testing.T) {
	// nginx [warn] / klog W lines stay failure-class in the minimap; the
	// suppression only applies to TypeJSON.
	l := &line.LogLine{
		Type: line.TypePlain,
		Segments: []line.Segment{
			{Text: "[warn]", Style: "level-warn"},
		},
	}
	if got := classifyMinimapStatus(l); got != statusFailure {
		t.Errorf("plain [warn]: got %d, want statusFailure", got)
	}
}
