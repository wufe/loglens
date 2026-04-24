// loggen emits a realistic log stream at a ramping rate for benchmarking
// loglens. Each emitted line is prefixed with `[LLB:<nano>] ` so loglens
// (invoked with --bench) can compute end-to-end lag.
//
// Usage:
//   loggen [flags]
//
// Typical usage:
//   loggen --start-rate 100 --end-rate 200000 --duration 30s --ramp exp \
//     | loglens --bench out.txt
package main

import (
	_ "embed"
	"bufio"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"time"
)

//go:embed template.log
var embeddedTemplate string

func main() {
	var (
		startRate   = flag.Float64("start-rate", 100, "initial lines per second")
		endRate     = flag.Float64("end-rate", 50000, "final lines per second")
		duration    = flag.Duration("duration", 30*time.Second, "ramp duration")
		ramp        = flag.String("ramp", "linear", "ramp shape: linear | exp | step")
		stepRates   = flag.String("steps", "", "comma-separated rates for --ramp step, e.g. 1000,5000,20000,100000")
		stepHold    = flag.Duration("step-hold", 5*time.Second, "hold time per rate level when --ramp step")
		shape       = flag.String("shape", "kuttl", "line shape: kuttl (embedded template) | s3json (synthetic DD/s3gw-style JSON)")
		templateArg = flag.String("template", "", "path to a template log file (overrides embedded; only used with --shape kuttl)")
		shuffle     = flag.Bool("shuffle", false, "shuffle template lines before use (only with --shape kuttl)")
		noPrefix    = flag.Bool("no-prefix", false, "disable the [LLB:<nano>] lag-measurement prefix")
		maxLines    = flag.Int64("max-lines", 0, "stop after emitting N lines (0 = unbounded)")
		flushEvery  = flag.Int("flush-every", 256, "flush stdout every N lines (0 = line buffered)")
		silentStats = flag.Bool("quiet", false, "suppress end-of-run summary on stderr")
	)
	flag.Usage = usage
	flag.Parse()

	var nextLine func() string
	switch *shape {
	case "kuttl":
		tmpl, err := loadTemplate(*templateArg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "loggen:", err)
			os.Exit(1)
		}
		if len(tmpl) == 0 {
			fmt.Fprintln(os.Stderr, "loggen: template has no lines")
			os.Exit(1)
		}
		if *shuffle {
			rand.Shuffle(len(tmpl), func(i, j int) { tmpl[i], tmpl[j] = tmpl[j], tmpl[i] })
		}
		idx := 0
		nextLine = func() string {
			s := tmpl[idx]
			idx++
			if idx >= len(tmpl) {
				idx = 0
			}
			return s
		}
	case "s3json":
		nextLine = buildS3JSONLine
	default:
		fmt.Fprintf(os.Stderr, "loggen: unknown shape %q (want kuttl|s3json)\n", *shape)
		os.Exit(1)
	}

	var rateFn func(t time.Duration) float64
	switch *ramp {
	case "linear":
		rateFn = linearRate(*startRate, *endRate, *duration)
	case "exp", "exponential":
		rateFn = expRate(*startRate, *endRate, *duration)
	case "step":
		levels, err := parseRates(*stepRates)
		if err != nil {
			fmt.Fprintln(os.Stderr, "loggen: --steps:", err)
			os.Exit(1)
		}
		if len(levels) == 0 {
			fmt.Fprintln(os.Stderr, "loggen: --ramp step requires --steps")
			os.Exit(1)
		}
		rateFn = stepRate(levels, *stepHold)
		// Total duration is implicitly len(levels) * stepHold.
		*duration = time.Duration(len(levels)) * *stepHold
	default:
		fmt.Fprintf(os.Stderr, "loggen: unknown ramp %q (want linear|exp|step)\n", *ramp)
		os.Exit(1)
	}

	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer w.Flush()

	start := time.Now()
	var emitted int64

	// We compute the cumulative expected count as the integral of rate(t) from
	// 0..t, then emit as many lines as needed to catch up. This gives stable
	// average throughput even when the OS scheduler is bursty.
	var integral float64
	lastT := time.Duration(0)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

