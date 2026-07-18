package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	got, ok, _ := s.Get("k")
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

// TestTTL_LockUpgradeRace_NoSpuriousMiss is the milestone-owned risk
// test for M4 (ROADMAP M4 / LLD §3.2). A writer goroutine repeatedly
// re-SETs a single key with a 100 ms TTL, the sweeper runs on a 5 ms
// interval, and N reader goroutines pound Get on that key.
//
// Invariant: a Get whose internal time check runs at t must return the
// value whenever t < "expireAt of the most-recently-completed SET that
// the reader has observed as committed." A spurious nil here would
// mean the lock-upgrade window in Get (RLock → release → Lock →
// re-check) is dropping live values — the bug LLD §3.2 calls out.
//
// Test mechanics:
//   - Writer publishes each completed Set's expireAt via atomic.Store
//     AFTER Set returns. The happens-before edge guarantees that any
//     reader observing this value via atomic.Load also sees the Set's
//     effect on the store map.
//   - Reader does atomic.Load BEFORE Get, captures tAfter AFTER Get.
//     If Get returns nil and tAfter < loadedExpireAt, then Get's
//     internal time check (which precedes tAfter) was strictly inside
//     the live window of a committed entry — that is the bug.
//
// Uses real time (not fakeClock) so the sweeper's real ticker and
// Get's lock-upgrade interleave the way they would in production. Run
// under -race -count=20 to stress-test.
func TestTTL_LockUpgradeRace_NoSpuriousMiss(t *testing.T) {
	const (
		key        = "k"
		ttl        = 100 * time.Millisecond
		readers    = 8
		testWindow = 1500 * time.Millisecond
		sweepEvery = 5 * time.Millisecond
		sweepBatch = 50
	)

	s := New()
	sw := NewSweeper(s, SweeperOptions{Interval: sweepEvery, Batch: sweepBatch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sw.Run(ctx)

	var lastExpireAtNano atomic.Int64
	done := make(chan struct{})
	var violations atomic.Int64
	var reads atomic.Int64

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			expireAt := time.Now().Add(ttl)
			s.Set(key, []byte("v"), SetOpts{ExpireAt: expireAt})
			lastExpireAtNano.Store(expireAt.UnixNano())
		}
	}()

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				// Load BEFORE Get: synchronizes-with the writer's
				// most recent committed Set. The store map is
				// guaranteed to reflect at least that Set when Get
				// runs.
				loaded := lastExpireAtNano.Load()
				_, ok, _ := s.Get(key)
				// Capture AFTER Get: upper-bounds Get's internal
				// time check.
				tAfter := time.Now()
				reads.Add(1)
				if !ok && loaded > 0 && tAfter.UnixNano() < loaded {
					violations.Add(1)
				}
			}
		}()
	}

	time.Sleep(testWindow)
	close(done)
	wg.Wait()
	cancel()

	if v := violations.Load(); v != 0 {
		t.Fatalf("got %d spurious nil reads (of %d total) while key was within TTL — lock-upgrade dropped live values",
			v, reads.Load())
	}
	if reads.Load() < 1000 {
		t.Fatalf("test was not exercised enough: only %d reads issued", reads.Load())
	}
}

// TestRenameConcurrent_ExactlyOneWinner is the milestone-owned risk test
// for M15 (ROADMAP §M15 concurrent-rename atomicity): N goroutines race to
// RENAME the same source key. Because the move is a single store-mutex
// critical section, exactly one must succeed (the rest see ErrNoKey once
// the source is consumed), with no torn intermediate state, and the
// surviving key must keep the source's value, TTL, and type. Run under
// -race.
func TestRenameConcurrent_ExactlyOneWinner(t *testing.T) {
	const goroutines = 50

	t.Run("distinct destinations", func(t *testing.T) {
		clk := newFakeClock(fakeEpoch)
		s := NewWithClock(clk.now)
		s.RPush("src", []byte("a"), []byte("b"))
		s.Expire("src", fakeEpoch.Add(1000*time.Second))

		var wins atomic.Int64
		var winner atomic.Value // string
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			i := i
			go func() {
				defer wg.Done()
				dst := fmt.Sprintf("dst%d", i)
				err := s.Rename("src", dst)
				if err == nil {
					wins.Add(1)
					winner.Store(dst)
				} else if !errors.Is(err, ErrNoKey) {
					t.Errorf("Rename: unexpected error %v (want nil or ErrNoKey)", err)
				}
			}()
		}
		wg.Wait()

		if n := wins.Load(); n != 1 {
			t.Fatalf("got %d winners, want exactly 1", n)
		}
		dst := winner.Load().(string)
		if typ, _ := s.Type(dst); typ != "list" {
			t.Fatalf("survivor %q type = %q, want list", dst, typ)
		}
		if n, _ := s.LLen(dst); n != 2 {
			t.Fatalf("survivor %q LLen = %d, want 2 (torn value?)", dst, n)
		}
		if ttl := s.TTL(dst); ttl <= 0 || ttl > 1000*time.Second {
			t.Fatalf("survivor %q lost its TTL: %v", dst, ttl)
		}
		if got := s.DBSize(); got != 1 {
			t.Fatalf("DBSize = %d, want 1 (exactly the survivor)", got)
		}
	})

	t.Run("same destination", func(t *testing.T) {
		s := New()
		s.Set("src", []byte("v"), SetOpts{})

		var wins atomic.Int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				if err := s.Rename("src", "dst"); err == nil {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()

		if n := wins.Load(); n != 1 {
			t.Fatalf("got %d winners, want exactly 1", n)
		}
		if v, ok, _ := s.Get("dst"); !ok || string(v) != "v" {
			t.Fatalf("dst = (%q,%v), want (v,true)", v, ok)
		}
	})
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
					_, _, _ = s.Get(key)
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
