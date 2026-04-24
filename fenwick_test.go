package main

import "testing"

func TestFenwickBasic(t *testing.T) {
	f := newFenwick(8)
	vals := []int{1, 1, 0, 1, 2, 0, 3, 1}
	for i, v := range vals {
		f.update(i, v)
	}
	// prefix sums
	want := []int{0, 1, 2, 2, 3, 5, 5, 8, 9}
	for i := 0; i <= 8; i++ {
		if got := f.prefix(i); got != want[i] {
			t.Errorf("prefix(%d) = %d, want %d", i, got, want[i])
		}
	}
}

func TestFenwickFindByPrefix(t *testing.T) {
	f := newFenwick(8)
	vals := []int{1, 1, 0, 1, 2, 0, 3, 1}
	for i, v := range vals {
		f.update(i, v)
	}
	// target → expected line (row → line containing that row)
	cases := map[int]int{
		0: 0, // row 0 in line 0
		1: 1, // row 1 in line 1
		2: 3, // row 2 in line 3 (line 2 hidden)
		3: 4, // rows 3,4 in line 4
		4: 4,
		5: 6, // rows 5,6,7 in line 6 (line 5 hidden)
		6: 6,
		7: 6,
		8: 7,  // row 8 in line 7
		9: 8,  // past end → limit
		99: 8, // way past end → limit
	}
	for target, want := range cases {
		got := f.findByPrefix(target, 8)
		if got != want {
			t.Errorf("findByPrefix(%d, 8) = %d, want %d", target, got, want)
		}
	}
}

func TestBuildFenwickFromValues(t *testing.T) {
	values := []int{5, 0, 0, 2, 0, 0, 0, 0, 0, 3}
	f := buildFenwick(values)
	if got := f.prefix(10); got != 10 {
		t.Fatalf("prefix(10) = %d, want 10", got)
	}
	if got := f.prefix(4); got != 7 {
		t.Fatalf("prefix(4) = %d, want 7", got)
	}
	// After a rebuild we can keep doing point updates.
	f.update(5, 4)
	if got := f.prefix(10); got != 14 {
		t.Fatalf("prefix(10) after update = %d, want 14", got)
	}
}

func TestFenwickUpdateDelta(t *testing.T) {
	f := newFenwick(4)
	f.update(1, 5)
	if got := f.prefix(4); got != 5 {
		t.Fatalf("prefix(4) = %d, want 5", got)
	}
	// simulate changing the value at index 1 from 5 to 2: apply delta -3
	f.update(1, -3)
	if got := f.prefix(4); got != 2 {
		t.Fatalf("after delta, prefix(4) = %d, want 2", got)
	}
}
