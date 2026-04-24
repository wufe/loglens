package input

import (
	"io"
	"testing"
	"time"
)

func TestPipeSourceReadsLines(t *testing.T) {
	r, w := io.Pipe()
	src := NewPipeSourceFromReader(r)

	go func() {
		w.Write([]byte("hello\nworld\n"))
		w.Close()
	}()

	var lines []RawLine
	for l := range src.Lines() {
		lines = append(lines, l)
	}

	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Text != "hello" {
		t.Errorf("line 0 = %q, want hello", lines[0].Text)
	}
	if lines[1].Text != "world" {
		t.Errorf("line 1 = %q, want world", lines[1].Text)
	}
	if lines[0].Source != SourceStdin {
		t.Errorf("source = %v, want SourceStdin", lines[0].Source)
	}
}

func TestPipeSourceEOF(t *testing.T) {
	r, w := io.Pipe()
	src := NewPipeSourceFromReader(r)
	w.Close()

	select {
	case _, ok := <-src.Lines():
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}

func TestPipeSourceExitCode(t *testing.T) {
	r, w := io.Pipe()
	src := NewPipeSourceFromReader(r)
	w.Close()

	if src.ExitCode() != -1 {
		t.Errorf("exit code = %d, want -1", src.ExitCode())
	}
}

func TestPipeSourceStop(t *testing.T) {
	r, w := io.Pipe()
	src := NewPipeSourceFromReader(r)
	// Stop should be a no-op
	src.Stop()
	w.Close()
}
