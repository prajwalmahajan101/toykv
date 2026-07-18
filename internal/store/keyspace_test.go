package store

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestRename_MovesValueTTLAndType(t *testing.T) {
	clk := newFakeClock(fakeEpoch)
	s := NewWithClock(clk.now)
	s.Set("src", []byte("v"), SetOpts{ExpireAt: fakeEpoch.Add(1000 * time.Second)})

	if err := s.Rename("src", "dst"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// src is gone.
	if v, ok, _ := s.Get("src"); ok {
		t.Fatalf("src still present: %q", v)
	}
	// dst has the value...
	v, ok, err := s.Get("dst")
	if err != nil || !ok || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("Get dst = (%q,%v,%v), want (v,true,nil)", v, ok, err)
	}
	// ...the type...
	if typ, _ := s.Type("dst"); typ != "string" {
		t.Fatalf("Type dst = %q, want string", typ)
	}
	// ...and the TTL travelled.
	if ttl := s.TTL("dst"); ttl <= 0 || ttl > 1000*time.Second {
		t.Fatalf("TTL dst = %v, want ~1000s", ttl)
	}
}

func TestRename_OverwritesDestination(t *testing.T) {
	s := New()
	s.Set("src", []byte("new"), SetOpts{})
	s.Set("dst", []byte("old"), SetOpts{})
	if err := s.Rename("src", "dst"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if v, _, _ := s.Get("dst"); !bytes.Equal(v, []byte("new")) {
		t.Fatalf("dst = %q, want new", v)
	}
}

func TestRename_NoSuchKey(t *testing.T) {
	s := New()
	if err := s.Rename("missing", "dst"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Rename missing = %v, want ErrNoKey", err)
	}
}

func TestRename_SameKeyIsNoop(t *testing.T) {
	s := New()
	s.Set("k", []byte("v"), SetOpts{})
	if err := s.Rename("k", "k"); err != nil {
		t.Fatalf("Rename k k: %v", err)
	}
	if v, ok, _ := s.Get("k"); !ok || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("k = (%q,%v), want (v,true) — self-rename must not drop the key", v, ok)
	}
}

func TestRename_ListAndHashRoundTrip(t *testing.T) {
	s := New()
	s.RPush("lst", []byte("a"), []byte("b"))
	if err := s.Rename("lst", "lst2"); err != nil {
		t.Fatalf("Rename list: %v", err)
	}
	if n, _ := s.LLen("lst2"); n != 2 {
		t.Fatalf("LLen lst2 = %d, want 2", n)
	}
	if typ, _ := s.Type("lst2"); typ != "list" {
		t.Fatalf("Type lst2 = %q, want list", typ)
	}

	s.HSet("h", []byte("f"), []byte("v"))
	if err := s.Rename("h", "h2"); err != nil {
		t.Fatalf("Rename hash: %v", err)
	}
	if v, ok, _ := s.HGet("h2", "f"); !ok || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("HGet h2 f = (%q,%v), want (v,true)", v, ok)
	}
}

func TestRenameNX(t *testing.T) {
	s := New()
	s.Set("src", []byte("v"), SetOpts{})

	// dst absent → moved.
	ok, err := s.RenameNX("src", "dst")
	if err != nil || !ok {
		t.Fatalf("RenameNX (dst absent) = (%v,%v), want (true,nil)", ok, err)
	}
	// Re-create src; dst now exists → not moved.
	s.Set("src", []byte("v2"), SetOpts{})
	ok, err = s.RenameNX("src", "dst")
	if err != nil || ok {
		t.Fatalf("RenameNX (dst exists) = (%v,%v), want (false,nil)", ok, err)
	}
	if v, _, _ := s.Get("dst"); !bytes.Equal(v, []byte("v")) {
		t.Fatalf("dst overwritten by RenameNX: %q", v)
	}
	// Missing source → ErrNoKey.
	if _, err := s.RenameNX("missing", "x"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("RenameNX missing = %v, want ErrNoKey", err)
	}
	// Self → false (destination exists).
	if ok, err := s.RenameNX("dst", "dst"); ok || err != nil {
		t.Fatalf("RenameNX dst dst = (%v,%v), want (false,nil)", ok, err)
	}
}

