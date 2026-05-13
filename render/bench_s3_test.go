package render

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/wufe/loglens/line"
)

// BenchmarkRenderS3JSONCollapsed exercises the hot-path collapsed render
// for a single-line DD/s3gw log line. View() calls this once per visible
// line on every Update cycle.
func BenchmarkRenderS3JSONCollapsed(b *testing.B) {
	data, err := os.ReadFile("/tmp/s3line.txt")
	if err != nil {
		b.Skip("no /tmp/s3line.txt sample")
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		b.Fatal(err)
	}
	l := &line.LogLine{
		Raw:        string(data),
		Type:       line.TypeJSON,
		Expandable: true,
		Meta: &line.JSONMeta{
			Value:   parsed,
			Summary: "",
			Keys:    nil,
			RawJSON: data,
		},
	}
	styles := testStyles()
	width := 200
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderJSON(l, width, styles)
	}
}
