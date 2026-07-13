package store

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func b(s string) []byte { return []byte(s) }

func TestList_PushPopRoundTrip(t *testing.T) {
	s := New()

	n, err := s.LPush("l", b("b"), b("a")) // LPUSH l b a → [a, b]
	if err != nil || n != 2 {
		t.Fatalf("LPush = %d,%v, want 2,nil", n, err)
	}
	n, err = s.RPush("l", b("c"))
	if err != nil || n != 3 {
		t.Fatalf("RPush = %d,%v, want 3,nil", n, err)
	}

	got, err := s.LRange("l", 0, -1)
	if err != nil {
		t.Fatalf("LRange: %v", err)
	}
	dqEqual(t, got, "a", "b", "c")

	if n, err := s.LLen("l"); err != nil || n != 3 {
		t.Fatalf("LLen = %d,%v, want 3,nil", n, err)
	}
	if v, ok, err := s.LIndex("l", -1); err != nil || !ok || string(v) != "c" {
		t.Fatalf("LIndex(-1) = %q,%v,%v, want c,true,nil", v, ok, err)
	}
	if _, ok, err := s.LIndex("l", 99); err != nil || ok {
		t.Fatalf("LIndex(99) = ok=%v,err=%v, want miss", ok, err)
	}

	if v, ok, err := s.LPop("l"); err != nil || !ok || string(v) != "a" {
		t.Fatalf("LPop = %q,%v,%v, want a,true,nil", v, ok, err)
	}
	if v, ok, err := s.RPop("l"); err != nil || !ok || string(v) != "c" {
		t.Fatalf("RPop = %q,%v,%v, want c,true,nil", v, ok, err)
	}
}

func TestList_EmptyCollectionDeletesKey(t *testing.T) {
	s := New()
	if _, err := s.RPush("l", b("only")); err != nil {
		t.Fatalf("RPush: %v", err)
	}
	if _, ok, _ := s.LPop("l"); !ok {
		t.Fatal("LPop missed")
	}
	if s.Exists("l") != 0 {
		t.Fatal("key still exists after popping last element")
	}
	if _, ok := s.Type("l"); ok {
		t.Fatal("Type reports key after popping last element")
	}
	// Key is fully gone: a SET may now claim it as a string.
	if !s.Set("l", b("str"), SetOpts{Mode: SetNX}) {
		t.Fatal("SetNX rejected on a deleted-by-pop key")
	}
}

func TestHash_CRUDRoundTrip(t *testing.T) {
	s := New()

	n, err := s.HSet("h", b("f1"), b("v1"), b("f2"), b("v2"))
	if err != nil || n != 2 {
		t.Fatalf("HSet = %d,%v, want 2,nil", n, err)
	}
	// Update of an existing field is not "created".
	n, err = s.HSet("h", b("f1"), b("v1b"), b("f3"), b("v3"))
	if err != nil || n != 1 {
		t.Fatalf("HSet update = %d,%v, want 1,nil", n, err)
	}

	if v, ok, err := s.HGet("h", "f1"); err != nil || !ok || string(v) != "v1b" {
		t.Fatalf("HGet f1 = %q,%v,%v, want v1b,true,nil", v, ok, err)
	}
	if _, ok, err := s.HGet("h", "missing"); err != nil || ok {
		t.Fatalf("HGet missing = ok=%v,err=%v, want miss", ok, err)
	}
	if ok, err := s.HExists("h", "f2"); err != nil || !ok {
		t.Fatalf("HExists f2 = %v,%v, want true,nil", ok, err)
	}
	if n, err := s.HLen("h"); err != nil || n != 3 {
		t.Fatalf("HLen = %d,%v, want 3,nil", n, err)
	}

	keys, err := s.HKeys("h")
	if err != nil || len(keys) != 3 {
		t.Fatalf("HKeys = %v,%v, want 3 fields", keys, err)
	}
	vals, err := s.HVals("h")
	if err != nil || len(vals) != 3 {
		t.Fatalf("HVals = %d vals,%v, want 3", len(vals), err)
	}
	flat, err := s.HGetAll("h")
	if err != nil || len(flat) != 6 {
		t.Fatalf("HGetAll = %d elems,%v, want 6", len(flat), err)
	}
	// Pairs are adjacent: verify f1 → v1b via the flat encoding.
	found := false
	for i := 0; i+1 < len(flat); i += 2 {
		if string(flat[i]) == "f1" {
			found = bytes.Equal(flat[i+1], b("v1b"))
		}
	}
	if !found {
		t.Fatalf("HGetAll missing f1→v1b pair: %q", flat)
	}

	if n, err := s.HDel("h", "f1", "nope"); err != nil || n != 1 {
		t.Fatalf("HDel = %d,%v, want 1,nil", n, err)
	}
}

