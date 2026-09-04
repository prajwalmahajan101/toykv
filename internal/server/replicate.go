package server

import (
	"strconv"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// applyReplicated is the StateMachine's ApplyFunc: it executes a committed,
// already-resolved command on this node by re-entering dispatch with a
// connState marked applying+authenticated. applying makes the propose gate
// fall through to the handler (no re-proposing), and because s.aof is live
// here, the handler's appendIfLive writes the AOF exactly once. The reply is
// returned to cluster.StateMachine, which stashes it for Node.Propose.
func (s *Server) applyReplicated(argv [][]byte) resp.Value {
	return s.dispatch(&connState{authenticated: true, applying: true}, argv)
}

// resolveNondeterministic rewrites a mutating command's argv so that any
// wall-clock input embedded in the arguments is resolved to an absolute
// deadline at propose time, using the leader's clock. This is what keeps
// StateMachine.Apply deterministic: every replica applies the same absolute
// deadline instead of each recomputing "now + n" against its own clock.
//
// Only SET (…EX/PX/EXAT → …PXAT) and EXPIRE/PEXPIRE (→ PEXPIREAT) carry such
// inputs; every other mutating command passes through unchanged. Conditional
// tokens that depend on live state (SET NX/XX) are preserved and still
// evaluated during Apply. Malformed arguments are passed through untouched so
// Apply surfaces the same clock-independent error the standalone path would.
//
// This is only ever called on the replicated propose path; standalone dispatch
// never invokes it, so standalone behaviour is byte-identical to v2.
func (s *Server) resolveNondeterministic(argv [][]byte) [][]byte {
	switch upperASCII(argv[0]) {
	case "SET":
		return s.resolveSet(argv)
	case "EXPIRE":
		return s.resolveRelativeExpire(argv, time.Second)
	case "PEXPIRE":
		return s.resolveRelativeExpire(argv, time.Millisecond)
	default:
		return argv
	}
}

// resolveSet canonicalises a SET: it preserves the NX/XX mode and rewrites any
// expiry option (EX/PX/EXAT/PXAT) to an absolute PXAT. If the options don't
// parse, argv is returned unchanged — cmdSet re-parses on Apply and returns the
// identical (clock-independent) syntax error.
func (s *Server) resolveSet(argv [][]byte) [][]byte {
	opts, err := parseSetOptions(argv[3:], s.now())
	if err != nil {
		return argv
	}
	out := [][]byte{argv[0], argv[1], argv[2]}
	switch opts.Mode {
	case store.SetNX:
		out = append(out, []byte("NX"))
	case store.SetXX:
		out = append(out, []byte("XX"))
	case store.SetAlways:
		// no mode token
	}
	if !opts.ExpireAt.IsZero() {
		out = append(out, []byte("PXAT"), []byte(strconv.FormatInt(opts.ExpireAt.UnixMilli(), 10)))
	}
	return out
}

// resolveRelativeExpire rewrites EXPIRE/PEXPIRE key n into the absolute
// PEXPIREAT key <unix-ms>, matching setExpiry's computation. A non-integer n is
// passed through so Apply returns the standalone error. A past deadline is
// preserved verbatim (EXPIRE with a non-positive n deletes on Apply, same as
// standalone).
func (s *Server) resolveRelativeExpire(argv [][]byte, unit time.Duration) [][]byte {
	n, err := strconv.ParseInt(string(argv[2]), 10, 64)
	if err != nil {
		return argv
	}
	expireAt := s.now().Add(time.Duration(n) * unit)
	return [][]byte{
		[]byte("PEXPIREAT"),
		argv[1],
		[]byte(strconv.FormatInt(expireAt.UnixMilli(), 10)),
	}
}
