# loglens

A terminal UI log viewer with real-time rich formatting. Pipe any command output through loglens and it automatically detects and highlights JSON, Go test results, diffs, Kubernetes resources, tables, timestamps, warnings, and more — with an interactive tree for navigating nested structures.

## Features

- **JSON detection** — inline and multiline JSON is parsed into a navigable tree. Expand/collapse objects and arrays with arrow keys.
- **Go test output** — `=== RUN`, `--- PASS/FAIL/SKIP` markers are highlighted with durations and pass/fail color coding.
- **Diff highlighting** — unified diff hunks get colored add/remove lines and hunk headers.
- **Kubernetes resources** — `kind/name` paths (e.g. `deployment.apps/my-deploy`) and CRD API groups are highlighted.
- **Table detection** — space-aligned and tab-separated tabular output (e.g. `kubectl get`) is rendered with aligned separators.
- **Timestamps and log levels** — ISO 8601 dates, `WARN`/`ERROR`/`INFO`/`DEBUG` prefixes, and source file references are all styled.
- **Follow mode** — auto-scrolls to new lines as they arrive, like `tail -f`.
- **Wrap mode** — toggle line wrapping for long lines (`w` key).
- **Search** — `/` to enter search mode; `n`/`N` to jump between matches.
- **Minimap** — VS Code-style braille minimap overlay showing your position in the stream (`m` to toggle).
- **Large file support** — disk-offloaded chunked store handles streams well beyond available RAM; O(log N) Fenwick-tree-backed scrolling stays fast at 100K+ lines.

## Installation

```bash
go install github.com/wufe/loglens@latest
```

## Usage

### Pipe mode

```bash
kubectl logs -f my-pod | loglens
some-command 2>&1 | loglens
loglens < file.log
```

### Wrapper mode (preferred — preserves stderr/stdout separation)

```bash
loglens -- kubectl logs -f my-pod
loglens -- go test ./... -v
loglens -- some-command arg1 arg2
```

### Options

```
--no-follow        Start with follow mode off (default: on)
-x, --exit-on-eof  Auto-exit 5s after EOF when in follow mode
--max-disk <size>  Max disk for offloaded chunks (e.g. 512M, 2G; default: 1G)
--bench <file>     Write timing metrics to <file> for perf testing
-h, --help         Show help
```

## Key bindings

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `→` | Expand JSON node / jump to next expandable |
| `←` | Collapse JSON node / exit tree |
| `g` | Jump to top |
| `G` | Jump to bottom (re-enables follow) |
| `f` | Toggle follow mode |
| `w` | Toggle line wrap |
| `m` | Toggle minimap |
| `/` | Enter search mode |
| `n` / `N` | Next / previous search match |
| `PageUp` / `PageDown` | Scroll by page |
| `q` / `Ctrl+C` | Quit |

## Benchmarking

The `cmd/loggen` tool emits a ramping log stream at a configurable rate, prefixed with lag-measurement timestamps. Use it with `--bench` to measure rendering performance:

```bash
cd cmd/loggen
go build -o loggen .

# Linear ramp from 100 to 200k lines/sec over 30s:
./loggen --start-rate 100 --end-rate 200000 --duration 30s | loglens --bench out.txt

# Exponential ramp:
./loggen --start-rate 100 --end-rate 500000 --duration 60s --ramp exp | loglens --bench out.txt

# Step function:
./loggen --ramp step --steps 1000,5000,20000,100000 --step-hold 10s | loglens --bench out.txt

# Generate S3-access-log-style JSON lines:
./loggen --shape s3json --start-rate 1000 --end-rate 50000 --duration 20s | loglens
```

`loggen` flags: `--start-rate`, `--end-rate`, `--duration`, `--ramp` (linear|exp|step), `--steps`, `--step-hold`, `--shape` (kuttl|s3json), `--shuffle`, `--no-prefix`, `--max-lines`, `--flush-every`, `--quiet`.

## Development

```bash
make test       # run all tests
make test-race  # run with race detector
make lint       # golangci-lint
make build      # build loglens binary
```

## License

MIT — see [LICENSE](LICENSE).