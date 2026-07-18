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
