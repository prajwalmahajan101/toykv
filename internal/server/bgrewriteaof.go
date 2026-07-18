package server

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

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
func cmdBGRewriteAOF(s *Server, _ *connState, _ [][]byte) resp.Value {
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

	start := time.Now()
	r := aof.NewRewriter(s.aof, s.snapshotForRewrite)
	err := r.Rewrite(context.Background())

	// §1.5 rewrite metrics: outcome + wall time.
	ctx := context.Background()
	result := "ok"
	if err != nil {
		result = "error"
	}
	s.tel.Metrics.AOFRewrites.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	s.tel.Metrics.AOFRewriteDuration.Record(ctx, time.Since(start).Seconds())

	if err != nil {
		s.log.Error("aof rewrite failed", "err", err)
	}
}

// snapshotForRewrite bridges Store.Snapshot to the rewriter's expected
// []aof.SnapshotCmd. Each key becomes one canonical record:
//
//	string → SET k v [PXAT ms]        (renderCanonicalSet, as in v1/v2)
//	list   → RPUSH k e1 … eN          (front-to-back preserves order)
//	hash   → HSET k f1 v1 … fN vN
//
// Lists and hashes carry TTL as a follow-up PEXPIREAT record — their
// creation commands have no expiry clause (unlike SET's PXAT), and
// PEXPIREAT is already the canonical TTL form on the live path
// (ADR-0004).
func (s *Server) snapshotForRewrite() []aof.SnapshotCmd {
	entries := s.store.Snapshot()
	out := make([]aof.SnapshotCmd, 0, len(entries))
	for _, e := range entries {
		switch e.Type {
		case "list":
			argv := make([][]byte, 0, 2+len(e.List))
			argv = append(argv, []byte("RPUSH"), []byte(e.Key))
			argv = append(argv, e.List...)
			out = append(out, aof.SnapshotCmd{Argv: argv})
		case "hash":
			argv := make([][]byte, 0, 2+2*len(e.Hash))
			argv = append(argv, []byte("HSET"), []byte(e.Key))
			for f, v := range e.Hash {
				argv = append(argv, []byte(f), v)
			}
			out = append(out, aof.SnapshotCmd{Argv: argv})
		default: // "string"
			out = append(out, aof.SnapshotCmd{Argv: renderCanonicalSet([]byte(e.Key), e.Value, e.ExpireAt)})
			continue // TTL already encoded via PXAT
		}
		if !e.ExpireAt.IsZero() {
			out = append(out, aof.SnapshotCmd{Argv: renderPExpireAt([]byte(e.Key), e.ExpireAt)})
		}
	}
	return out
}
