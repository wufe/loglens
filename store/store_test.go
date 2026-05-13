package store

import (
	"fmt"
	"github.com/wufe/loglens/line"
	"testing"
)

func TestSerializeRoundTripPlain(t *testing.T) {
	lines := []*line.LogLine{
		{Raw: "hello world", Type: line.TypePlain},
		{Raw: "another line", Type: line.TypePlain, FromStderr: true},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(got))
	}
	if got[0].Raw != "hello world" {
		t.Errorf("line 0 raw = %q", got[0].Raw)
	}
	if got[1].FromStderr != true {
		t.Error("line 1 should be FromStderr")
	}
}

func TestSerializeRoundTripJSON(t *testing.T) {
	meta := &line.JSONMeta{
		Value:   map[string]interface{}{"key": "value", "num": float64(42)},
		Summary: `{"key":"value","num":42}`,
		Keys:    []string{"key", "num"},
		RawJSON: []byte(`{"key":"value","num":42}`),
	}
	lines := []*line.LogLine{
		{
			Raw:        `{"key":"value","num":42}`,
			Type:       line.TypeJSON,
			Expandable: true,
			Meta:       meta,
			Segments: []line.Segment{
				{Text: `{"key":"value","num":42}`, Style: "json"},
			},
		},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 line, got %d", len(got))
	}
	l := got[0]
	if l.Type != line.TypeJSON {
		t.Errorf("type = %d, want TypeJSON", l.Type)
	}
	if !l.Expandable {
		t.Error("should be expandable")
	}
	jm, ok := l.Meta.(*line.JSONMeta)
	if !ok {
		t.Fatal("Meta should be *JSONMeta")
	}
	if jm.Summary != meta.Summary {
		t.Errorf("summary = %q, want %q", jm.Summary, meta.Summary)
	}
	if len(jm.Keys) != 2 || jm.Keys[0] != "key" || jm.Keys[1] != "num" {
		t.Errorf("keys = %v", jm.Keys)
	}
	if string(jm.RawJSON) != string(meta.RawJSON) {
		t.Errorf("rawJSON mismatch")
	}
	// Children should be nil (not serialized)
	if l.Children != nil {
		t.Error("children should be nil after deserialization")
	}
	// Expanded should be false (not serialized)
	if l.Expanded {
		t.Error("expanded should be false after deserialization")
	}
}

