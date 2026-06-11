package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSweepOnce_BoundedByBatch(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	for i := 0; i < 100; i++ {
		s.Set(fmt.Sprintf("k%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(time.Second)})
	}
	fc.advance(2 * time.Second)

	sampled, evicted := s.sweepOnce(fc.now(), 20)
	if sampled != 20 {
		t.Fatalf("sampled = %d, want 20 (capped by batch)", sampled)
	}
	if evicted != 20 {
		t.Fatalf("evicted = %d, want 20 (all sampled were expired)", evicted)
	}
	if got := s.DBSize(); got != 80 {
		t.Fatalf("dbsize after sweepOnce = %d, want 80", got)
	}
}

func TestSweepOnce_LeavesAliveAlone(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	for i := 0; i < 20; i++ {
		s.Set(fmt.Sprintf("k%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(time.Hour)})
	}
	sampled, evicted := s.sweepOnce(fc.now(), 20)
	if sampled != 20 {
		t.Fatalf("sampled = %d, want 20", sampled)
	}
	if evicted != 0 {
		t.Fatalf("evicted = %d, want 0 (none expired)", evicted)
	}
	if got := s.DBSize(); got != 20 {
		t.Fatalf("dbsize = %d, want 20 (none should be removed)", got)
	}
}

func TestSweeper_Tick_LoopsAboveThreshold(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	for i := 0; i < 200; i++ {
		s.Set(fmt.Sprintf("k%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(time.Second)})
	}
	fc.advance(2 * time.Second)
	sw := NewSweeper(s, SweeperOptions{Batch: 20})

	sampled, evicted := sw.tick(fc.now())
	// All sampled entries are expired ⇒ ratio is 1.0 ⇒ tick keeps
	// looping up to maxLoops. Asserts the loop fired at least twice.
	if sampled < 40 {
		t.Fatalf("tick should loop while expired-fraction > threshold; sampled = %d, want ≥40", sampled)
	}
	if evicted != sampled {
		t.Fatalf("expected all sampled evicted; got %d/%d", evicted, sampled)
	}
}

func TestSweeper_Tick_StopsBelowThreshold(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	// 5 expired keys + 95 alive ⇒ first pass expects ~5%/sample
	// (~1 expired in 20), below the 25% threshold ⇒ single pass.
	for i := 0; i < 5; i++ {
		s.Set(fmt.Sprintf("e%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(-time.Second)})
	}
	for i := 0; i < 95; i++ {
		s.Set(fmt.Sprintf("a%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(time.Hour)})
	}
	sw := NewSweeper(s, SweeperOptions{Batch: 20})
	sampled, _ := sw.tick(fc.now())
	if sampled > 40 {
		t.Fatalf("tick should bail out early when expired-fraction ≤ threshold; sampled = %d", sampled)
	}
}

func TestSweeper_Run_EvictsInBackground(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	// 50 expirable + 5 permanent.
	for i := 0; i < 50; i++ {
		s.Set(fmt.Sprintf("e%d", i), []byte("v"),
			SetOpts{ExpireAt: fc.now().Add(time.Second)})
	}
	for i := 0; i < 5; i++ {
		s.Set(fmt.Sprintf("p%d", i), []byte("v"), SetOpts{})
	}
	if got := s.DBSize(); got != 55 {
		t.Fatalf("initial dbsize = %d, want 55", got)
	}

	// Advance virtual clock past expiry, then drive the real-time
	// ticker fast enough to converge in well under the test deadline.
	fc.advance(2 * time.Second)
	sw := NewSweeper(s, SweeperOptions{
		Interval: 5 * time.Millisecond,
		Batch:    25,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sw.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.DBSize() == 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.DBSize(); got != 5 {
		t.Fatalf("after background sweep, dbsize = %d, want 5 (permanent keys only)", got)
	}
}

func TestSweeper_Run_RespectsContextCancel(t *testing.T) {
	s := New()
	sw := NewSweeper(s, SweeperOptions{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sw.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Sweeper.Run did not return within 1s of ctx cancel")
	}
}

func TestNewSweeper_DefaultsApplied(t *testing.T) {
	s := New()
	sw := NewSweeper(s, SweeperOptions{})
	if sw.interval != time.Second {
		t.Errorf("default interval = %v, want 1s", sw.interval)
	}
	if sw.batch != 20 {
		t.Errorf("default batch = %d, want 20", sw.batch)
	}
	if sw.threshold != 0.25 {
		t.Errorf("default threshold = %v, want 0.25", sw.threshold)
	}
	if sw.maxLoops != 16 {
		t.Errorf("default maxLoops = %d, want 16", sw.maxLoops)
	}
}
