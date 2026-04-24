package input

import (
	"bufio"
	"io"
	"os"
)

// PipeSource reads lines from stdin (or any reader).
type PipeSource struct {
	ch     chan RawLine
	reader io.Reader
}

// NewPipeSource creates a PipeSource reading from os.Stdin.
func NewPipeSource() *PipeSource {
	return NewPipeSourceFromReader(os.Stdin)
}

// NewPipeSourceFromReader creates a PipeSource reading from the given reader.
func NewPipeSourceFromReader(r io.Reader) *PipeSource {
	p := &PipeSource{
		// Large buffer: the ingestor goroutine drains this channel in parallel
		// with the UI, but bursts (e.g. a 100k-line file piped in) can out-run
		// the ingestor for a moment. Sizing the channel to absorb ~1s at 100k
		// lps means the producer (pipe writer) keeps making progress without
		// blocking on kernel pipe backpressure.
		ch:     make(chan RawLine, 100000),
		reader: r,
	}
	go p.read()
	return p
}

func (p *PipeSource) read() {
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		p.ch <- RawLine{Text: scanner.Text(), Source: SourceStdin}
	}
	close(p.ch)
}

func (p *PipeSource) Lines() <-chan RawLine {
	return p.ch
}

func (p *PipeSource) Stop() {}

func (p *PipeSource) ExitCode() int {
	return -1
}