func TestCopy(t *testing.T) {
	clk := newFakeClock(fakeEpoch)
	s := NewWithClock(clk.now)
	s.Set("src", []byte("v"), SetOpts{ExpireAt: fakeEpoch.Add(500 * time.Second)})

	// Fresh dst → copied, source retained.
	ok, err := s.Copy("src", "dst", false)
	if err != nil || !ok {
		t.Fatalf("Copy = (%v,%v), want (true,nil)", ok, err)
	}
	if v, present, _ := s.Get("src"); !present || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("src lost after Copy: (%q,%v)", v, present)
	}
	if v, _, _ := s.Get("dst"); !bytes.Equal(v, []byte("v")) {
		t.Fatalf("dst = %q, want v", v)
	}
	if ttl := s.TTL("dst"); ttl <= 0 || ttl > 500*time.Second {
		t.Fatalf("Copy did not carry TTL: %v", ttl)
	}

	// dst exists, no REPLACE → not copied.
	s.Set("src", []byte("v2"), SetOpts{})
	if ok, _ := s.Copy("src", "dst", false); ok {
		t.Fatal("Copy without REPLACE overwrote existing dst")
	}
	// dst exists, REPLACE → copied.
	if ok, _ := s.Copy("src", "dst", true); !ok {
		t.Fatal("Copy REPLACE did not overwrite")
	}
	if v, _, _ := s.Get("dst"); !bytes.Equal(v, []byte("v2")) {
		t.Fatalf("dst = %q after REPLACE, want v2", v)
	}

	// Missing source → false.
	if ok, err := s.Copy("missing", "x", false); ok || err != nil {
		t.Fatalf("Copy missing = (%v,%v), want (false,nil)", ok, err)
	}
	// src == dst → ErrSameObject.
	if _, err := s.Copy("src", "src", false); !errors.Is(err, ErrSameObject) {
		t.Fatalf("Copy src src = %v, want ErrSameObject", err)
	}
}

// TestCopy_DeepCopyIsolation proves COPY duplicates list and hash payloads
// so mutating the source never leaks into the copy (a shallow copy would
// alias the *deque / map and corrupt the copy).
func TestCopy_DeepCopyIsolation(t *testing.T) {
	s := New()
	s.RPush("lst", []byte("a"))
	if ok, err := s.Copy("lst", "lstcopy", false); err != nil || !ok {
		t.Fatalf("Copy list: (%v,%v)", ok, err)
	}
	s.RPush("lst", []byte("b")) // mutate source after copy
	if n, _ := s.LLen("lstcopy"); n != 1 {
		t.Fatalf("copy aliased source list: LLen=%d, want 1", n)
	}

	s.HSet("h", []byte("f1"), []byte("v1"))
	if ok, err := s.Copy("h", "hcopy", false); err != nil || !ok {
		t.Fatalf("Copy hash: (%v,%v)", ok, err)
	}
	s.HSet("h", []byte("f2"), []byte("v2")) // mutate source after copy
	if n, _ := s.HLen("hcopy"); n != 1 {
		t.Fatalf("copy aliased source hash: HLen=%d, want 1", n)
	}
}

// TestRename_FreshSeqAtDestination proves the destination key receives a
// fresh creation sequence: a SCAN continued past the source's old seq must
// still surface the renamed key (it behaves as a newly-created key, not one
// stuck behind the cursor). A stale-seq move would make the key vanish from
// the in-flight scan.
func TestRename_FreshSeqAtDestination(t *testing.T) {
	s := New()
	s.Set("k1", []byte("1"), SetOpts{}) // seq 1
	s.Set("k2", []byte("2"), SetOpts{}) // seq 2

	// First page consumes k1; cursor advances to k1's seq (1).
	keys, next, err := s.Scan(0, "*", 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("first Scan page = (%v,%v), want 1 key", keys, err)
	}

	// Rename k1 → k1moved while the scan is mid-flight. With a fresh seq
	// (3 > cursor) the moved key must appear when the scan continues.
	if err := s.Rename("k1", "k1moved"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	seen := map[string]bool{}
	for next != 0 {
		var page []string
		page, next, err = s.Scan(next, "*", 10)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		for _, k := range page {
			seen[k] = true
		}
	}
	if !seen["k1moved"] {
		t.Fatal("renamed key missing from continued scan — destination did not get a fresh seq")
	}
}
