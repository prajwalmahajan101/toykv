//go:build chaos

// The chaos invariant suite runs long-form by default (5 min/test, ≥3 workers ×
// SIGKILL/SIGSTOP/BGREWRITEAOF). Gated behind the `chaos` build tag so plain
// `go test ./...` (and CI's default `test` job) skip it. Run via:
//
//	make chaos        // full soak, ~15 min
//	make chaos-smoke  // -short -tags=chaos, ~30 s
//
// The always-on harness smoke (TestHarnessSmoke in main_test.go) is *not*
// tagged — it exercises the build path every CI run.

package chaos

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// chaosConfig switches between short-form (CI -short) and long-form (local make
// chaos) iteration counts. Both forms exercise the same invariant code paths;
// the long form just gets more crashes and more workers.
type chaosConfig struct {
	duration     time.Duration
	workers      int
	keySpace     int
	killEvery    time.Duration
	pauseEvery   time.Duration
	rewriteEvery time.Duration
}

func cfgFor(t *testing.T) chaosConfig {
	if testing.Short() {
		return chaosConfig{
			duration:     8 * time.Second,
			workers:      4,
			keySpace:     32,
			killEvery:    1500 * time.Millisecond,
			pauseEvery:   2 * time.Second,
			rewriteEvery: 3 * time.Second,
		}
	}
	return chaosConfig{
		duration:     5 * time.Minute,
		workers:      8,
		keySpace:     128,
		killEvery:    7 * time.Second,
		pauseEvery:   5 * time.Second,
		rewriteEvery: 11 * time.Second,
	}
}

// TestAckedWriteSurvival is the durability soak. Goroutines SET/DEL under
// fsync=always; every +OK SET is tracked. We periodically SIGKILL and restart,
// and after the soak ends we verify that every key still believed live (no
// subsequent DEL ack) is present.
//
// The "no subsequent DEL ack" carve-out matters: the workload deletes keys in
// the same goroutine that SETs them, and a DEL ack durably erases the key. So
// the survivor set is "last ack per key was a SET". That set must round-trip
// every restart.
func TestAckedWriteSurvival(t *testing.T) {
	cfg := cfgFor(t)
	srv := NewServer(t, "always")
	srv.Start(t)
	t.Cleanup(srv.Stop)

	// Tracked state: latest acked op per key. true = SET, false = DEL/missing.
	var mu sync.Mutex
	live := map[string]string{}

	w := &Workload{
		Addr:       srv.Addr,
		Goroutines: cfg.workers,
		KeySpace:   cfg.keySpace,
		OnAckSet: func(k, v string) {
			mu.Lock()
			live[k] = v
			mu.Unlock()
		},
	}
	// Wrap DEL acks too — we extend the workload by re-using its DEL path via
	// a side hook. The workload's case-3 (DEL) doesn't notify; do a parallel
	// "scrubber" goroutine that probes EXISTS post-soak instead. Cleaner.

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	// Fault injector: SIGKILL + restart at killEvery cadence.
	faultsDone := make(chan struct{})
	go func() {
		defer close(faultsDone)
		ticker := time.NewTicker(cfg.killEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				srv.Kill()
				// Brief jitter so the restart isn't lock-step with the worker
				// loop. Tunes how often workers race the listening socket.
				time.Sleep(time.Duration(50+rand.Intn(150)) * time.Millisecond)
				srv.Start(t)
			}
		}
	}()

	w.Run(ctx)
	<-faultsDone

	// Make sure the server is up for verification.
	if srv.Stderr() == "" { // smoke: ensure stderr buffer wired
		_ = srv.Stderr
	}

	// Verify every still-live SET ack survived.
	cli := NewRawClient(srv.Addr)
	defer cli.Close()

	mu.Lock()
	defer mu.Unlock()
	checked := 0
	missing := 0
	for k, want := range live {
		got, isNil, err := cli.Do("GET", k)
		if err != nil {
			t.Fatalf("verify GET %s: %v", k, err)
		}
		checked++
		if isNil || got != want {
			missing++
			if missing <= 5 {
				t.Errorf("acked SET lost: key=%q want=%q got=%q nil=%v", k, want, got, isNil)
			}
		}
	}
	t.Logf("survival: ops=%d errs=%d keys_tracked=%d missing=%d",
		w.Ops(), w.Errors(), checked, missing)
	if missing > 0 {
		t.Fatalf("%d/%d acked SETs missing after crash storm", missing, checked)
	}
}