loop:
	for {
		now := time.Now()
		elapsed := now.Sub(start)
		if elapsed >= *duration {
			break
		}

		// Trapezoidal integration over [lastT, elapsed].
		dt := elapsed - lastT
		if dt > 0 {
			r1 := rateFn(lastT)
			r2 := rateFn(elapsed)
			integral += 0.5 * (r1 + r2) * dt.Seconds()
			lastT = elapsed
		}
		target := int64(integral)

		for emitted < target {
			line := nextLine()
			if *noPrefix {
				if _, err := w.WriteString(line); err != nil {
					break loop
				}
			} else {
				if _, err := fmt.Fprintf(w, "[LLB:%d] %s", time.Now().UnixNano(), line); err != nil {
					break loop
				}
			}
			if err := w.WriteByte('\n'); err != nil {
				break loop
			}
			emitted++
			if *flushEvery > 0 && int(emitted)%*flushEvery == 0 {
				if err := w.Flush(); err != nil {
					break loop
				}
			}
			if *maxLines > 0 && emitted >= *maxLines {
				break loop
			}
		}
		<-tick.C
	}

	_ = w.Flush()

	if !*silentStats {
		wall := time.Since(start)
		avg := float64(emitted) / wall.Seconds()
		fmt.Fprintf(os.Stderr,
			"loggen: emitted=%d wall=%.3fs avg_rate=%.0f lines/sec start=%.0f end=%.0f ramp=%s\n",
			emitted, wall.Seconds(), avg, *startRate, *endRate, *ramp)
	}
}

func loadTemplate(path string) ([]string, error) {
	src := embeddedTemplate
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading template %q: %w", path, err)
		}
		src = string(b)
	}
	// Split keeping line content without trailing newline.
	lines := strings.Split(src, "\n")
	// Drop trailing empty entry produced by a trailing newline, but keep
	// blank lines that are genuinely in the middle of the file.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func linearRate(start, end float64, dur time.Duration) func(time.Duration) float64 {
	d := dur.Seconds()
	if d <= 0 {
		return func(time.Duration) float64 { return end }
	}
	return func(t time.Duration) float64 {
		u := t.Seconds() / d
		if u < 0 {
			u = 0
		} else if u > 1 {
			u = 1
		}
		return start + (end-start)*u
	}
}

func expRate(start, end float64, dur time.Duration) func(time.Duration) float64 {
	d := dur.Seconds()
	if d <= 0 || start <= 0 || end <= 0 {
		return linearRate(start, end, dur)
	}
	ratio := end / start
	logR := math.Log(ratio)
	return func(t time.Duration) float64 {
		u := t.Seconds() / d
		if u < 0 {
			u = 0
		} else if u > 1 {
			u = 1
		}
		return start * math.Exp(logR*u)
	}
}

func stepRate(levels []float64, hold time.Duration) func(time.Duration) float64 {
	return func(t time.Duration) float64 {
		idx := int(t / hold)
		if idx < 0 {
			idx = 0
		} else if idx >= len(levels) {
			idx = len(levels) - 1
		}
		return levels[idx]
	}
}

func parseRates(s string) ([]float64, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(p, "%f", &v); err != nil {
			return nil, fmt.Errorf("invalid rate %q", p)
		}
		if v <= 0 {
			return nil, fmt.Errorf("rate must be positive: %q", p)
		}
		out = append(out, v)
	}
	return out, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `loggen — emit a ramping log stream for benchmarking loglens.

Flags:`)
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, `
Examples:
  # Linear ramp 100 -> 200k lines/sec over 30s, pipe into loglens:
  loggen --start-rate 100 --end-rate 200000 --duration 30s --ramp linear \
    | loglens --bench out.txt

  # Exponential ramp (better for finding the breaking point):
  loggen --start-rate 100 --end-rate 500000 --duration 60s --ramp exp \
    | loglens --bench out.txt

  # Step-function (hold each rate long enough to see lag stabilize):
  loggen --ramp step --steps 1000,5000,20000,100000,300000 --step-hold 10s \
    | loglens --bench out.txt`)
}
