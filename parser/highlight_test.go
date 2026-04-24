package parser

import (
	"loglens/line"
	"testing"
)

func TestTimestamp(t *testing.T) {
	segments := highlightSegments("00:52:29 some log message")
	found := false
	for _, seg := range segments {
		if seg.Style == "timestamp" && seg.Text == "00:52:29" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected timestamp segment for 00:52:29")
	}
}

func TestDatetime(t *testing.T) {
	segments := highlightSegments("2026-04-11 00:39:46 +0200 CEST something happened")
	found := false
	for _, seg := range segments {
		if seg.Style == "datetime" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected datetime segment")
	}
}

func TestSourceFileRef(t *testing.T) {
	segments := highlightSegments("logger.go:42: some message")
	found := false
	for _, seg := range segments {
		if seg.Style == "sourceref" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected sourceref segment for logger.go:42")
	}
}

func TestK8sResource(t *testing.T) {
	segments := highlightSegments("customresourcedefinition.apiextensions.k8s.io/widgets.widget.example.io unchanged")
	found := false
	for _, seg := range segments {
		if seg.Style == "k8s" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected k8s segment")
	}
}

func TestWarningPrefix(t *testing.T) {
	p := New()
	result := p.Parse("Warning: something bad", false)
	if result.Line.Type != line.TypeWarning {
		t.Errorf("type = %v, want TypeWarning", result.Line.Type)
	}
}

func TestErrorPrefix(t *testing.T) {
	p := New()
	result := p.Parse("ERROR: something terrible", false)
	if result.Line.Type != line.TypeWarning {
		t.Errorf("type = %v, want TypeWarning", result.Line.Type)
	}
	meta := result.Line.Meta.(*line.WarningMeta)
	if meta.Level != "ERROR" {
		t.Errorf("level = %q, want ERROR", meta.Level)
	}
}

func TestMultipleHighlights(t *testing.T) {
	segments := highlightSegments("logger.go:42: 00:52:29 deployment.apps/my-deploy configured")
	styles := map[string]bool{}
	for _, seg := range segments {
		if seg.Style != "plain" {
			styles[seg.Style] = true
		}
	}
	if len(styles) < 2 {
		t.Errorf("expected at least 2 highlight styles, got %d: %v", len(styles), styles)
	}
}

func TestURLNotFileRef(t *testing.T) {
	segments := highlightSegments("http://localhost:8080/api")
	for _, seg := range segments {
		if seg.Style == "sourceref" {
			t.Error("URL port should not be detected as source file ref")
		}
	}
}