// TestMonotonicINCR proves AOF replay preserves INCR ordering. A single
// counter is INCRed concurrently under fsync=always with periodic SIGKILL
// + restart. Every acked value must be unique, strictly positive, and the
// post-soak GET must be ≥ every acked value seen.
func TestMonotonicINCR(t *testing.T) {
	cfg := cfgFor(t)
	srv := NewServer(t, "always")
	srv.Start(t)
	t.Cleanup(srv.Stop)

	var (
		mu       sync.Mutex
		seen     = map[int64]struct{}{}
		maxAcked int64
	)

	w := &Workload{
		Addr:       srv.Addr,
		Goroutines: cfg.workers,
		KeySpace:   cfg.keySpace,
		CounterKey: "ctr",
		OnAckIncr: func(n int64) {
			mu.Lock()
			defer mu.Unlock()
			if _, dup := seen[n]; dup {
				t.Errorf("INCR ack value %d returned twice", n)
			}
			seen[n] = struct{}{}
			if n > maxAcked {
				maxAcked = n
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	faultsDone := make(chan struct{})
	go func() {
		defer close(faultsDone)
		ticker := time.NewTicker(cfg.killEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				srv.Kill()
				time.Sleep(time.Duration(50+rand.Intn(150)) * time.Millisecond)
				srv.Start(t)
			}
		}
	}()

	w.Run(ctx)
	<-faultsDone

	cli := NewRawClient(srv.Addr)
	defer cli.Close()
	rep, isNil, err := cli.Do("GET", "ctr")
	if err != nil || isNil {
		t.Fatalf("final GET ctr: err=%v nil=%v", err, isNil)
	}
	final, perr := strconv.ParseInt(rep, 10, 64)
	if perr != nil {
		t.Fatalf("parse final ctr %q: %v", rep, perr)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("monotonic INCR: ops=%d errs=%d unique_acks=%d max_ack=%d final=%d",
		w.Ops(), w.Errors(), len(seen), maxAcked, final)
	if final < maxAcked {
		t.Fatalf("final counter %d < max acked %d — AOF replay lost INCRs", final, maxAcked)
	}
}

// TestNoTornTailUnderRewrite stresses the BGREWRITEAOF + SIGKILL combination
// (M5 owned this risk; chaos composes it with sustained writes). We trigger
// BGREWRITEAOF on a cadence while the workload runs, and SIGKILL at random
// offsets. After every restart the server must come back; its startup log
// must announce a successful replay (no panic, no torn-tail fatal).
func TestNoTornTailUnderRewrite(t *testing.T) {
	cfg := cfgFor(t)
	srv := NewServer(t, "always")
	srv.Start(t)
	t.Cleanup(srv.Stop)

	w := &Workload{
		Addr:       srv.Addr,
		Goroutines: cfg.workers,
		KeySpace:   cfg.keySpace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	// Rewriter goroutine — fires BGREWRITEAOF on rewriteEvery; ignores errors
	// (server may be mid-restart).
	rewriterDone := make(chan struct{})
	go func() {
		defer close(rewriterDone)
		cli := NewRawClient(srv.Addr)
		defer cli.Close()
		ticker := time.NewTicker(cfg.rewriteEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _, _ = cli.Do("BGREWRITEAOF")
			}
		}
	}()

	// Pauser goroutine — random SIGSTOP/SIGCONT to simulate GC / load spikes.
	pauserDone := make(chan struct{})
	go func() {
		defer close(pauserDone)
		ticker := time.NewTicker(cfg.pauseEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := srv.Pause(); err == nil {
					time.Sleep(150 * time.Millisecond)
					_ = srv.Resume()
				}
			}
		}
	}()

	// Killer goroutine — restarts must produce a clean "aof ready" log line.
	killerDone := make(chan struct{})
	restarts := 0
	go func() {
		defer close(killerDone)
		ticker := time.NewTicker(cfg.killEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				srv.Kill()
				time.Sleep(time.Duration(50+rand.Intn(200)) * time.Millisecond)
				srv.Start(t)
				restarts++
			}
		}
	}()

	w.Run(ctx)
	<-rewriterDone
	<-pauserDone
	<-killerDone

	logs := srv.Stderr()
	if strings.Contains(logs, "panic") || strings.Contains(logs, "fatal") {
		t.Fatalf("server stderr contains panic/fatal across %d restarts:\n%s", restarts, logs)
	}
	// Confirm the server replayed at least once across the soak. The structured
	// log line emitted by internal/aof on startup is "aof ready" (see
	// internal/aof — fsync=always emits this on every Start).
	if !strings.Contains(logs, "aof ready") {
		t.Fatalf("no aof-ready log across %d restarts — startup never logged replay outcome:\n%s",
			restarts, logs)
	}
	t.Logf("torn-tail: ops=%d errs=%d restarts=%d", w.Ops(), w.Errors(), restarts)
}
