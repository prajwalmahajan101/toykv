package server

import (
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// TestWaitStandalone verifies that WAIT returns 0 immediately on a
// non-replicated server — no followers exist, no polling required.
func TestWaitStandalone(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:0", Store: store.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	go func() { _ = s.Run(t.Context()) }()

	c, err := dialReady(t, s)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	v, err := c.Do("WAIT", "1", "500")
	if err != nil {
		t.Fatalf("WAIT: %v", err)
	}
	if v.Kind != resp.KindInteger || v.Int != 0 {
		t.Fatalf("WAIT on standalone = %+v, want :0", v)
	}
}

// TestWaitTruthful is M21's owned-risk test: WAIT must never over-count dead
// replicas. It starts a 3-node cluster, kills one follower, does a write that
// commits on the surviving quorum (leader + 1 follower), then asserts that
// WAIT 2 500 returns ≤ 1 (not 2).
func TestWaitTruthful(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster test in -short mode")
	}
	rc := newRouteCluster(t, 3)

	leader := rc.leaderIndex(t)
	if leader < 0 {
		t.Fatal("no leader")
	}

	// Connect to the leader directly (bare client; no redirect needed since we
	// dial it explicitly).
	lc, err := client.DialTimeout(rc.caddrs[leader], 2*time.Second)
	if err != nil {
		t.Fatalf("dial leader: %v", err)
	}
	defer lc.Close()

	// 1. Do an initial write so all replicas are caught up before we stop one.
	if v, err := lc.Do("SET", "k", "init"); err != nil || v.Kind != resp.KindSimpleString {
		t.Fatalf("initial SET: v=%+v err=%v", v, err)
	}

	// Wait for both followers to apply the write (poll up to 5 s).
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		v, _ := lc.Do("WAIT", "2", "200")
		if v.Kind == resp.KindInteger && v.Int >= 2 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("both followers did not catch up within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Stop one follower's Raft node (Close() stops the Raft transport and
	//    node; the TCP client listener stays open but new commands fail because
	//    the cluster is gone — that is fine for this test).
	dead := (leader + 1) % 3
	if err := rc.servers[dead].Close(); err != nil {
		t.Fatalf("close follower %d: %v", dead, err)
	}

	// Wait for at least two heartbeat cycles (each 300ms) so the leader has
	// had time to attempt — and fail — AppendEntries to the dead follower.
	// After two failed cycles the leader's matchIndex[dead] is confirmed stale.
	time.Sleep(2 * routeHeartbeat)

	// 3. Do a write — commits on quorum (leader + 1 remaining follower).
	if v, err := lc.Do("SET", "k", "after-stop"); err != nil || v.Kind != resp.KindSimpleString {
		t.Fatalf("SET after stop: v=%+v err=%v", v, err)
	}

	// 4. WAIT 2 500 must return ≤ 1 — the dead follower must NOT be counted.
	v, err := lc.Do("WAIT", "2", "500")
	if err != nil {
		t.Fatalf("WAIT 2 500: %v", err)
	}
	if v.Kind != resp.KindInteger {
		t.Fatalf("WAIT 2 500 = %+v, want integer", v)
	}
	if v.Int > 1 {
		t.Fatalf("WAIT 2 500 = %d, want ≤ 1 (dead follower must not be counted)", v.Int)
	}

	// 5. WAIT 1 500 must return 1 (the live follower acked).
	v, err = lc.Do("WAIT", "1", "500")
	if err != nil {
		t.Fatalf("WAIT 1 500: %v", err)
	}
	if v.Kind != resp.KindInteger || v.Int < 1 {
		t.Fatalf("WAIT 1 500 = %+v, want ≥ 1", v)
	}

	// 6. WAIT 0 0 returns immediately with the current count (fast path).
	start := time.Now()
	v, err = lc.Do("WAIT", "0", "0")
	if err != nil {
		t.Fatalf("WAIT 0 0: %v", err)
	}
	if v.Kind != resp.KindInteger {
		t.Fatalf("WAIT 0 0 = %+v, want integer", v)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("WAIT 0 0 took %v, want < 200ms", elapsed)
	}
}

// TestWaitOnFollowerRedirects verifies that WAIT on a follower returns a
// NOTLEADER redirect rather than a count.
func TestWaitOnFollowerRedirects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster test in -short mode")
	}
	rc := newRouteCluster(t, 3)

	leader := rc.leaderIndex(t)
	if leader < 0 {
		t.Fatal("no leader")
	}

	follower := (leader + 1) % 3
	fc, err := client.DialTimeout(rc.caddrs[follower], 2*time.Second)
	if err != nil {
		t.Fatalf("dial follower: %v", err)
	}
	defer fc.Close()

	v, err := fc.Do("WAIT", "1", "500")
	if err != nil {
		t.Fatalf("WAIT on follower: %v", err)
	}
	if !isNotLeaderReply(v) {
		t.Fatalf("WAIT on follower = %+v, want NOTLEADER", v)
	}
}

// dialReady dials s and waits up to 2 s for the listener to open.
func dialReady(t *testing.T, s *Server) (*client.Client, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		addr := s.Addr()
		if addr != "" {
			return client.DialTimeout(addr, time.Second)
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
