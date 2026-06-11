package server

import "github.com/prajwalmahajan101/toykv/internal/resp"

// cmdPing implements PING. With no argument it returns +PONG. With one
// argument it echoes the argument as a bulk string — matching Redis's
// behaviour.
func cmdPing(_ *Server, argv [][]byte) resp.Value {
	if len(argv) == 1 {
		return resp.String("PONG")
	}
	return resp.Bulk(argv[1])
}

// cmdEcho returns its argument as a bulk string.
func cmdEcho(_ *Server, argv [][]byte) resp.Value {
	return resp.Bulk(argv[1])
}
