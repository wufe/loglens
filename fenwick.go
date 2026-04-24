package main

// fenwick is a binary indexed tree (Fenwick tree) over integer values.
// Supports point update and prefix sum in O(log N).
//
// Used to cache per-line visual row counts so adjustOffset / setAbsoluteOffset
// don't have to linearly scan every line (which was the source of O(N²)
// behavior during streaming with follow mode on).
type fenwick struct {
	n    int
	tree []int // 1-indexed; tree[0] unused
}

func newFenwick(cap int) *fenwick {
	if cap < 1 {
		cap = 1
	}
	return &fenwick{n: cap, tree: make([]int, cap+1)}
}

// buildFenwick constructs a Fenwick tree pre-populated from the given values.
// Capacity is chosen to leave headroom for subsequent appends without an
// immediate rebuild. Construction is O(N).
//
// Note: growing a Fenwick tree by mere tree-slice extension (copy into a
// bigger slice) is *not* safe — prior point updates stop at the old n, so
// parent slots that cover wider ranges would never have been populated. The
// correct way to grow is to rebuild via this function from the full values
// slice.
func buildFenwick(values []int) *fenwick {
	capN := max(len(values)*2, 1024)
	f := &fenwick{n: capN, tree: make([]int, capN+1)}
	// Seed leaf values.
	for i, v := range values {
		f.tree[i+1] = v
	}
	// Linear-time Fenwick construction: each node rolls up into its parent.
	for i := 1; i <= len(values); i++ {
		parent := i + (i & -i)
		if parent <= capN {
			f.tree[parent] += f.tree[i]
		}
	}
	return f
}

// update adds delta at 0-based index i. Caller must ensure i < f.n.
func (f *fenwick) update(i, delta int) {
	if delta == 0 {
		return
	}
	for x := i + 1; x <= f.n; x += x & -x {
		f.tree[x] += delta
	}
}

// prefix returns the sum of values at 0-based indices [0, i).
func (f *fenwick) prefix(i int) int {
	if i <= 0 {
		return 0
	}
	if i > f.n {
		i = f.n
	}
	sum := 0
	for x := i; x > 0; x -= x & -x {
		sum += f.tree[x]
	}
	return sum
}

// findByPrefix returns the smallest 0-based index i in [0, limit) such that
// prefix(i+1) > target. If target >= prefix(limit), returns limit.
// In O(log N). Used to locate the line containing a given visual row.
func (f *fenwick) findByPrefix(target, limit int) int {
	if target < 0 {
		target = 0
	}
	if limit <= 0 {
		return 0
	}
	if limit > f.n {
		limit = f.n
	}
	idx := 0
	step := 1
	for step*2 <= limit {
		step *= 2
	}
	for step > 0 {
		nxt := idx + step
		if nxt <= limit && f.tree[nxt] <= target {
			idx = nxt
			target -= f.tree[nxt]
		}
		step >>= 1
	}
	if idx > limit {
		return limit
	}
	return idx
}
