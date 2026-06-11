package server

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// fakeClock is a manually-advanced clock for deterministic TTL tests
// on the server layer. Safe under concurrent reads.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(seed time.Time) *fakeClock { return &fakeClock{t: seed} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

var fakeEpoch = time.Unix(1_700_000_000, 0)

// setupTTLServer wires a server, a store, and the SET/EXPIRE handlers
// to a shared injected clock so TTL behaviour is deterministic. The
// sweeper is disabled (very long interval) — lazy expiry covers what
// these tests assert, and a fast sweeper would race the assertions.
func setupTTLServer(t *testing.T, fc *fakeClock) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.NewWithClock(fc.now),
		NowFunc:     fc.now,
		SweeperOpts: store.SweeperOptions{Interval: time.Hour},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func withTTLServer(t *testing.T, fc *fakeClock, fn func(r *resp.Reader, w *resp.Writer)) {
	t.Helper()
	s := setupTTLServer(t, fc)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()
	c, r, w := dial(t, s.Addr())
	defer c.Close()
	fn(r, w)
}

func TestSet_EX_TTLVisibleViaTTLCommand(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "EX", "10")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, 10)
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 10_000)
	})
}

func TestSet_PX_TTL(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "PX", "2500")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 2500)
		writeCmd(t, w, "TTL", "k")
		// 2.5s → 2 after integer truncation, matching Redis.
		expectInt(t, r, 2)
	})
}

func TestSet_PXAT_Absolute(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	deadline := fakeEpoch.Add(5 * time.Second).UnixMilli()
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "PXAT", itoa(deadline))
		expectSimple(t, r, "OK")
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 5000)
	})
}

func TestSet_EXAT_Absolute(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	deadline := fakeEpoch.Add(7 * time.Second).Unix()
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "EXAT", itoa(deadline))
		expectSimple(t, r, "OK")
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, 7)
	})
}

func TestSet_NX_With_EX(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v1", "NX", "EX", "30")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "SET", "k", "v2", "NX", "EX", "60")
		expectNullBulk(t, r) // NX must reject overwrite
		writeCmd(t, w, "GET", "k")
		expectBulk(t, r, "v1")
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, 30) // original TTL preserved
	})
}

func TestSet_RejectsZeroEX(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "EX", "0")
		expectErrContains(t, r, "invalid expire time")
		writeCmd(t, w, "SET", "k", "v", "PX", "-1")
		expectErrContains(t, r, "invalid expire time")
	})
}

func TestSet_RejectsDuplicateModeOrExpiry(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "NX", "XX")
		expectErrContains(t, r, "syntax error")
		writeCmd(t, w, "SET", "k", "v", "EX", "10", "PX", "10")
		expectErrContains(t, r, "syntax error")
	})
}

func TestSet_RejectsBadToken(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "WHAT")
		expectErrContains(t, r, "syntax error")
	})
}

func TestExpire_OnExistingKey(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "EXPIRE", "k", "5")
		expectInt(t, r, 1)
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, 5)
	})
}

func TestExpire_MissingKey(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "EXPIRE", "nope", "5")
		expectInt(t, r, 0)
	})
}

func TestPExpire_Milliseconds(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "PEXPIRE", "k", "1500")
		expectInt(t, r, 1)
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 1500)
	})
}

func TestPExpireAt_AbsoluteAndIdempotent(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	deadline := fakeEpoch.Add(4 * time.Second).UnixMilli()
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "PEXPIREAT", "k", itoa(deadline))
		expectInt(t, r, 1)
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 4000)
		// Re-applying the same PEXPIREAT must be idempotent (the
		// canonical-form invariant PR C will depend on for replay).
		writeCmd(t, w, "PEXPIREAT", "k", itoa(deadline))
		expectInt(t, r, 1)
		writeCmd(t, w, "PTTL", "k")
		expectInt(t, r, 4000)
	})
}

func TestTTL_Sentinels(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "TTL", "nope")
		expectInt(t, r, -2)
		writeCmd(t, w, "SET", "k", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, -1)
	})
}

func TestPersist_RemovesTTL(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "EX", "10")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "PERSIST", "k")
		expectInt(t, r, 1)
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, -1)
		// PERSIST on a key already without TTL returns 0.
		writeCmd(t, w, "PERSIST", "k")
		expectInt(t, r, 0)
		// PERSIST on a missing key returns 0.
		writeCmd(t, w, "PERSIST", "nope")
		expectInt(t, r, 0)
	})
}

func TestExpire_LazyExpiryEndToEnd(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v", "EX", "2")
		expectSimple(t, r, "OK")
		fc.advance(3 * time.Second)
		writeCmd(t, w, "GET", "k")
		expectNullBulk(t, r)
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, -2)
	})
}

func TestSet_OverwriteWithoutExpiryClearsTTL(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	withTTLServer(t, fc, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v1", "EX", "30")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "SET", "k", "v2") // no expiry token
		expectSimple(t, r, "OK")
		writeCmd(t, w, "TTL", "k")
		expectInt(t, r, -1)
	})
}

// TestPRB_AOFAppendsAreV1Shape documents the milestone-B asymmetry:
// SET-with-TTL appends only the v1-shape (no PXAT token), so replay
// (in a separate server process / restart) will produce a key WITHOUT
// the TTL. PR C closes this by bumping the AOF version and appending
// canonical PXAT. The test verifies the asymmetry exists exactly as
// described — when PR C lands and reverses it, this test should fail
// and be deleted as part of the v2 cutover.
func TestPRB_AOFAppendsAreV1Shape(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	dir := t.TempDir()
	cfgClock := fc.now

	// First server: SET k v EX 60, then stop cleanly (drains AOF).
	mkServer := func() *Server {
		s, err := New(Config{
			Addr:        "127.0.0.1:0",
			Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
			Store:       store.NewWithClock(cfgClock),
			NowFunc:     cfgClock,
			Dir:         dir,
			SweeperOpts: store.SweeperOptions{Interval: time.Hour},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	}

	s1 := mkServer()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s1.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for s1.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c, r, w := dial(t, s1.Addr())
	writeCmd(t, w, "SET", "k", "v", "EX", "60")
	expectSimple(t, r, "OK")
	writeCmd(t, w, "PTTL", "k")
	expectInt(t, r, 60_000)
	c.Close()
	cancel()
	<-errCh
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Second server: same dir, fresh process state.
	s2 := mkServer()
	ctx2, cancel2 := context.WithCancel(context.Background())
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- s2.Run(ctx2) }()
	deadline = time.Now().Add(2 * time.Second)
	for s2.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	defer func() {
		cancel2()
		<-errCh2
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	// Value survived…
	writeCmd(t, w2, "GET", "k")
	expectBulk(t, r2, "v")
	// …but TTL did NOT — this is PR B's documented asymmetry.
	writeCmd(t, w2, "TTL", "k")
	expectInt(t, r2, -1)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
