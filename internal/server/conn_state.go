package server

import "github.com/prajwalmahajan101/toykv/internal/resp"

// connState holds per-connection protocol state. One is created per
// accepted connection in handleConn and threaded through dispatch to the
// command handlers. In M10 it carries only the negotiated wire protocol
// and the connection id reported by HELLO; M12 adds authentication state
// here, and dispatch gains command gating that reads it.
type connState struct {
	// proto is the wire protocol version for replies on this connection.
	// It starts at Proto2 and is raised to Proto3 by a successful HELLO 3.
	proto resp.Proto
	// id is the monotonic connection id, assigned at accept time and
	// echoed in the HELLO handshake (Redis's `id` field).
	id uint64
}

// newConnState returns the default state for a freshly accepted
// connection: RESP2 until the client negotiates otherwise.
func newConnState(id uint64) *connState {
	return &connState{proto: resp.Proto2, id: id}
}
