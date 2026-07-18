package server

import (
	"context"
	"strconv"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// cmdHello implements HELLO [protover [AUTH username password]] — the
// RESP3 handshake and protocol-negotiation command (ADR-0011).
//
//   - HELLO            → handshake for the current protocol (no change).
//   - HELLO 2 | 3      → switch this connection's reply protocol, then
//     return the handshake in the newly negotiated protocol.
//   - HELLO <other>    → -NOPROTO (protocol unchanged).
//   - HELLO … AUTH u p → the grammar is accepted so M12 can wire real
//     verification; with no requirepass configured it errors exactly as
//     Redis does, and the protocol is left unchanged.
//
// Validation precedes any mutation: a rejected AUTH or protover leaves
// cs.proto untouched, matching Redis semantics.
func cmdHello(s *Server, cs *connState, argv [][]byte) resp.Value {
	proto := cs.proto
	if len(argv) >= 2 {
		p, err := strconv.Atoi(string(argv[1]))
		if err != nil || (p != int(resp.Proto2) && p != int(resp.Proto3)) {
			return resp.Error("NOPROTO sorry, this protocol version is not supported.")
		}
		proto = resp.Proto(p)
	}

	// Optional [AUTH username password] clause. Only the exact 3-token
	// form is valid; anything else is a syntax error. Authentication must
	// succeed before the protocol switch commits — a failed AUTH leaves
	// both auth state and proto untouched.
	if len(argv) > 2 {
		if len(argv) != 5 || upperASCII(argv[2]) != "AUTH" {
			return resp.Error("ERR Syntax error in HELLO")
		}
		if v := authenticate(s, cs, argv[3], argv[4]); v.Kind == resp.KindError {
			return v
		}
	}

	// Keep the by-protocol gauge (§1.2) balanced across an upgrade: move one
	// client from the old protocol bucket to the new one. No-op if unchanged.
	if proto != cs.proto {
		m := s.tel.Metrics
		m.ClientsByProtocol.Add(context.Background(), -1, protoAttr(cs.proto))
		m.ClientsByProtocol.Add(context.Background(), 1, protoAttr(proto))
	}
	cs.proto = proto
	return helloReply(cs)
}

// helloReply builds the handshake map. It is encoded as a RESP3 map to a
// proto-3 connection and as a flat array to a proto-2 one (the writer's
// map downgrade), so a single reply value serves both protocols.
func helloReply(cs *connState) resp.Value {
	return resp.Map(
		resp.Bulk([]byte("server")), resp.Bulk([]byte("toykv")),
		resp.Bulk([]byte("version")), resp.Bulk([]byte(serverVersion)),
		resp.Bulk([]byte("proto")), resp.Int(int64(cs.proto)),
		resp.Bulk([]byte("id")), resp.Int(int64(cs.id)),
		resp.Bulk([]byte("mode")), resp.Bulk([]byte("standalone")),
		resp.Bulk([]byte("role")), resp.Bulk([]byte("master")),
		resp.Bulk([]byte("modules")), resp.Array(),
	)
}
