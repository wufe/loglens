package stats

import (
	"loglens/line"
	"sync"
	"testing"
	"time"
)

func makeJSONLine(value map[string]any) *line.LogLine {
	return &line.LogLine{
		Type: line.TypeJSON,
		Meta: &line.JSONMeta{Value: value},
	}
}

// waitForCount polls the snapshot's total counter until it reaches `want` or
// `timeout` elapses. The stat worker is async, so we can't assert immediately
// after Feed.
func waitForCount(t *testing.T, s *Stat, want uint64, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		snap := s.Snapshot()
		if snap.Total >= want {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for total >= %d, got %d", want, snap.Total)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestExtractFieldString_TopLevel(t *testing.T) {
	l := makeJSONLine(map[string]any{"api_name": "GetObject"})
	v, ok := ExtractFieldString(l, []string{"api_name"})
	if !ok || v != "GetObject" {
		t.Fatalf("got %q, ok=%v; want %q, ok=true", v, ok, "GetObject")
	}
}

func TestExtractFieldString_Nested(t *testing.T) {
	l := makeJSONLine(map[string]any{
		"req": map[string]any{"verb": "POST"},
	})
	v, ok := ExtractFieldString(l, []string{"req", "verb"})
	if !ok || v != "POST" {
		t.Fatalf("got %q, ok=%v; want POST, true", v, ok)
	}
}

func TestExtractFieldString_Missing(t *testing.T) {
	l := makeJSONLine(map[string]any{"x": 1})
	if _, ok := ExtractFieldString(l, []string{"y"}); ok {
		t.Fatal("expected missing key to return false")
	}
}

func TestExtractFieldString_NumberFormat(t *testing.T) {
	l := makeJSONLine(map[string]any{"n": float64(42)})
	v, ok := ExtractFieldString(l, []string{"n"})
	if !ok || v != "42" {
		t.Fatalf("got %q, ok=%v; want 42, true", v, ok)
	}
}

func TestFrequency_NoGroups(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	st, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"api_name"},
		Type:      Frequency,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GetObject", "GetObject", "PutObject"} {
		mgr.Observe(makeJSONLine(map[string]any{"api_name": name}))
	}
	snap := waitForCount(t, st, 3, 500*time.Millisecond)
	want := map[string]uint64{"GetObject": 2, "PutObject": 1}
	got := map[string]uint64{}
	for _, c := range snap.Counts {
		got[c.Label] = c.Count
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("count %q: got %d, want %d", k, got[k], v)
		}
	}
}

func TestFrequency_WithGroups(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	st, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"api_name"},
		Type:      Frequency,
		Groups: []GroupRule{
			{Title: "Downloads", Pattern: "GetObject"},
			{Title: "Uploads", Pattern: "PutObject|UploadPart"},
			{Title: "Others", Pattern: ".*"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	feed := []string{"GetObject", "GetObject", "PutObject", "UploadPart", "ListBuckets"}
	for _, n := range feed {
		mgr.Observe(makeJSONLine(map[string]any{"api_name": n}))
	}
	snap := waitForCount(t, st, uint64(len(feed)), 500*time.Millisecond)
	got := map[string]uint64{}
	for _, c := range snap.Counts {
		got[c.Label] = c.Count
	}
	want := map[string]uint64{"Downloads": 2, "Uploads": 2, "Others": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("group %q: got %d, want %d", k, got[k], v)
		}
	}
}