func TestHash_EmptyCollectionDeletesKey(t *testing.T) {
	s := New()
	if _, err := s.HSet("h", b("f"), b("v")); err != nil {
		t.Fatalf("HSet: %v", err)
	}
	if n, _ := s.HDel("h", "f"); n != 1 {
		t.Fatal("HDel missed")
	}
	if s.Exists("h") != 0 {
		t.Fatal("key still exists after deleting last field")
	}
}

// TestWrongType_Matrix drives every typed accessor against every wrong
// value type and expects ErrWrongType from each.
func TestWrongType_Matrix(t *testing.T) {
	setupString := func(s *Store) { s.Set("k", b("v"), SetOpts{}) }
	setupList := func(s *Store) { _, _ = s.RPush("k", b("v")) }
	setupHash := func(s *Store) { _, _ = s.HSet("k", b("f"), b("v")) }

	stringOps := map[string]func(s *Store) error{
		"Get":  func(s *Store) error { _, _, err := s.Get("k"); return err },
		"Incr": func(s *Store) error { _, err := s.Incr("k"); return err },
		"Decr": func(s *Store) error { _, err := s.Decr("k"); return err },
	}
	listOps := map[string]func(s *Store) error{
		"LPush":  func(s *Store) error { _, err := s.LPush("k", b("x")); return err },
		"RPush":  func(s *Store) error { _, err := s.RPush("k", b("x")); return err },
		"LPop":   func(s *Store) error { _, _, err := s.LPop("k"); return err },
		"RPop":   func(s *Store) error { _, _, err := s.RPop("k"); return err },
		"LLen":   func(s *Store) error { _, err := s.LLen("k"); return err },
		"LRange": func(s *Store) error { _, err := s.LRange("k", 0, -1); return err },
		"LIndex": func(s *Store) error { _, _, err := s.LIndex("k", 0); return err },
	}
	hashOps := map[string]func(s *Store) error{
		"HSet":    func(s *Store) error { _, err := s.HSet("k", b("f"), b("v")); return err },
		"HGet":    func(s *Store) error { _, _, err := s.HGet("k", "f"); return err },
		"HDel":    func(s *Store) error { _, err := s.HDel("k", "f"); return err },
		"HExists": func(s *Store) error { _, err := s.HExists("k", "f"); return err },
		"HKeys":   func(s *Store) error { _, err := s.HKeys("k"); return err },
		"HVals":   func(s *Store) error { _, err := s.HVals("k"); return err },
		"HLen":    func(s *Store) error { _, err := s.HLen("k"); return err },
		"HGetAll": func(s *Store) error { _, err := s.HGetAll("k"); return err },
	}

	cases := []struct {
		name  string
		setup func(*Store)
		ops   map[string]func(*Store) error
	}{
		{"string-ops-on-list", setupList, stringOps},
		{"string-ops-on-hash", setupHash, stringOps},
		{"list-ops-on-string", setupString, listOps},
		{"list-ops-on-hash", setupHash, listOps},
		{"hash-ops-on-string", setupString, hashOps},
		{"hash-ops-on-list", setupList, hashOps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for op, fn := range tc.ops {
				s := New()
				tc.setup(s)
				if err := fn(s); !errors.Is(err, ErrWrongType) {
					t.Errorf("%s: err = %v, want ErrWrongType", op, err)
				}
			}
		})
	}
}

