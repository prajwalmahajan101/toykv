package store

import (
	"bytes"
	"fmt"
	"testing"
)

func dqVals(vals ...string) [][]byte {
	out := make([][]byte, len(vals))
	for i, v := range vals {
		out[i] = []byte(v)
	}
	return out
}

func dqEqual(t *testing.T, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %q)", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], []byte(want[i])) {
			t.Fatalf("elem %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// full range helper — rng(0, -1) is "everything" in LRANGE terms.
func dqAll(d *deque) [][]byte { return d.rng(0, -1) }

func TestDeque_PushPopBothEnds(t *testing.T) {
	var d deque
	d.pushBack([]byte("b"))
	d.pushFront([]byte("a"))
	d.pushBack([]byte("c"))
	if d.len() != 3 {
		t.Fatalf("len = %d, want 3", d.len())
	}
	dqEqual(t, dqAll(&d), "a", "b", "c")

	v, ok := d.popFront()
	if !ok || string(v) != "a" {
		t.Fatalf("popFront = %q,%v, want a,true", v, ok)
	}
	v, ok = d.popBack()
	if !ok || string(v) != "c" {
		t.Fatalf("popBack = %q,%v, want c,true", v, ok)
	}
	dqEqual(t, dqAll(&d), "b")
}

func TestDeque_EmptyPops(t *testing.T) {
	var d deque
	if _, ok := d.popFront(); ok {
		t.Fatal("popFront on empty deque returned ok")
	}
	if _, ok := d.popBack(); ok {
		t.Fatal("popBack on empty deque returned ok")
	}
	dqEqual(t, dqAll(&d)) // empty, non-nil
	if dqAll(&d) == nil {
		t.Fatal("rng on empty deque returned nil, want empty slice")
	}
}

// TestDeque_GrowAcrossWrap forces the ring to wrap (head > 0) before
// growing, so grow() must linearise correctly.
func TestDeque_GrowAcrossWrap(t *testing.T) {
	var d deque
	// Fill to initial capacity (8) then rotate so head moves.
	for i := range 8 {
		d.pushBack(fmt.Appendf(nil, "v%d", i))
	}
	// Pop two from the front, push two at the back → head=2, wrapped.
	d.popFront()
	d.popFront()
	d.pushBack([]byte("v8"))
	d.pushBack([]byte("v9"))
	// Now full again with head != 0; next push triggers grow().
	d.pushFront([]byte("front"))
	dqEqual(t, dqAll(&d), "front", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9")

	// Interleave heavy front-pushes to prove O(1)-ish behaviour is at
	// least correct under repeated growth.
	for i := range 100 {
		d.pushFront(fmt.Appendf(nil, "f%d", i))
	}
	if d.len() != 109 {
		t.Fatalf("len = %d, want 109", d.len())
	}
	if got := string(d.at(0)); got != "f99" {
		t.Fatalf("at(0) = %q, want f99", got)
	}
	if got := string(d.at(d.len() - 1)); got != "v9" {
		t.Fatalf("at(last) = %q, want v9", got)
	}
}

func TestDeque_Rng_RedisSemantics(t *testing.T) {
	var d deque
	for _, v := range dqVals("a", "b", "c", "d", "e") {
		d.pushBack(v)
	}
	cases := []struct {
		start, stop int
		want        []string
	}{
		{0, -1, []string{"a", "b", "c", "d", "e"}}, // full range
		{0, 1, []string{"a", "b"}},
		{-2, -1, []string{"d", "e"}},                   // negative from tail
		{-100, 100, []string{"a", "b", "c", "d", "e"}}, // clamped
		{3, 1, []string{}},                             // start > stop
		{5, 10, []string{}},                            // start >= len
		{-1, -2, []string{}},                           // inverted negatives
		{2, 2, []string{"c"}},                          // single element
	}
	for _, tc := range cases {
		got := d.rng(tc.start, tc.stop)
		dqEqual(t, got, tc.want...)
	}
}
