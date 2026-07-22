package server

import (
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// orderedFields is the deliberately non-alphabetical insertion order used
// by the persistence tests. A map-iteration bug (or an order-losing
// snapshot) would surface as a reshuffle of this sequence.
var orderedFields = []string{"zeta", "1", "alpha", "2", "mike", "3", "bravo", "4"}

// TestHashFieldOrder_SurvivesAOFReplay proves HKEYS[i]↔HVALS[i]↔HGETALL
// order is preserved when a fresh server replays the raw append log (no
// rewrite). HSET records replay in order, so the rebuilt field-order slice
// must match the original.
func TestHashFieldOrder_SurvivesAOFReplay(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	c, r, w := dial(t, s.Addr())

	writeCmd(t, w, "HSET", "h", "zeta", "1", "alpha", "2", "mike", "3", "extra", "x")
	expectInt(t, r, 4)
	// Delete then re-add a field: it must land at the END after replay too.
	writeCmd(t, w, "HDEL", "h", "extra")
	expectInt(t, r, 1)
	writeCmd(t, w, "HSET", "h", "bravo", "4")
	expectInt(t, r, 1)

	// Shut down cleanly, then replay into a fresh server on the same dir.
	c.Close()
	cancel()
	<-errCh
	_ = s.Close()

	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel2, errCh2 := runServer(t, s2)
	defer func() {
		cancel2()
		<-errCh2
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	assertHashOrderOnWire(t, r2, w2, "h", orderedFields)
}

// TestHashFieldOrder_SurvivesBGRewriteAOF proves the same order survives a
// BGREWRITEAOF snapshot, which materialises the hash via Store.Snapshot and
// emits HSET fields in tracked order (not map order).
func TestHashFieldOrder_SurvivesBGRewriteAOF(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	c, r, w := dial(t, s.Addr())

	writeCmd(t, w, "HSET", "h", "zeta", "1", "alpha", "2", "mike", "3", "bravo", "4")
	expectInt(t, r, 4)

	writeCmd(t, w, "BGREWRITEAOF")
	reply := readReply(t, r)
	if reply.Kind != resp.KindSimpleString || !strings.Contains(reply.Str, "rewriting started") {
		t.Fatalf("BGREWRITEAOF reply = %+v", reply)
	}
	waitForRewriteIdle(t, s, 2*time.Second)

	c.Close()
	cancel()
	<-errCh
	_ = s.Close()

	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel2, errCh2 := runServer(t, s2)
	defer func() {
		cancel2()
		<-errCh2
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	assertHashOrderOnWire(t, r2, w2, "h", orderedFields)
}

// assertHashOrderOnWire checks HKEYS/HVALS/HGETALL for key k against want
// (a flat [f1,v1,…] expectation) over the RESP2 wire, verifying both the
// order and the pairwise correspondence.
func assertHashOrderOnWire(t *testing.T, r *resp.Reader, w *resp.Writer, k string, want []string) {
	t.Helper()

	wantKeys := make([]string, 0, len(want)/2)
	wantVals := make([]string, 0, len(want)/2)
	for i := 0; i < len(want); i += 2 {
		wantKeys = append(wantKeys, want[i])
		wantVals = append(wantVals, want[i+1])
	}

	writeCmd(t, w, "HKEYS", k)
	expectBulkArray(t, r, wantKeys...)
	writeCmd(t, w, "HVALS", k)
	expectBulkArray(t, r, wantVals...)
	writeCmd(t, w, "HGETALL", k)
	expectBulkArray(t, r, want...)
}
