package store

import (
	"errors"
	"path"
	"sort"
	"testing"
	"time"
)

// scanAll drives Scan to completion and returns every key returned across
// all pages. It fails the test if the loop does not terminate promptly
// (guards against a cursor that never reaches 0).
func scanAll(t *testing.T, s *Store, match string, count int) []string {
	t.Helper()
	var got []string
	var cursor uint64
	for i := 0; ; i++ {
		if i > 100_000 {
			t.Fatalf("Scan did not terminate (cursor=%d)", cursor)
		}
		keys, next, err := s.Scan(cursor, match, count)
		if err != nil {
			t.Fatalf("Scan(%d): unexpected err %v", cursor, err)
		}
		got = append(got, keys...)
		if next == 0 {
			return got
		}
		cursor = next
	}
}

func TestScan_FullEnumeration(t *testing.T) {
	s := New()
	want := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, k := range want {
		s.Set(k, []byte("v"), SetOpts{})
	}
	// Small COUNT forces multiple pages; every key must appear exactly once.
	got := scanAll(t, s, "*", 2)
	sort.Strings(got)
	if !equalStringSlices(got, want) {
		t.Fatalf("Scan enumeration = %v, want %v", got, want)
	}
}

func TestScan_CursorResumeInSeqOrder(t *testing.T) {
	s := New()
	order := []string{"first", "second", "third", "fourth"}
	for _, k := range order {
		s.Set(k, []byte("v"), SetOpts{})
	}
	// With COUNT=1 the walk visits keys in creation (seq) order.
	keys, next, err := s.Scan(0, "*", 1)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(keys) != 1 || keys[0] != "first" {
		t.Fatalf("first page = %v, want [first]", keys)
	}
	keys, _, err = s.Scan(next, "*", 1)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(keys) != 1 || keys[0] != "second" {
		t.Fatalf("second page = %v, want [second]", keys)
	}
}

func TestScan_UpdateDoesNotRestampSeq(t *testing.T) {
	s := New()
	s.Set("a", []byte("v"), SetOpts{}) // seq 1
	s.Set("b", []byte("v"), SetOpts{}) // seq 2
	// Overwriting "a" must NOT move it to the end of the scan order.
	s.Set("a", []byte("v2"), SetOpts{})

	keys, _, err := s.Scan(0, "*", 1)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(keys) != 1 || keys[0] != "a" {
		t.Fatalf("first page = %v, want [a] (overwrite must preserve seq)", keys)
	}
}

func TestScan_MatchFilter(t *testing.T) {
	s := New()
	for _, k := range []string{"user:1", "user:2", "post:1", "user:3"} {
		s.Set(k, []byte("v"), SetOpts{})
	}
	got := scanAll(t, s, "user:*", 2)
	sort.Strings(got)
	want := []string{"user:1", "user:2", "user:3"}
	if !equalStringSlices(got, want) {
		t.Fatalf("Scan(user:*) = %v, want %v", got, want)
	}
}

func TestScan_SkipsExpired(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("live", []byte("v"), SetOpts{})
	s.Set("dead", []byte("v"), SetOpts{ExpireAt: fc.now().Add(time.Second)})

	fc.advance(2 * time.Second) // "dead" is now expired but unswept
	got := scanAll(t, s, "*", 10)
	sort.Strings(got)
	if !equalStringSlices(got, []string{"live"}) {
		t.Fatalf("Scan after expiry = %v, want [live]", got)
	}
}

func TestScan_IncludesTypedKeys(t *testing.T) {
	s := New()
	s.Set("str", []byte("v"), SetOpts{})
	if _, err := s.LPush("lst", []byte("x")); err != nil {
		t.Fatalf("LPush: %v", err)
	}
	if _, err := s.HSet("hsh", []byte("f"), []byte("v")); err != nil {
		t.Fatalf("HSet: %v", err)
	}
	got := scanAll(t, s, "*", 1)
	sort.Strings(got)
	want := []string{"hsh", "lst", "str"}
	if !equalStringSlices(got, want) {
		t.Fatalf("Scan typed = %v, want %v", got, want)
	}
}

func TestScan_EmptyStore(t *testing.T) {
	s := New()
	keys, next, err := s.Scan(0, "*", 10)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(keys) != 0 || next != 0 {
		t.Fatalf("empty Scan = (%v, %d), want ([], 0)", keys, next)
	}
}

func TestScan_CountDefaultsWhenNonPositive(t *testing.T) {
	s := New()
	for i := range 5 {
		s.Set("k"+string(rune('a'+i)), []byte("v"), SetOpts{})
	}
	// count<=0 must not stall; it falls back to the Redis default of 10.
	got := scanAll(t, s, "*", 0)
	if len(got) != 5 {
		t.Fatalf("Scan(count=0) returned %d keys, want 5", len(got))
	}
}

func TestScan_BadPattern(t *testing.T) {
	s := New()
	s.Set("k", []byte("v"), SetOpts{})
	if _, _, err := s.Scan(0, "[unterminated", 10); !errors.Is(err, path.ErrBadPattern) {
		t.Fatalf("got %v, want path.ErrBadPattern", err)
	}
}