func TestSerializeRoundTripGoTest(t *testing.T) {
	lines := []*line.LogLine{
		{
			Raw:  "--- PASS: TestFoo (1.23s)",
			Type: line.TypeGoTestResult,
			Meta: &line.GoTestMeta{
				Action:   "PASS",
				TestName: "TestFoo",
				Duration: "1.23s",
				IsPass:   true,
			},
		},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	gm := got[0].Meta.(*line.GoTestMeta)
	if gm.Action != "PASS" || gm.TestName != "TestFoo" || !gm.IsPass {
		t.Errorf("GoTestMeta mismatch: %+v", gm)
	}
}

func TestSerializeRoundTripDiff(t *testing.T) {
	lines := []*line.LogLine{
		{Raw: "+added line", Type: line.TypeDiff, Meta: &line.DiffMeta{LineKind: "add"}},
		{Raw: "-removed line", Type: line.TypeDiff, Meta: &line.DiffMeta{LineKind: "remove"}},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Meta.(*line.DiffMeta).LineKind != "add" {
		t.Error("diff meta mismatch")
	}
}

func TestSerializeRoundTripTable(t *testing.T) {
	lines := []*line.LogLine{
		{Raw: "col1 | col2", Type: line.TypeTable, Meta: &line.TableMeta{Columns: []int{0, 6}, IsHeader: true}},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	tm := got[0].Meta.(*line.TableMeta)
	if len(tm.Columns) != 2 || tm.Columns[0] != 0 || tm.Columns[1] != 6 {
		t.Errorf("table columns = %v", tm.Columns)
	}
	if !tm.IsHeader {
		t.Error("should be header")
	}
}

func TestSerializeRoundTripWarning(t *testing.T) {
	lines := []*line.LogLine{
		{Raw: "WARN: something", Type: line.TypeWarning, Meta: &line.WarningMeta{Level: "Warning"}},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Meta.(*line.WarningMeta).Level != "Warning" {
		t.Error("warning meta mismatch")
	}
}

func TestSerializeRoundTripGroup(t *testing.T) {
	lines := []*line.LogLine{
		{Raw: "{", Type: line.TypeJSON, GroupID: 1, GroupHead: true, Expandable: true},
		{Raw: `  "key": "value"`, Type: line.TypeJSON, GroupID: 1},
		{Raw: "}", Type: line.TypeJSON, GroupID: 1},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
	if !got[0].GroupHead || got[0].GroupID != 1 {
		t.Error("head mismatch")
	}
	if got[1].GroupHead || got[1].GroupID != 1 {
		t.Error("member 1 mismatch")
	}
	if got[2].GroupHead || got[2].GroupID != 1 {
		t.Error("member 2 mismatch")
	}
}

func TestSerializeRoundTripNestedJSON(t *testing.T) {
	// JSONMeta.Value with nested objects and arrays
	meta := &line.JSONMeta{
		Value: map[string]interface{}{
			"name": "test",
			"items": []interface{}{
				map[string]interface{}{"id": float64(1), "active": true},
				map[string]interface{}{"id": float64(2), "active": false},
			},
			"count": float64(2),
		},
		Keys:    []string{"name", "items", "count"},
		RawJSON: []byte(`{"name":"test","items":[{"id":1,"active":true},{"id":2,"active":false}],"count":2}`),
	}
	lines := []*line.LogLine{
		{Raw: `{"name":"test",...}`, Type: line.TypeJSON, Expandable: true, Meta: meta},
	}
	data, err := serializeChunk(lines)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	jm := got[0].Meta.(*line.JSONMeta)
	v := jm.Value.(map[string]interface{})
	items := v["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	item0 := items[0].(map[string]interface{})
	if item0["id"] != float64(1) || item0["active"] != true {
		t.Errorf("item0 = %v", item0)
	}
}

func TestSerializeEmptyChunk(t *testing.T) {
	data, err := serializeChunk(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 lines, got %d", len(got))
	}
}

func TestLineStoreBasic(t *testing.T) {
	s := New()
	s.Append(&line.LogLine{Raw: "line 0", Type: line.TypePlain})
	s.Append(&line.LogLine{Raw: "line 1", Type: line.TypePlain})
	s.Append(&line.LogLine{Raw: "line 2", Type: line.TypeJSON, GroupID: 1})

	if s.Len() != 3 {
		t.Fatalf("Len() = %d", s.Len())
	}
	if s.Get(0).Raw != "line 0" {
		t.Errorf("Get(0).Raw = %q", s.Get(0).Raw)
	}
	if s.Get(2).Raw != "line 2" {
		t.Errorf("Get(2).Raw = %q", s.Get(2).Raw)
	}

	stub := s.Stub(2)
	if stub.Type != line.TypeJSON || stub.GroupID != 1 {
		t.Errorf("stub mismatch: %+v", stub)
	}

	if s.IsHiddenGroupMember(0) {
		t.Error("line 0 should not be hidden")
	}
	if !s.IsHiddenGroupMember(2) {
		t.Error("line 2 (non-head, GroupID=1, TypeJSON) should be hidden")
	}
}

func TestLineStoreChunkBoundary(t *testing.T) {
	s := New()
	// Fill beyond one chunk
	n := ChunkSize + 100
	for i := range n {
		s.Append(&line.LogLine{Raw: "line", Type: line.TypePlain, GroupID: i})
	}
	if s.Len() != n {
		t.Fatalf("Len() = %d, want %d", s.Len(), n)
	}
	if s.ChunkCount() != 2 {
		t.Fatalf("ChunkCount() = %d, want 2", s.ChunkCount())
	}
	// Check lines at chunk boundary
	if s.Get(ChunkSize-1).GroupID != ChunkSize-1 {
		t.Error("last line of chunk 0 mismatch")
	}
	if s.Get(ChunkSize).GroupID != ChunkSize {
		t.Error("first line of chunk 1 mismatch")
	}
	if s.Get(n-1).GroupID != n-1 {
		t.Error("last line mismatch")
	}
}

func TestOffloadAndReload(t *testing.T) {
	s := New()
	defer s.Close()

	// Populate 3 full chunks + some extra
	n := ChunkSize*3 + 100
	for i := range n {
		s.Append(&line.LogLine{
			Raw:  fmt.Sprintf("line %d", i),
			Type: line.TypePlain,
		})
	}
	if s.ChunkCount() != 4 {
		t.Fatalf("ChunkCount() = %d, want 4", s.ChunkCount())
	}

	// Manually offload chunk 1 (middle chunk)
	if err := s.offloadChunk(1); err != nil {
		t.Fatal(err)
	}
	if s.ChunkStateAt(1) != ChunkCold {
		t.Error("chunk 1 should be cold")
	}
	if s.diskUsed == 0 {
		t.Error("diskUsed should be > 0")
	}

	// Accessing a line in the cold chunk should transparently reload it
	l := s.Get(ChunkSize + 5)
	if l.Raw != fmt.Sprintf("line %d", ChunkSize+5) {
		t.Errorf("Get(%d).Raw = %q", ChunkSize+5, l.Raw)
	}
	if s.ChunkStateAt(1) != ChunkHot {
		t.Error("chunk 1 should be hot after reload")
	}
}

func TestEviction(t *testing.T) {
	s := NewWithDiskCap(1) // 1 byte cap — will force eviction immediately
	defer s.Close()

	n := ChunkSize*3 + 100
	for i := range n {
		s.Append(&line.LogLine{Raw: fmt.Sprintf("line %d", i), Type: line.TypePlain})
	}

	// Offload chunk 1
	s.offloadChunk(1)
	// Evict it
	s.evictChunk(1)

	if s.ChunkStateAt(1) != ChunkEvicted {
		t.Error("chunk 1 should be evicted")
	}
	if s.diskUsed != 0 {
		t.Errorf("diskUsed should be 0 after eviction, got %d", s.diskUsed)
	}

	// Accessing evicted line returns placeholder
	l := s.Get(ChunkSize + 5)
	if l.Raw != "[line evicted from disk cache]" {
		t.Errorf("evicted line raw = %q", l.Raw)
	}
}

func TestRunOffloadCycleBelowThreshold(t *testing.T) {
	s := New()
	defer s.Close()

	// Below threshold: nothing should happen
	for i := range 100 {
		s.Append(&line.LogLine{Raw: fmt.Sprintf("line %d", i), Type: line.TypePlain})
	}
	s.RunOffloadCycle(50, 45, 20)

	for ci := range s.ChunkCount() {
		if s.ChunkStateAt(ci) != ChunkHot {
			t.Errorf("chunk %d should still be hot (below threshold)", ci)
		}
	}
}

func TestRunOffloadCycleAboveThreshold(t *testing.T) {
	s := New()
	defer s.Close()

	// Create enough lines to exceed threshold with many chunks
	n := OffloadThreshold + ChunkSize*4
	for i := range n {
		s.Append(&line.LogLine{Raw: fmt.Sprintf("line %d", i), Type: line.TypePlain})
	}

	// Cursor at the end, viewport 50 lines
	cursor := n - 1
	s.RunOffloadCycle(cursor, cursor-50, 50)
	// Offload is async — wait for the worker to finish and apply results.
	s.WaitOffloadsForTest()

	// Chunks near cursor and at start/end should be hot
	// Chunks far from cursor should be cold
	lastChunk := s.ChunkCount() - 1
	if s.ChunkStateAt(0) != ChunkHot {
		t.Error("chunk 0 should be hot (first chunk)")
	}
	if s.ChunkStateAt(lastChunk) != ChunkHot {
		t.Error("last chunk should be hot")
	}

	// Middle chunks (far from cursor) should be cold
	coldFound := false
	for ci := 2; ci < lastChunk-3; ci++ {
		if s.ChunkStateAt(ci) == ChunkCold {
			coldFound = true
			break
		}
	}
	if !coldFound {
		t.Error("expected some middle chunks to be cold")
	}

	// Data should still be accessible (transparently reloads)
	l := s.Get(ChunkSize * 3)
	if l.Raw != fmt.Sprintf("line %d", ChunkSize*3) {
		t.Errorf("transparent reload: Get(%d).Raw = %q", ChunkSize*3, l.Raw)
	}
}

func TestRunOffloadCycleDiskCapEviction(t *testing.T) {
	// Very small disk cap to force eviction
	s := NewWithDiskCap(100)
	defer s.Close()

	n := OffloadThreshold + ChunkSize*4
	for i := range n {
		s.Append(&line.LogLine{Raw: fmt.Sprintf("line %d", i), Type: line.TypePlain})
	}

	cursor := n - 1
	s.RunOffloadCycle(cursor, cursor-50, 50)
	s.WaitOffloadsForTest()
	// Run again to trigger eviction pass now that diskUsed is up to date.
	s.RunOffloadCycle(cursor, cursor-50, 50)

	// After offloading, disk cap is tiny, so eviction should have occurred
	evictedFound := false
	for ci := range s.ChunkCount() {
		if s.ChunkStateAt(ci) == ChunkEvicted {
			evictedFound = true
			break
		}
	}
	if !evictedFound {
		t.Error("expected some chunks to be evicted (disk cap = 100 bytes)")
	}
}
