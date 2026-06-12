package server

import (
	"context"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// cmdBGRewriteAOF starts a background AOF rewrite. Returns
// "Background append only file rewriting started" when the rewrite is
// launched; "-ERR Background append only file rewriting already in
// progress" when a rewrite is already running; "-ERR ..." when
// persistence is disabled (Dir == "").
//
// The single-flight gate is the server-level rewriteInFlight flag, set
// before launching the goroutine and cleared in defer regardless of
// outcome. See ADR-0005 for the underlying design.
func cmdBGRewriteAOF(s *Server, _ [][]byte) resp.Value {
	if s.aof == nil {
		return resp.Error("ERR persistence is disabled")
	}

	s.rewriteMu.Lock()
	if s.rewriteInFlight {
		s.rewriteMu.Unlock()
		return resp.Error("ERR Background append only file rewriting already in progress")
	}
	s.rewriteInFlight = true
	s.rewriteMu.Unlock()

	go s.runRewrite()
	return resp.String("Background append only file rewriting started")
}

// runRewrite executes one Rewriter cycle and clears the in-flight flag.
// Errors are logged but otherwise swallowed — BGREWRITEAOF replies
// before the rewrite completes, matching Redis semantics.
func (s *Server) runRewrite() {
	defer func() {
		s.rewriteMu.Lock()
		s.rewriteInFlight = false
		s.rewriteMu.Unlock()
	}()

	r := aof.NewRewriter(s.aof, s.snapshotForRewrite)
	if err := r.Rewrite(context.Background()); err != nil {
		s.log.Error("aof rewrite failed", "err", err)
	}
}

// snapshotForRewrite bridges Store.Snapshot to the rewriter's expected
// []aof.SnapshotCmd. It uses renderCanonicalSet so the rewritten file
// is byte-identical to what a live SET sequence would produce.
func (s *Server) snapshotForRewrite() []aof.SnapshotCmd {
	entries := s.store.Snapshot()
	out := make([]aof.SnapshotCmd, 0, len(entries))
	for _, e := range entries {
		argv := renderCanonicalSet([]byte(e.Key), e.Value, e.ExpireAt)
		out = append(out, aof.SnapshotCmd{Argv: argv})
	}
	return out
}
