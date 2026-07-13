package server

import (
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// TestBGRewriteAOF_TypedSnapshotRoundTrip churns typed data, rewrites,
// then restarts a fresh server on the same dir and verifies the
// snapshot records reconstructed lists (in order), hashes, and TTLs.
func TestBGRewriteAOF_TypedSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	c, r, w := dial(t, s.Addr())

	// Heavy churn so the rewrite has something to collapse: build the
	// list up and pop most of it back down.
	for range 20 {
		writeCmd(t, w, "RPUSH", "list", "junk")
		expectInt(t, r, int64(1))
		writeCmd(t, w, "LPOP", "list")
		expectBulk(t, r, "junk")
	}
	writeCmd(t, w, "RPUSH", "list", "a", "b", "c")
	expectInt(t, r, 3)
	writeCmd(t, w, "HSET", "hash", "f1", "v1", "f2", "v2")
	expectInt(t, r, 2)
	writeCmd(t, w, "HDEL", "hash", "f2")
	expectInt(t, r, 1)
	writeCmd(t, w, "SET", "str", "sv")
	expectSimple(t, r, "OK")
	writeCmd(t, w, "EXPIRE", "list", "3600")
	expectInt(t, r, 1)

	preSize := aofSize(t, dir)
	writeCmd(t, w, "BGREWRITEAOF")
	reply := readReply(t, r)
	if reply.Kind != resp.KindSimpleString || !strings.Contains(reply.Str, "rewriting started") {
		t.Fatalf("BGREWRITEAOF reply = %+v", reply)
	}
	waitForRewriteIdle(t, s, 2*time.Second)
	if post := aofSize(t, dir); post >= preSize {
		t.Fatalf("typed rewrite did not shrink AOF: pre=%d post=%d", preSize, post)
	}

	// Shut the first server down cleanly, then replay into a fresh one.
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

	writeCmd(t, w2, "LRANGE", "list", "0", "-1")
	expectBulkArray(t, r2, "a", "b", "c")
	writeCmd(t, w2, "TTL", "list")
	got := readReply(t, r2)
	if got.Kind != resp.KindInteger || got.Int <= 0 || got.Int > 3600 {
		t.Fatalf("TTL list after replay = %+v, want 0 < ttl <= 3600", got)
	}
	writeCmd(t, w2, "HGETALL", "hash")
	expectBulkArray(t, r2, "f1", "v1") // f2 was deleted pre-rewrite
	writeCmd(t, w2, "GET", "str")
	expectBulk(t, r2, "sv")
	writeCmd(t, w2, "TYPE", "list")
	expectSimple(t, r2, "list")
	writeCmd(t, w2, "TYPE", "hash")
	expectSimple(t, r2, "hash")
}
