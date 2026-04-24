package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBenchParsePrefix(t *testing.T) {
	b := &benchLogger{}
	now := time.Now().UnixNano()
	stripped, lag, ok := b.parsePrefix(fmt.Sprintf("[LLB:%d] hello world", now-5_000_000))
	if !ok {
		t.Fatal("expected prefix to parse")
	}
	if stripped != "hello world" {
		t.Errorf("stripped text = %q, want %q", stripped, "hello world")
	}
	if lag < 4_000_000 || lag > 50_000_000 {
		t.Errorf("lag = %d ns, want ~5ms", lag)
	}

	// No prefix -> passthrough.
	out, _, ok := b.parsePrefix("no prefix here")
	if ok {
		t.Errorf("expected no match, got ok=true (stripped=%q)", out)
	}
	if out != "no prefix here" {
		t.Errorf("passthrough altered: %q", out)
	}

	// Malformed -> passthrough.
	out, _, ok = b.parsePrefix("[LLB:notanumber] x")
	if ok {
		t.Errorf("malformed prefix should not parse")
	}
	if out != "[LLB:notanumber] x" {
		t.Errorf("malformed passthrough altered: %q", out)
	}
}

func TestBenchRateWindowEmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	b, err := newBenchLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	// Shrink bucket width to 50ms so the test runs quickly.
	b.bucketWidthNs = int64(50 * time.Millisecond)

	// Feed 200 tagged lines spread over two buckets.
	start := time.Now()
	for i := 0; i < 200; i++ {
		// Simulate a 2ms end-to-end lag.
		b.lineReceivedWithLag(int64(2 * time.Millisecond))
		if i == 100 {
			// Force into next bucket.
			for time.Since(start) < 60*time.Millisecond {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	b.eofReached(200)
	b.close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "rate_window") {
		t.Errorf("expected rate_window entry in bench output:\n%s", s)
	}
	if !strings.Contains(s, "peak_rate_per_sec=") {
		t.Errorf("expected peak_rate_per_sec in bench output:\n%s", s)
	}
	if !strings.Contains(s, "lag_p95_ms=") {
		t.Errorf("expected lag_p95_ms in bench output:\n%s", s)
	}
}
