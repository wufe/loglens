package parser

import (
	"github.com/valyala/fastjson"
)

// fastjsonPool reuses fastjson.Parser instances across Parse calls.
// fastjson.Parser allocates an internal arena on first parse; pooling avoids
// re-allocating that arena for every line. Safe under our single-ingestor
// goroutine model — the Pool is also goroutine-safe for shared use.
var fastjsonPool fastjson.ParserPool

// parseJSONAny validates and parses JSON bytes, returning the value as the
// same Go-native tree that encoding/json.Unmarshal would produce
// (map[string]any, []any, float64, string, bool, nil). This is the hot-path
// replacement for the json.Valid + json.Unmarshal pair.
//
// ok=false if the input is not valid JSON.
//
// The returned tree is fully materialized (no *fastjson.Value leak), so the
// fastjson parser can safely be returned to the pool immediately.
func parseJSONAny(data []byte) (any, bool) {
	p := fastjsonPool.Get()
	defer fastjsonPool.Put(p)
	v, err := p.ParseBytes(data)
	if err != nil {
		return nil, false
	}
	return materialize(v), true
}

// parseJSONValid reports whether data is valid JSON without materializing
// the tree. Used in places that only need the validity check.
func parseJSONValid(data []byte) bool {
	p := fastjsonPool.Get()
	defer fastjsonPool.Put(p)
	_, err := p.ParseBytes(data)
	return err == nil
}

// materialize walks a *fastjson.Value and produces the equivalent
// encoding/json-style tree. Keys and ordering inside objects follow the
// original source order exposed by fastjson's Object.Visit.
func materialize(v *fastjson.Value) any {
	switch v.Type() {
	case fastjson.TypeNull:
		return nil
	case fastjson.TypeTrue:
		return true
	case fastjson.TypeFalse:
		return false
	case fastjson.TypeNumber:
		// encoding/json.Unmarshal into `any` always yields float64 for
		// numbers. Downstream render code type-switches on float64, so
		// match that exactly.
		return v.GetFloat64()
	case fastjson.TypeString:
		// GetStringBytes returns the already-unescaped bytes; copy into a
		// string so the value survives parser reuse.
		return string(v.GetStringBytes())
	case fastjson.TypeArray:
		arr := v.GetArray()
		out := make([]any, len(arr))
		for i, elem := range arr {
			out[i] = materialize(elem)
		}
		return out
	case fastjson.TypeObject:
		obj, err := v.Object()
		if err != nil {
			return nil
		}
		out := make(map[string]any, obj.Len())
		obj.Visit(func(key []byte, child *fastjson.Value) {
			out[string(key)] = materialize(child)
		})
		return out
	}
	return nil
}
