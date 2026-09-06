package server

import (
	"context"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// connState holds per-connection protocol state. One is created per
// accepted connection in handleConn and threaded through dispatch to the
// command handlers. It is touched only by its connection's goroutine, so
// no field needs locking. M10 added the negotiated wire protocol and the
// connection id reported by HELLO; M12 adds authentication state, read
// by dispatch's command gating.
type connState struct {
	// proto is the wire protocol version for replies on this connection.
	// It starts at Proto2 and is raised to Proto3 by a successful HELLO 3.
	proto resp.Proto
	// id is the monotonic connection id, assigned at accept time and
	// echoed in the HELLO handshake (Redis's `id` field).
	id uint64
	// authenticated gates command dispatch. A server with no requirepass
	// marks every connection authenticated at accept, so gating stays a
	// single condition; otherwise AUTH / HELLO … AUTH flips it.
	authenticated bool
	// ctx carries the per-connection telemetry context (M16). It is stamped
	// in handleConn and, once tracing lands, roots the connection span so
	// command/store/aof spans thread through it. May be nil on connStates
	// built directly (replay, tests) — use context() to read it safely.
	ctx context.Context
	// applying marks a dispatch that is executing an already-committed command
	// — a Raft Apply (Server.applyReplicated) or an AOF replay — rather than a
	// fresh client request. The replicated dispatch propose gate reads it: when
	// true the command runs locally instead of being re-proposed, so the
	// mutate→appendIfLive path fires exactly once inside Apply. Only ever set
	// on synthetic connStates, never on a live client connection.
	applying bool
	// readonly opts this connection into follower-local reads (M20). Default
	// false: keyspace reads on a non-leader redirect to the leader (linearizable).
	// READONLY sets it so a follower serves reads from local, possibly stale,
	// state; READWRITE clears it. Meaningful only under -replicate; harmless
	// otherwise. Per-connection, so it dies with the connection.
	readonly bool
}

// context returns the connection's telemetry context, defaulting to
// context.Background() when unset so a literal connState (replay, tests) is
// always safe to pass to instrumentation.
func (cs *connState) context() context.Context {
	if cs.ctx != nil {
		return cs.ctx
	}
	return context.Background()
}

// newConnState returns the default state for a freshly accepted
// connection: RESP2 until the client negotiates otherwise, and
// pre-authenticated only when the server requires no password.
func newConnState(id uint64, preAuthed bool) *connState {
	return &connState{proto: resp.Proto2, id: id, authenticated: preAuthed}
}
