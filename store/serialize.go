package store

import (
	"bytes"
	"compress/flate"
	"encoding/gob"
	"io"
	"github.com/wufe/loglens/line"
)

func init() {
	// Register concrete types that appear behind interface{} fields
	// so gob can encode/decode them.
	gob.Register(&line.JSONMeta{})
	gob.Register(&line.GoTestMeta{})
	gob.Register(&line.DiffMeta{})
	gob.Register(&line.TableMeta{})
	gob.Register(&line.WarningMeta{})
	gob.Register(&line.FFmpegMeta{})
	// JSONMeta.Value can contain these Go types (produced by json.Unmarshal):
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	// Primitives (string, float64, bool, nil) are handled natively by gob.
}

// serializableLine is the gob-encoded representation of a LogLine.
// Children are omitted — they are lazily regenerated from Meta.
type serializableLine struct {
	Raw        string
	Type       line.LineType
	Segments   []line.Segment
	Expandable bool
	Depth      int
	GroupID    int
	GroupHead  bool
	FromStderr bool
	Meta       interface{}
}

func toSerializable(l *line.LogLine) serializableLine {
	return serializableLine{
		Raw:        l.Raw,
		Type:       l.Type,
		Segments:   l.Segments,
		Expandable: l.Expandable,
		Depth:      l.Depth,
		GroupID:    l.GroupID,
		GroupHead:  l.GroupHead,
		FromStderr: l.FromStderr,
		Meta:       l.Meta,
	}
}

func fromSerializable(sl serializableLine) *line.LogLine {
	return &line.LogLine{
		Raw:        sl.Raw,
		Type:       sl.Type,
		Segments:   sl.Segments,
		Expandable: sl.Expandable,
		Depth:      sl.Depth,
		GroupID:    sl.GroupID,
		GroupHead:  sl.GroupHead,
		FromStderr: sl.FromStderr,
		Meta:       sl.Meta,
	}
}

// serializeChunk encodes a slice of LogLines into compressed gob bytes.
func serializeChunk(lines []*line.LogLine) ([]byte, error) {
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	enc := gob.NewEncoder(fw)
	if err := enc.Encode(len(lines)); err != nil {
		fw.Close()
		return nil, err
	}
	for _, l := range lines {
		if err := enc.Encode(toSerializable(l)); err != nil {
			fw.Close()
			return nil, err
		}
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deserializeChunk decodes compressed gob bytes back into LogLines.
func deserializeChunk(data []byte) ([]*line.LogLine, error) {
	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	dec := gob.NewDecoder(fr)
	var n int
	if err := dec.Decode(&n); err != nil {
		return nil, err
	}
	lines := make([]*line.LogLine, n)
	for i := range n {
		var sl serializableLine
		if err := dec.Decode(&sl); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		lines[i] = fromSerializable(sl)
	}
	return lines, nil
}