func TestSet_OverwritesAnyType(t *testing.T) {
	s := New()
	if _, err := s.RPush("k", b("x")); err != nil {
		t.Fatalf("RPush: %v", err)
	}
	// SET replaces the list wholesale (Redis semantics).
	if !s.Set("k", b("str"), SetOpts{}) {
		t.Fatal("Set rejected")
	}
	if typ, _ := s.Type("k"); typ != "string" {
		t.Fatalf("Type = %q, want string", typ)
	}
	if v, ok, err := s.Get("k"); err != nil || !ok || string(v) != "str" {
		t.Fatalf("Get = %q,%v,%v after overwrite", v, ok, err)
	}
}

func TestType_PerValueKind(t *testing.T) {
	s := New()
	s.Set("s", b("v"), SetOpts{})
	_, _ = s.RPush("l", b("v"))
	_, _ = s.HSet("h", b("f"), b("v"))

	for k, want := range map[string]string{"s": "string", "l": "list", "h": "hash"} {
		if got, ok := s.Type(k); !ok || got != want {
			t.Fatalf("Type(%s) = %q,%v, want %q,true", k, got, ok, want)
		}
	}
	if _, ok := s.Type("missing"); ok {
		t.Fatal("Type on missing key returned ok")
	}
}

// TestTyped_TTLUniform verifies TTL machinery applies to typed values:
// an expired list/hash is invisible to every accessor.
func TestTyped_TTLUniform(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := now
	var mu sync.Mutex
	s := NewWithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	})

	_, _ = s.RPush("l", b("v"))
	_, _ = s.HSet("h", b("f"), b("v"))
	s.Expire("l", now.Add(time.Second))
	s.Expire("h", now.Add(time.Second))

	mu.Lock()
	clock = now.Add(2 * time.Second)
	mu.Unlock()

	if n, err := s.LLen("l"); err != nil || n != 0 {
		t.Fatalf("LLen after expiry = %d,%v, want 0,nil", n, err)
	}
	if n, err := s.HLen("h"); err != nil || n != 0 {
		t.Fatalf("HLen after expiry = %d,%v, want 0,nil", n, err)
	}
	if _, ok := s.Type("l"); ok {
		t.Fatal("Type sees expired list")
	}
	// A push to the expired key recreates it fresh.
	if n, err := s.LPush("l", b("new")); err != nil || n != 1 {
		t.Fatalf("LPush on expired = %d,%v, want 1,nil", n, err)
	}
}

func TestSnapshot_TypedEntries(t *testing.T) {
	s := New()
	s.Set("s", b("v"), SetOpts{})
	_, _ = s.RPush("l", b("a"), b("b"))
	_, _ = s.HSet("h", b("f"), b("v"))
	s.Expire("l", time.Now().Add(time.Hour))

	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot has %d entries, want 3", len(snap))
	}
	byKey := map[string]SnapshotEntry{}
	for _, e := range snap {
		byKey[e.Key] = e
	}
	if e := byKey["s"]; e.Type != "string" || string(e.Value) != "v" {
		t.Fatalf("string entry = %+v", e)
	}
	if e := byKey["l"]; e.Type != "list" || len(e.List) != 2 || string(e.List[0]) != "a" || e.ExpireAt.IsZero() {
		t.Fatalf("list entry = %+v", e)
	}
	if e := byKey["h"]; e.Type != "hash" || string(e.Hash["f"]) != "v" {
		t.Fatalf("hash entry = %+v", e)
	}
}

// TestTyped_ConcurrentStress hammers list and hash ops from many
// goroutines; correctness is checked by final counts and the race
// detector.
func TestTyped_ConcurrentStress(t *testing.T) {
	s := New()
	const goroutines = 50
	const perG = 200

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perG {
				_, _ = s.RPush("list", fmt.Appendf(nil, "g%d-%d", g, i))
				_, _ = s.HSet("hash", fmt.Appendf(nil, "f-g%d-%d", g, i), b("v"))
				_, _ = s.LLen("list")
				_, _, _ = s.HGet("hash", "f-g0-0")
			}
		}()
	}
	wg.Wait()

	if n, err := s.LLen("list"); err != nil || n != goroutines*perG {
		t.Fatalf("final LLen = %d,%v, want %d", n, err, goroutines*perG)
	}
	if n, err := s.HLen("hash"); err != nil || n != goroutines*perG {
		t.Fatalf("final HLen = %d,%v, want %d", n, err, goroutines*perG)
	}
}
