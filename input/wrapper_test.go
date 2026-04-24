package input

import (
	"testing"
	"time"
)

func TestWrapperStdout(t *testing.T) {
	src, err := NewWrapperSource([]string{"echo", "hello"})
	if err != nil {
		t.Fatal(err)
	}

	var lines []RawLine
	for l := range src.Lines() {
		lines = append(lines, l)
	}

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if lines[0].Text != "hello" {
		t.Errorf("text = %q, want hello", lines[0].Text)
	}
	if lines[0].Source != SourceStdout {
		t.Errorf("source = %v, want SourceStdout", lines[0].Source)
	}
}

func TestWrapperStderr(t *testing.T) {
	src, err := NewWrapperSource([]string{"sh", "-c", "echo err >&2"})
	if err != nil {
		t.Fatal(err)
	}

	var lines []RawLine
	for l := range src.Lines() {
		lines = append(lines, l)
	}

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if lines[0].Text != "err" {
		t.Errorf("text = %q, want err", lines[0].Text)
	}
	if lines[0].Source != SourceStderr {
		t.Errorf("source = %v, want SourceStderr", lines[0].Source)
	}
}

func TestWrapperBothStreams(t *testing.T) {
	src, err := NewWrapperSource([]string{"sh", "-c", "echo out; echo err >&2"})
	if err != nil {
		t.Fatal(err)
	}

	var lines []RawLine
	for l := range src.Lines() {
		lines = append(lines, l)
	}

	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	hasStdout := false
	hasStderr := false
	for _, l := range lines {
		if l.Source == SourceStdout {
			hasStdout = true
		}
		if l.Source == SourceStderr {
			hasStderr = true
		}
	}
	if !hasStdout || !hasStderr {
		t.Error("expected both stdout and stderr lines")
	}
}

func TestWrapperExitCodeSuccess(t *testing.T) {
	src, err := NewWrapperSource([]string{"true"})
	if err != nil {
		t.Fatal(err)
	}

	// Drain the channel
	for range src.Lines() {
	}

	// Small delay for cmd.Wait to complete
	time.Sleep(100 * time.Millisecond)

	if src.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", src.ExitCode())
	}
}

func TestWrapperExitCodeFailure(t *testing.T) {
	src, err := NewWrapperSource([]string{"false"})
	if err != nil {
		t.Fatal(err)
	}

	for range src.Lines() {
	}

	time.Sleep(100 * time.Millisecond)

	if src.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", src.ExitCode())
	}
}

func TestWrapperStop(t *testing.T) {
	src, err := NewWrapperSource([]string{"sleep", "60"})
	if err != nil {
		t.Fatal(err)
	}

	src.Stop()

	// Channel should close after stop
	select {
	case <-src.Lines():
		// OK, channel closed or drained
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for channel close after Stop")
	}
}
