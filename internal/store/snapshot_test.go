package store

import (
	"bytes"
	"sort"
	"testing"
	"time"
)

func TestSnapshot_Empty(t *testing.T) {
	s := New()
	got := s.Snapshot()
	if len(got) != 0 {
		t.Fatalf("Snapshot on empty store = %d entries, want 0", len(got))
	}
}

func TestSnapshot_ReturnsLiveKeysOnly(t *testing.T) {
	clk := newFakeClock(fakeEpoch)
	s := NewWithClock(clk.now)

	s.Set("a", []byte("1"), SetOpts{})
	s.Set("b", []byte("2"), SetOpts{ExpireAt: clk.now().Add(10 * time.Second)})
	s.Set("c", []byte("3"), SetOpts{ExpireAt: clk.now().Add(1 * time.Second)})

	clk.advance(5 * time.Second) // "c" expires; "b" still has 5s left.

	got := s.Snapshot()
	sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })

	if len(got) != 2 {
		t.Fatalf("Snapshot len = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Key != "a" || !bytes.Equal(got[0].Value, []byte("1")) || !got[0].ExpireAt.IsZero() {
		t.Errorf("entry[0] = %+v, want a=1 no-expiry", got[0])
	}
	if got[1].Key != "b" || !bytes.Equal(got[1].Value, []byte("2")) || got[1].ExpireAt.IsZero() {
		t.Errorf("entry[1] = %+v, want b=2 with expiry", got[1])
	}

	// The expired "c" must have been evicted by Snapshot's write lock.
	if s.DBSize() != 2 {
		t.Errorf("DBSize after Snapshot = %d, want 2 (Snapshot must evict expired)", s.DBSize())
	}
}

func TestSnapshot_ValueIsCopy(t *testing.T) {
	s := New()
	original := []byte("hello")
	s.Set("k", original, SetOpts{})

	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot len = %d, want 1", len(got))
	}

	// Mutating the returned slice must not affect the store.
	got[0].Value[0] = 'X'
	v, ok := s.Get("k")
	if !ok || !bytes.Equal(v, []byte("hello")) {
		t.Errorf("after mutating Snapshot result, Get(k) = %q ok=%v, want \"hello\" true", v, ok)
	}
}

func TestSnapshot_PreservesExpireAtExactly(t *testing.T) {
	clk := newFakeClock(fakeEpoch)
	s := NewWithClock(clk.now)

	deadline := clk.now().Add(42*time.Second + 123*time.Millisecond)
	s.Set("k", []byte("v"), SetOpts{ExpireAt: deadline})

	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot len = %d, want 1", len(got))
	}
	if !got[0].ExpireAt.Equal(deadline) {
		t.Errorf("ExpireAt = %v, want %v", got[0].ExpireAt, deadline)
	}
}
