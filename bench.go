package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// benchLogger records timing information for manual performance testing.
// Enabled via the --bench <file> flag.
type benchLogger struct {
	mu            sync.Mutex
	f             *os.File
	path          string
	startTime     time.Time
	firstLineSet  bool
	firstLineTime time.Time
	lastLineTime  time.Time
	eofLogged     bool

	adjustCalls   int
	adjustTotalNs int64

	ingestCalls   int
	ingestTotalNs int64

	// Ingest rate + end-to-end lag tracking. A bucket covers one second of
	// wall time (bucketWidthNs) and holds per-line lag samples when the
	// generator embeds `[LLB:<nanos>] ` prefixes. Buckets are flushed to
	// the bench file when they roll over or at EOF.
	bucketWidthNs int64
	curBucket     *rateBucket

	peakRatePerSec     float64
	peakSustainedRate  float64 // highest 1s rate with p95 lag <= sustainedLagP95Ms
	sustainedLagP95Ms  float64 // threshold under which a bucket counts as "keeping up"
	laggedLines        int64
	totalLaggedLines   int64
	maxLagNs           int64
	totalIngestLines   int64 // counted via lineReceived{,WithLag}
}

type rateBucket struct {
	startNano int64   // bucket window start (wall-clock nano)
	lagsNs    []int64 // lag samples (ns) for lines arriving in this window
	untagged  int64   // lines with no [LLB:...] prefix in this window
}

func newBenchLogger(path string) (*benchLogger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	b := &benchLogger{
		f:                 f,
		path:              path,
		startTime:         time.Now(),
		bucketWidthNs:     int64(time.Second),
		sustainedLagP95Ms: 100.0,
	}
	fmt.Fprintf(f, "bench_start=%s\n", b.startTime.Format(time.RFC3339Nano))
	_ = f.Sync()
	return b, nil
}

// parsePrefix looks for `[LLB:<nano>] ` at the start of text. If found,
// returns the stripped text, the lag (now - ts) in ns, and true. Otherwise
// returns the original text and false.
func (b *benchLogger) parsePrefix(text string) (string, int64, bool) {
	if b == nil {
		return text, 0, false
	}
	if !strings.HasPrefix(text, "[LLB:") {
		return text, 0, false
	}
	end := strings.IndexByte(text, ']')
	if end < 0 {
		return text, 0, false
	}
	ns, err := strconv.ParseInt(text[5:end], 10, 64)
	if err != nil {
		return text, 0, false
	}
	rest := text[end+1:]
	if len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return rest, time.Now().UnixNano() - ns, true
}

func (b *benchLogger) lineReceived() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.recordArrivalLocked(false, 0)
	b.mu.Unlock()
}

// lineReceivedWithLag records a tagged-line arrival (prefix was parsed) along
// with the measured end-to-end lag in ns.
func (b *benchLogger) lineReceivedWithLag(lagNs int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.recordArrivalLocked(true, lagNs)
	b.mu.Unlock()
}

func (b *benchLogger) recordArrivalLocked(tagged bool, lagNs int64) {
	now := time.Now()
	if !b.firstLineSet {
		b.firstLineTime = now
		b.firstLineSet = true
	}
	b.lastLineTime = now
	b.totalIngestLines++

	nowNs := now.UnixNano()
	if b.curBucket == nil {
		b.curBucket = &rateBucket{startNano: nowNs}
	}
	for nowNs-b.curBucket.startNano >= b.bucketWidthNs {
		b.flushBucketLocked()
		// Start next bucket exactly one width later so bucket boundaries are
		// aligned to wall time.
		b.curBucket = &rateBucket{startNano: b.curBucket.startNano + b.bucketWidthNs}
	}

	if tagged {
		b.curBucket.lagsNs = append(b.curBucket.lagsNs, lagNs)
		b.totalLaggedLines++
		if lagNs > b.maxLagNs {
			b.maxLagNs = lagNs
		}
	} else {
		b.curBucket.untagged++
	}
}

