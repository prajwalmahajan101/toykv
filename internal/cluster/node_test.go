package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// TestNodeLifecycleProposeReturnsReply is the M18 apply-once + reply-fidelity
// check at the Node layer: Start → WaitLeader → Propose round-trips every
// mutating command through Propose→Apply, and Propose returns the exact reply
// the handler produced — crucially the values computed inside Apply (INCR's new
// integer, RPUSH's length) that ToyRaft's Propose would otherwise discard.
func TestNodeLifecycleProposeReturnsReply(t *testing.T) {
	s := store.New()
	n, err := New("n1", storeApply(s), func() []store.SnapshotEntry { return s.Snapshot() }, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := t.Context()
	if err := n.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := n.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	leaderCtx, leaderCancel := context.WithTimeout(ctx, 2*time.Second)
	defer leaderCancel()
	if err := n.WaitLeader(leaderCtx); err != nil {
		t.Fatalf("WaitLeader: %v", err)
	}

	propose := func(argv ...string) resp.Value {
		bs := make([][]byte, len(argv))
		for i, a := range argv {
			bs[i] = []byte(a)
		}
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		defer pcancel()
		reply, err := n.Propose(pctx, bs)
		if err != nil {
			t.Fatalf("Propose(%v): %v", argv, err)
		}
		return reply
	}

	if got := propose("SET", "k", "v"); got.Kind != resp.KindSimpleString || got.Str != "OK" {
		t.Fatalf("SET reply = %+v, want +OK", got)
	}
	// INCR's reply is computed inside Apply — the exact case ToyRaft's Propose
	// drops. Proving it round-trips is the point of the reply-capture design.
	if got := propose("INCR", "c"); got.Kind != resp.KindInteger || got.Int != 1 {
		t.Fatalf("INCR reply = %+v, want :1", got)
	}
	if got := propose("INCR", "c"); got.Int != 2 {
		t.Fatalf("second INCR reply = %+v, want :2", got)
	}
	if got := propose("RPUSH", "l", "a", "b", "c"); got.Kind != resp.KindInteger || got.Int != 3 {
		t.Fatalf("RPUSH reply = %+v, want :3", got)
	}

	// State landed in the store exactly as the replies imply.
	if v, ok, _ := s.Get("c"); !ok || string(v) != "2" {
		t.Fatalf("store Get(c) = %q, %v; want \"2\", true", v, ok)
	}
}
