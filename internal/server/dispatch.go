package server

import (
	"bytes"
	"fmt"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// handler executes one command and returns the reply frame.
type handler struct {
	// fn is the command implementation.
	fn func(s *Server, argv [][]byte) resp.Value
	// minArgs / maxArgs bound the inclusive argv length, *including* the
	// command name itself. maxArgs = -1 means unbounded.
	minArgs, maxArgs int
}

// commands is the dispatch table. M1 ships PING + ECHO; M2 adds the
// store-core commands; M4 adds TTL ops. Per-command arity bounds; SET's
// upper bound covers "SET k v NX EX 10" — 6 tokens including the verb.
var commands = map[string]handler{
	"PING":      {fn: cmdPing, minArgs: 1, maxArgs: 2},
	"ECHO":      {fn: cmdEcho, minArgs: 2, maxArgs: 2},
	"GET":       {fn: cmdGet, minArgs: 2, maxArgs: 2},
	"SET":       {fn: cmdSet, minArgs: 3, maxArgs: -1},
	"DEL":       {fn: cmdDel, minArgs: 2, maxArgs: -1},
	"EXISTS":    {fn: cmdExists, minArgs: 2, maxArgs: -1},
	"INCR":      {fn: cmdIncr, minArgs: 2, maxArgs: 2},
	"DECR":      {fn: cmdDecr, minArgs: 2, maxArgs: 2},
	"KEYS":      {fn: cmdKeys, minArgs: 2, maxArgs: 2},
	"FLUSHDB":   {fn: cmdFlushDB, minArgs: 1, maxArgs: 1},
	"DBSIZE":    {fn: cmdDBSize, minArgs: 1, maxArgs: 1},
	"EXPIRE":    {fn: cmdExpire, minArgs: 3, maxArgs: 3},
	"PEXPIRE":   {fn: cmdPExpire, minArgs: 3, maxArgs: 3},
	"PEXPIREAT": {fn: cmdPExpireAt, minArgs: 3, maxArgs: 3},
	"TTL":       {fn: cmdTTL, minArgs: 2, maxArgs: 2},
	"PTTL":      {fn: cmdPTTL, minArgs: 2, maxArgs: 2},
	"PERSIST":   {fn: cmdPersist, minArgs: 2, maxArgs: 2},
}

// dispatch routes argv to its handler, validating the command exists
// and the arity is correct. Returns an error frame on lookup or arity
// failure.
func (s *Server) dispatch(argv [][]byte) resp.Value {
	if len(argv) == 0 {
		return resp.Error("ERR empty command")
	}
	name := upperASCII(argv[0])
	h, ok := commands[name]
	if !ok {
		return resp.Error(fmt.Sprintf("ERR unknown command '%s'", argv[0]))
	}
	if len(argv) < h.minArgs || (h.maxArgs >= 0 && len(argv) > h.maxArgs) {
		return resp.Error(fmt.Sprintf("ERR wrong number of arguments for '%s'", lowerASCII(argv[0])))
	}
	return h.fn(s, argv)
}

// upperASCII returns an upper-case copy of b. Command names are
// case-insensitive in RESP and only valid ASCII.
func upperASCII(b []byte) string {
	out := bytes.ToUpper(b)
	return string(out)
}

// lowerASCII is the mirror for error-reply formatting.
func lowerASCII(b []byte) string {
	out := bytes.ToLower(b)
	return string(out)
}