// flushBucketLocked emits the current bucket's stats and advances state.
// Caller must hold b.mu.
func (b *benchLogger) flushBucketLocked() {
	bk := b.curBucket
	if bk == nil {
		return
	}
	total := int64(len(bk.lagsNs)) + bk.untagged
	if total == 0 {
		return
	}
	windowSec := float64(b.bucketWidthNs) / 1e9
	rate := float64(total) / windowSec
	tOffset := float64(bk.startNano-b.startTime.UnixNano()) / 1e9

	if rate > b.peakRatePerSec {
		b.peakRatePerSec = rate
	}

	if len(bk.lagsNs) > 0 {
		slices.Sort(bk.lagsNs)
		n := len(bk.lagsNs)
		p := func(q float64) float64 {
			idx := int(float64(n-1) * q)
			return float64(bk.lagsNs[idx]) / 1e6
		}
		p50 := p(0.50)
		p95 := p(0.95)
		p99 := p(0.99)
		pmx := p(1.0)
		fmt.Fprintf(b.f, "rate_window t=%.3f lines=%d rate=%.0f tagged=%d lag_p50_ms=%.3f lag_p95_ms=%.3f lag_p99_ms=%.3f lag_max_ms=%.3f\n",
			tOffset, total, rate, n, p50, p95, p99, pmx)
		if p95 <= b.sustainedLagP95Ms && rate > b.peakSustainedRate {
			b.peakSustainedRate = rate
		}
	} else {
		fmt.Fprintf(b.f, "rate_window t=%.3f lines=%d rate=%.0f tagged=0\n",
			tOffset, total, rate)
	}
	_ = b.f.Sync()
}

func (b *benchLogger) eofReached(lineCount int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.eofLogged {
		return
	}
	b.eofLogged = true

	// Drain the final bucket so its stats aren't lost.
	b.flushBucketLocked()
	b.curBucket = nil

	eofTime := time.Now()
	ingestMs := float64(eofTime.Sub(b.firstLineTime).Microseconds()) / 1000.0
	fmt.Fprintf(b.f, "ingest_lines=%d\n", lineCount)
	fmt.Fprintf(b.f, "ingest_ms=%.3f\n", ingestMs)
	if ingestMs > 0 {
		fmt.Fprintf(b.f, "ingest_throughput_lines_per_sec=%.0f\n", float64(lineCount)*1000.0/ingestMs)
	}
	fmt.Fprintf(b.f, "adjust_offset_calls=%d\n", b.adjustCalls)
	if b.adjustCalls > 0 {
		fmt.Fprintf(b.f, "adjust_offset_total_ms=%.3f\n", float64(b.adjustTotalNs)/1e6)
		fmt.Fprintf(b.f, "adjust_offset_avg_us=%.3f\n", float64(b.adjustTotalNs)/1e3/float64(b.adjustCalls))
	}
	fmt.Fprintf(b.f, "ingest_batch_calls=%d\n", b.ingestCalls)
	if b.ingestCalls > 0 {
		fmt.Fprintf(b.f, "ingest_batch_total_ms=%.3f\n", float64(b.ingestTotalNs)/1e6)
		fmt.Fprintf(b.f, "ingest_batch_avg_lines=%.2f\n", float64(lineCount)/float64(b.ingestCalls))
	}
	fmt.Fprintf(b.f, "peak_rate_per_sec=%.0f\n", b.peakRatePerSec)
	if b.totalLaggedLines > 0 {
		fmt.Fprintf(b.f, "tagged_lines=%d\n", b.totalLaggedLines)
		fmt.Fprintf(b.f, "max_lag_ms=%.3f\n", float64(b.maxLagNs)/1e6)
		fmt.Fprintf(b.f, "peak_sustained_rate_per_sec=%.0f sustained_p95_threshold_ms=%.1f\n",
			b.peakSustainedRate, b.sustainedLagP95Ms)
	}
	_ = b.f.Sync()
}

// keyTimed records the time taken by a key handler.
func (b *benchLogger) keyTimed(key string, d time.Duration, cursor int, totalLines int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintf(b.f, "key=%s ms=%.3f cursor=%d total_lines=%d\n",
		key, float64(d.Microseconds())/1000.0, cursor, totalLines)
	_ = b.f.Sync()
}

func (b *benchLogger) recordAdjust(d time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.adjustCalls++
	b.adjustTotalNs += d.Nanoseconds()
	b.mu.Unlock()
}

func (b *benchLogger) recordIngestBatch(d time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.ingestCalls++
	b.ingestTotalNs += d.Nanoseconds()
	b.mu.Unlock()
}

func (b *benchLogger) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Drain any trailing bucket that hasn't rolled over yet. Safe even if
	// eofReached already flushed (b.curBucket will be nil).
	b.flushBucketLocked()
	b.curBucket = nil
	fmt.Fprintf(b.f, "bench_end=%s\n", time.Now().Format(time.RFC3339Nano))
	_ = b.f.Close()
}
