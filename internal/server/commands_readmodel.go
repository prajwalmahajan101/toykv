package server

import "github.com/prajwalmahajan101/toykv/internal/resp"

// cmdReadOnly opts the connection into follower-local reads (M20): while set,
// keyspace reads run against this node's local state instead of redirecting to
// the leader. The reads are non-linearizable — a follower may be behind the
// leader's commit index. It is a local-admin toggle, valid in any mode; in
// standalone or on a leader it has no observable effect. Mirrors Redis Cluster's
// READONLY. Always replies +OK.
func cmdReadOnly(s *Server, cs *connState, argv [][]byte) resp.Value {
	cs.readonly = true
	return resp.OK()
}

// cmdReadWrite clears the READONLY opt-in, restoring leader-redirected
// (linearizable) reads on this connection. Idempotent; always replies +OK.
func cmdReadWrite(s *Server, cs *connState, argv [][]byte) resp.Value {
	cs.readonly = false
	return resp.OK()
}
