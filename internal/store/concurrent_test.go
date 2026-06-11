package store

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// TestIncrConcurrentExact is the milestone-owned risk test (ROADMAP M2):
// 100 goroutines × 1000 INCR on the same key must produce exactly
// 100 000. Run under -race in CI to catch any accidental split of the
// read-modify-write into RLock+Lock.
func TestIncrConcurrentExact(t *testing.T) {
	const goroutines = 100
	const iters = 1000

	s := New()
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if _, err := s.Incr("k"); err != nil {
					t.Errorf("Incr: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, ok := s.Get("k")
	if !ok {
		t.Fatal("key missing after concurrent INCR")
	}
	n, err := strconv.ParseInt(string(got), 10, 64)
	if err != nil {
		t.Fatalf("stored value %q is not an int: %v", got, err)
	}
	if want := int64(goroutines * iters); n != want {
		t.Fatalf("got %d, want %d (lost %d increments — mutex misuse?)", n, want, want-n)
	}
}

// TestMixedConcurrentReadWrite exists purely to give the race detector
// surface area on the read paths (Get, Exists, Keys, DBSize) racing
// against writes (Set, Del, Incr, FlushDB). No correctness assertion
// beyond "no race, no panic".
func TestMixedConcurrentReadWrite(t *testing.T) {
	const goroutines = 32
	const iters = 500

	s := New()
	// Seed a handful of keys so reads hit something.
	for i := 0; i < 16; i++ {
		s.Set(fmt.Sprintf("k%d", i), []byte("0"), SetOpts{})
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("k%d", g%16)
			for i := 0; i < iters; i++ {
				switch i % 8 {
				case 0:
					_, _ = s.Get(key)
				case 1:
					s.Set(key, []byte("v"), SetOpts{})
				case 2:
					_, _ = s.Incr(key)
				case 3:
					_ = s.Exists(key, "kX")
				case 4:
					_, _ = s.Keys("k*")
				case 5:
					_ = s.DBSize()
				case 6:
					s.Del("kX")
				case 7:
					s.Set(key, []byte("1"), SetOpts{Mode: SetXX})
				}
			}
		}()
	}
	wg.Wait()
}
