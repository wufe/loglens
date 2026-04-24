package input

import (
	"bufio"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// WrapperSource spawns a subprocess and reads its stdout/stderr.
type WrapperSource struct {
	ch       chan RawLine
	cmd      *exec.Cmd
	exitCode int
	mu       sync.Mutex
	done     chan struct{}
}

// NewWrapperSource spawns the given command and captures its output.
func NewWrapperSource(args []string) (*WrapperSource, error) {
	cmd := exec.Command(args[0], args[1:]...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	w := &WrapperSource{
		// See PipeSource: large buffer so the wrapped command never blocks on
		// its stdout write even if the ingestor briefly falls behind.
		ch:       make(chan RawLine, 100000),
		cmd:      cmd,
		exitCode: -1,
		done:     make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			w.ch <- RawLine{Text: scanner.Text(), Source: SourceStdout}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			w.ch <- RawLine{Text: scanner.Text(), Source: SourceStderr}
		}
	}()

	go func() {
		wg.Wait()
		_ = cmd.Wait()
		w.mu.Lock()
		if cmd.ProcessState != nil {
			w.exitCode = cmd.ProcessState.ExitCode()
		}
		w.mu.Unlock()
		close(w.ch)
		close(w.done)
	}()

	return w, nil
}

func (w *WrapperSource) Lines() <-chan RawLine {
	return w.ch
}

func (w *WrapperSource) Stop() {
	if w.cmd.Process == nil {
		return
	}
	_ = w.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
		return
	case <-time.After(2 * time.Second):
		_ = w.cmd.Process.Kill()
	}
}

func (w *WrapperSource) ExitCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exitCode
}
