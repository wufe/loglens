package main

import (
	"fmt"
	"github.com/wufe/loglens/input"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// version is the build version. Overridable via -ldflags "-X main.version=...".
// When unset, falls back to module info from runtime/debug.
var version = ""

type invocationMode int

const (
	PipeMode invocationMode = iota
	WrapperMode
)

func main() {
	// Parse CLI flags
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Println(versionString())
			os.Exit(0)
		}
	}
	noFollow := false
	exitOnEOF := false
	benchPath := ""
	var maxDiskCap int64
	var wrapperArgs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printUsage()
			os.Exit(0)
		case "--no-follow":
			noFollow = true
		case "-x", "--exit-on-eof":
			exitOnEOF = true
		case "--bench":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--bench requires a file path argument")
				os.Exit(1)
			}
			benchPath = args[i+1]
			i++
		case "--max-disk":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--max-disk requires a size argument (e.g. 512M, 2G)")
				os.Exit(1)
			}
			cap, err := parseDiskSize(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "--max-disk: %v\n", err)
				os.Exit(1)
			}
			maxDiskCap = cap
			i++
		case "--":
			wrapperArgs = args[i+1:]
			i = len(args) // break
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
			printUsage()
			os.Exit(1)
		}
	}

	var bench *benchLogger
	if benchPath != "" {
		b, err := newBenchLogger(benchPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening bench file %q: %v\n", benchPath, err)
			os.Exit(1)
		}
		bench = b
		defer bench.close()
	}

	// Determine invocation mode
	mode := detectMode(wrapperArgs)

	var inputSrc input.InputSource
	switch mode {
	case WrapperMode:
		src, err := input.NewWrapperSource(wrapperArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting command: %v\n", err)
			os.Exit(1)
		}
		inputSrc = src

	case PipeMode:
		inputSrc = input.NewPipeSource()
	}

	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
	}

	// In pipe mode, stdin is the pipe, so we need /dev/tty for key input
	if mode == PipeMode {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot open /dev/tty for key input: %v\n", err)
			os.Exit(1)
		}
		defer tty.Close()
		opts = append(opts, tea.WithInput(tty))
	}

	m := initialModel(inputSrc, noFollow, bench, maxDiskCap)
	m.exitOnEOF = exitOnEOF

	// Ensure temp files are cleaned up on SIGINT/SIGTERM even if
	// the Bubble Tea program doesn't get a chance to run quit handlers.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		if m.ingestor != nil {
			m.ingestor.stop()
		}
		m.store.Close()
		os.Exit(130)
	}()

	p := tea.NewProgram(m, opts...)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func detectMode(wrapperArgs []string) invocationMode {
	if len(wrapperArgs) > 0 {
		return WrapperMode
	}

	// Check if stdin is a pipe
	stat, err := os.Stdin.Stat()
	if err != nil {
		printUsage()
		os.Exit(1)
	}

	if (stat.Mode() & os.ModeCharDevice) == 0 {
		return PipeMode
	}

	// Stdin is a terminal — no input source
	printUsage()
	os.Exit(0)
	return PipeMode // unreachable
}

func printUsage() {
	fmt.Println(`Usage:
  loglens -- <command> [args...]   Run command and view its output (preferred)
  <command> 2>&1 | loglens         Pipe output into loglens
  loglens < file.log               View a log file

Commands:
  version            Print version information and exit

Options:
  --no-follow        Start with follow mode off (default: on)
  -x, --exit-on-eof  Auto-exit 5s after EOF when in follow mode
  --max-disk <size>  Max disk for offloaded chunks (e.g. 512M, 2G; default: 1G)
  --bench <file>     Write timing metrics to <file> for perf testing
  -h, --help         Show this help`)
}

// versionString returns a human-readable build identifier. If `version` was
// set via -ldflags it wins; otherwise we read module + VCS info from the
// embedded build metadata (works for `go install ...@vX.Y.Z` builds).
func versionString() string {
	if version != "" {
		return "loglens " + version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "loglens (unknown)"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	// Strip any "+dirty" Go appends to Main.Version — we surface dirty state
	// via the VCS suffix instead, to avoid duplication.
	v = strings.TrimSuffix(v, "+dirty")
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if rev != "" {
		return fmt.Sprintf("loglens %s (%s%s)", v, rev, modified)
	}
	return "loglens " + v
}

// parseDiskSize parses a human-readable size like "512M", "2G", "1024" (bytes).
func parseDiskSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0, fmt.Errorf("empty size")
	}
	suffix := strings.ToUpper(s[len(s)-1:])
	var multiplier int64 = 1
	numStr := s
	switch suffix {
	case "K":
		multiplier = 1 << 10
		numStr = s[:len(s)-1]
	case "M":
		multiplier = 1 << 20
		numStr = s[:len(s)-1]
	case "G":
		multiplier = 1 << 30
		numStr = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	return n * multiplier, nil
}
