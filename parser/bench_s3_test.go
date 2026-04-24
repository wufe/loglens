package parser

import (
	"os"
	"testing"
)

// BenchmarkParseS3JSON exercises the single-line-JSON hot path on a large,
// deeply-keyed DD/s3gw-shape log line (about 3KB). Run with:
//
//	go test ./parser -bench=S3JSON -benchmem -run=^$
func BenchmarkParseS3JSON(b *testing.B) {
	data, err := os.ReadFile("/tmp/s3line.txt")
	if err != nil {
		b.Skip("no /tmp/s3line.txt sample")
	}
	line := string(data)
	p := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Parse(line, false)
	}
}
