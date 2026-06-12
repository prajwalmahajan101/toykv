package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// waitForRewriteIdle polls the server's single-flight flag until the
// background rewrite goroutine has cleared it. Fails the test if the
// rewrite does not finish within the deadline.
func waitForRewriteIdle(t *testing.T, s *Server, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s.rewriteMu.Lock()
		idle := !s.rewriteInFlight
		s.rewriteMu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("BGREWRITEAOF did not complete within %s", within)
}

func TestBGRewriteAOF_ShrinksFile(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
		_ = s.Close()
	}()
	c, r, w := dial(t, s.Addr())
	defer c.Close()

	// Churn: 50 SETs that all collapse to the same key.
	for i := 0; i < 50; i++ {
		writeCmd(t, w, "SET", "k", "v")
		if got := readReply(t, r); got.Kind != resp.KindSimpleString || got.Str != "OK" {
			t.Fatalf("SET reply = %+v, want +OK", got)
		}
	}
	preSize := aofSize(t, dir)

	writeCmd(t, w, "BGREWRITEAOF")
	reply := readReply(t, r)
	if reply.Kind != resp.KindSimpleString || !strings.Contains(reply.Str, "rewriting started") {
		t.Fatalf("BGREWRITEAOF reply = %+v, want +Background ...", reply)
	}
	waitForRewriteIdle(t, s, 2*time.Second)

	postSize := aofSize(t, dir)
	if postSize >= preSize {
		t.Fatalf("rewrite did not shrink AOF: pre=%d post=%d", preSize, postSize)
	}

	// Sanity: replay still produces k=v.
	writeCmd(t, w, "GET", "k")
	got := readReply(t, r)
	if got.Kind != resp.KindBulkString || got.IsNull || string(got.Bytes) != "v" {
		t.Fatalf("GET k = %+v, want bulk \"v\"", got)
	}
}

func TestBGRewriteAOF_RejectsConcurrent(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
		_ = s.Close()
	}()

	// Hold the flag manually so the second BGREWRITEAOF observes "in
	// progress" deterministically without racing against the goroutine.
	s.rewriteMu.Lock()
	s.rewriteInFlight = true
	s.rewriteMu.Unlock()
	defer func() {
		s.rewriteMu.Lock()
		s.rewriteInFlight = false
		s.rewriteMu.Unlock()
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()

	writeCmd(t, w, "BGREWRITEAOF")
	got := readReply(t, r)
	if got.Kind != resp.KindError || !strings.Contains(got.Str, "already in progress") {
		t.Fatalf("BGREWRITEAOF during in-flight reply = %+v, want -ERR ... already in progress", got)
	}
}

func TestBGRewriteAOF_NoPersistence(t *testing.T) {
	// Default test server uses no AOF dir, so persistence is disabled.
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
		_ = s.Close()
	}()
	c, r, w := dial(t, s.Addr())
	defer c.Close()

	writeCmd(t, w, "BGREWRITEAOF")
	got := readReply(t, r)
	if got.Kind != resp.KindError || !strings.Contains(got.Str, "persistence is disabled") {
		t.Fatalf("BGREWRITEAOF without -dir reply = %+v, want -ERR persistence is disabled", got)
	}
}

func TestBGRewriteAOF_PreservesTTLAcrossRewrite(t *testing.T) {
	dir := t.TempDir()
	s := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s)
	c, r, w := dial(t, s.Addr())

	writeCmd(t, w, "SET", "with-ttl", "v", "EX", "3600")
	if got := readReply(t, r); got.Str != "OK" {
		t.Fatalf("SET reply = %+v", got)
	}
	writeCmd(t, w, "SET", "no-ttl", "v2")
	_ = readReply(t, r)

	writeCmd(t, w, "BGREWRITEAOF")
	_ = readReply(t, r)
	waitForRewriteIdle(t, s, 2*time.Second)

	// Restart against the rewritten file and confirm state survives.
	_ = c.Close()
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

	writeCmd(t, w2, "GET", "with-ttl")
	if got := readReply(t, r2); got.Kind != resp.KindBulkString || got.IsNull || string(got.Bytes) != "v" {
		t.Fatalf("with-ttl GET after restart = %+v", got)
	}
	writeCmd(t, w2, "TTL", "with-ttl")
	got := readReply(t, r2)
	if got.Kind != resp.KindInteger || got.Int < 3000 || got.Int > 3600 {
		t.Fatalf("with-ttl TTL after restart = %+v, want roughly 3600s", got)
	}
	writeCmd(t, w2, "GET", "no-ttl")
	if got := readReply(t, r2); got.IsNull || string(got.Bytes) != "v2" {
		t.Fatalf("no-ttl GET after restart = %+v", got)
	}
}

func aofSize(t *testing.T, dir string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, aof.Filename))
	if err != nil {
		t.Fatalf("stat AOF: %v", err)
	}
	return info.Size()
}