func TestFrequency_NonMatchingValuesFormOwnGroup(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	st, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"api_name"},
		Type:      Frequency,
		Groups: []GroupRule{
			{Title: "Upload", Pattern: "PutObject|UploadPart"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	feed := []string{"GetObject", "UploadPart", "PutObject", "HeadObject", "PutObjectRetention"}
	for _, n := range feed {
		mgr.Observe(makeJSONLine(map[string]any{"api_name": n}))
	}
	snap := waitForCount(t, st, uint64(len(feed)), 500*time.Millisecond)
	got := map[string]uint64{}
	for _, c := range snap.Counts {
		got[c.Label] = c.Count
	}
	want := map[string]uint64{
		"Upload":             2,
		"GetObject":          1,
		"HeadObject":         1,
		"PutObjectRetention": 1,
	}
	if len(got) != len(want) {
		t.Errorf("group count: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("group %q: got %d, want %d", k, got[k], v)
		}
	}
	if snap.Total != uint64(len(feed)) {
		t.Errorf("total: got %d, want %d", snap.Total, len(feed))
	}
}

func TestObserve_Concurrent(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	st, err := mgr.Add(Definition{
		Name:      "x",
		FieldPath: []string{"k"},
		Type:      Frequency,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const writers = 8
	const perWriter = 200
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				mgr.Observe(makeJSONLine(map[string]any{"k": "v"}))
			}
		}()
	}
	wg.Wait()
	snap := waitForCount(t, st, writers*perWriter-uint64(st.dropped.Load()), 2*time.Second)
	processed := snap.Total + snap.Dropped
	if int(processed) != writers*perWriter {
		t.Errorf("processed+dropped = %d, want %d", processed, writers*perWriter)
	}
}

func TestExtractFieldString_NotJSON(t *testing.T) {
	l := &line.LogLine{Type: line.TypePlain}
	if _, ok := ExtractFieldString(l, []string{"x"}); ok {
		t.Fatal("expected non-JSON line to return false")
	}
}

// TestNoDrops_HighThroughput pushes a large burst through the manager from
// many goroutines and verifies that nothing is dropped under the default
// memory cap. Each label is small (~12 bytes) so 500MB of headroom is far
// more than this test ever queues.
func TestNoDrops_HighThroughput(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	st, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"k"},
		Type:      Frequency,
		Groups: []GroupRule{
			{Title: "A", Pattern: "alpha"},
			{Title: "B", Pattern: "bravo"},
			{Title: "Other", Pattern: ".*"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 16
	const perWriter = 100_000
	values := []string{"alpha", "bravo", "charlie", "delta"}
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				v := values[(seed+j)%len(values)]
				mgr.Observe(makeJSONLine(map[string]any{"k": v}))
			}
		}(i)
	}
	wg.Wait()

	want := uint64(writers * perWriter)
	snap := waitForCount(t, st, want, 10*time.Second)
	if snap.Total != want {
		t.Errorf("total=%d, want %d", snap.Total, want)
	}
	if snap.Dropped != 0 {
		t.Errorf("dropped=%d, want 0 with default 500MB cap", snap.Dropped)
	}
}

// TestDropOnly_WhenCapForced makes sure the dropped counter is still
// functional — when the cap is set absurdly low, drops happen, and the
// total stays consistent (counted + dropped == observed).
func TestDropOnly_WhenCapForced(t *testing.T) {
	mgr := NewManager()
	defer mgr.Stop()
	mgr.SetMemoryCap(0) // refuse all reservations
	st, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"k"},
		Type:      Frequency,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		mgr.Observe(makeJSONLine(map[string]any{"k": "v"}))
	}
	// Give the worker a tick to drain anything that might have slipped in.
	time.Sleep(50 * time.Millisecond)
	snap := st.Snapshot()
	if snap.Dropped != 100 {
		t.Errorf("dropped=%d, want 100 with cap=0", snap.Dropped)
	}
	if snap.Total != 0 {
		t.Errorf("total=%d, want 0 (every line was dropped)", snap.Total)
	}
}

// BenchmarkObserve measures end-to-end producer overhead per Observe call
// for the explicit-groups path (regex match in the worker, lock-free
// counter increment). With a single stat and 3 groups the producer-side
// cost is dominated by the JSON path lookup + slice append.
func BenchmarkObserve(b *testing.B) {
	mgr := NewManager()
	defer mgr.Stop()
	_, err := mgr.Add(Definition{
		Name:      "api",
		FieldPath: []string{"api_name"},
		Type:      Frequency,
		Groups: []GroupRule{
			{Title: "Get", Pattern: "GetObject"},
			{Title: "Put", Pattern: "PutObject|UploadPart"},
			{Title: "Other", Pattern: ".*"},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	l := makeJSONLine(map[string]any{"api_name": "GetObject"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mgr.Observe(l)
	}
}
