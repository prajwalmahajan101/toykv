package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// setupServerWithAOF returns a server bound to a random port with AOF
// persistence enabled in dir.
func setupServerWithAOF(t *testing.T, dir string, policy aof.FsyncPolicy) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		Dir:         dir,
		FsyncPolicy: policy,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestAOF_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Round 1 — write mutations and a few skipped conditionals.
	s1 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s1)

	c, r, w := dial(t, s1.Addr())
	writeCmd(t, w, "SET", "k1", "v1")
	expectSimple(t, r, "OK")
	writeCmd(t, w, "SET", "k1", "ignored", "NX") // existing → no-op, no AOF append
	expectNullBulk(t, r)
	writeCmd(t, w, "SET", "k2", "v2", "NX")
	expectSimple(t, r, "OK")
	writeCmd(t, w, "INCR", "ctr")
	expectInt(t, r, 1)
	writeCmd(t, w, "INCR", "ctr")
	expectInt(t, r, 2)
	writeCmd(t, w, "DEL", "k1")
	expectInt(t, r, 1)
	writeCmd(t, w, "DEL", "missing") // count 0 → no append
	expectInt(t, r, 0)

	_ = c.Close()
	cancel()
	<-errCh
	if err := s1.Close(); err != nil {
		t.Fatalf("Close round 1: %v", err)
	}

	// Round 2 — fresh server, same dir. Replay reconstructs state.
	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel2, errCh2 := runServer(t, s2)
	defer func() {
		cancel2()
		<-errCh2
		_ = s2.Close()
	}()

	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	writeCmd(t, w2, "GET", "k1")
	expectNullBulk(t, r2)
	writeCmd(t, w2, "GET", "k2")
	expectBulk(t, r2, "v2")
	writeCmd(t, w2, "GET", "ctr")
	expectBulk(t, r2, "2")
	writeCmd(t, w2, "DBSIZE")
	expectInt(t, r2, 2)
}

func TestAOF_FlushDBPersists(t *testing.T) {
	dir := t.TempDir()

	s1 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s1)
	c, r, w := dial(t, s1.Addr())
	writeCmd(t, w, "SET", "a", "1")
	expectSimple(t, r, "OK")
	writeCmd(t, w, "FLUSHDB")
	expectSimple(t, r, "OK")
	_ = c.Close()
	cancel()
	<-errCh
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel2, errCh2 := runServer(t, s2)
	defer func() {
		cancel2()
		<-errCh2
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()
	writeCmd(t, w2, "DBSIZE")
	expectInt(t, r2, 0)
}

func TestAOF_BadHeaderRefusesStartup(t *testing.T) {
	dir := t.TempDir()
	junk := []byte(strings.Repeat("X", aof.HeaderLen))
	if err := os.WriteFile(filepath.Join(dir, aof.Filename), junk, 0o644); err != nil {
		t.Fatalf("seed junk: %v", err)
	}
	_, err := New(Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(),
		Dir:   dir,
	})
	if err == nil {
		t.Fatal("expected New to fail on bad header, got nil")
	}
}

func TestAOF_DisabledWhenDirEmpty(t *testing.T) {
	// Sanity: existing M2 tests pass with no AOF — the no-Dir path.
	s := setupServer(t)
	if s.aof != nil {
		t.Fatal("expected nil AOF when Dir == \"\"")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
