package server

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// handler executes one command and returns the reply frame.
type handler struct {
	// fn is the command implementation. cs carries per-connection state
	// (protocol version, later auth); most handlers ignore it, but HELLO
	// reads and mutates it.
	fn func(s *Server, cs *connState, argv [][]byte) resp.Value
	// minArgs / maxArgs bound the inclusive argv length, *including* the
	// command name itself. maxArgs = -1 means unbounded.
	minArgs, maxArgs int
	// mutating marks the commands that change replicated state — exactly the
	// handlers that call appendIfLive. Under -replicate these are proposed
	// through Raft instead of run locally (see dispatch's propose gate); it is
	// ignored in standalone mode. Read and local-admin commands are false.
	mutating bool
	// readKeyed marks commands that read replicated keyspace state (GET, SCAN,
	// HGETALL, …) as opposed to local-admin reads (PING, INFO, HELLO). Under
	// -replicate a readKeyed command on a non-leader is redirected to the leader
	// so reads are linearizable by default; a connection that opted into READONLY
	// serves it from local (possibly stale) state instead. Ignored in standalone
	// mode. mutating and readKeyed are mutually exclusive.
	readKeyed bool
}

// commands is the dispatch table. M1 ships PING + ECHO; M2 adds the
// store-core commands; M4 adds TTL ops. Per-command arity bounds; SET's
// upper bound covers "SET k v NX EX 10" — 6 tokens including the verb.
var commands = map[string]handler{
	"HELLO":     {fn: cmdHello, minArgs: 1, maxArgs: 5},
	"AUTH":      {fn: cmdAuth, minArgs: 2, maxArgs: 3},
	"PING":      {fn: cmdPing, minArgs: 1, maxArgs: 2},
	"ECHO":      {fn: cmdEcho, minArgs: 2, maxArgs: 2},
	"GET":       {fn: cmdGet, minArgs: 2, maxArgs: 2, readKeyed: true},
	"SET":       {fn: cmdSet, minArgs: 3, maxArgs: -1, mutating: true},
	"DEL":       {fn: cmdDel, minArgs: 2, maxArgs: -1, mutating: true},
	"EXISTS":    {fn: cmdExists, minArgs: 2, maxArgs: -1, readKeyed: true},
	"INCR":      {fn: cmdIncr, minArgs: 2, maxArgs: 2, mutating: true},
	"DECR":      {fn: cmdDecr, minArgs: 2, maxArgs: 2, mutating: true},
	"KEYS":      {fn: cmdKeys, minArgs: 2, maxArgs: 2, readKeyed: true},
	"SCAN":      {fn: cmdScan, minArgs: 2, maxArgs: 6, readKeyed: true},
	"FLUSHDB":   {fn: cmdFlushDB, minArgs: 1, maxArgs: 1, mutating: true},
	"DBSIZE":    {fn: cmdDBSize, minArgs: 1, maxArgs: 1, readKeyed: true},
	"RENAME":    {fn: cmdRename, minArgs: 3, maxArgs: 3, mutating: true},
	"RENAMENX":  {fn: cmdRenameNX, minArgs: 3, maxArgs: 3, mutating: true},
	"COPY":      {fn: cmdCopy, minArgs: 3, maxArgs: 6, mutating: true},
	"INFO":      {fn: cmdInfo, minArgs: 1, maxArgs: 2},
	"EXPIRE":    {fn: cmdExpire, minArgs: 3, maxArgs: 3, mutating: true},
	"PEXPIRE":   {fn: cmdPExpire, minArgs: 3, maxArgs: 3, mutating: true},
	"PEXPIREAT": {fn: cmdPExpireAt, minArgs: 3, maxArgs: 3, mutating: true},
	"TTL":       {fn: cmdTTL, minArgs: 2, maxArgs: 2, readKeyed: true},
	"PTTL":      {fn: cmdPTTL, minArgs: 2, maxArgs: 2, readKeyed: true},
	"PERSIST":   {fn: cmdPersist, minArgs: 2, maxArgs: 2, mutating: true},

	"BGREWRITEAOF": {fn: cmdBGRewriteAOF, minArgs: 1, maxArgs: 1},

	// Cluster read model (M20): opt a connection into (READONLY) / out of
	// (READWRITE) follower-local stale reads. Local-admin, never replicated.
	"READONLY":  {fn: cmdReadOnly, minArgs: 1, maxArgs: 1},
	"READWRITE": {fn: cmdReadWrite, minArgs: 1, maxArgs: 1},

	// Lists (M11).
	"LPUSH":  {fn: cmdLPush, minArgs: 3, maxArgs: -1, mutating: true},
	"RPUSH":  {fn: cmdRPush, minArgs: 3, maxArgs: -1, mutating: true},
	"LPOP":   {fn: cmdLPop, minArgs: 2, maxArgs: 2, mutating: true},
	"RPOP":   {fn: cmdRPop, minArgs: 2, maxArgs: 2, mutating: true},
	"LLEN":   {fn: cmdLLen, minArgs: 2, maxArgs: 2, readKeyed: true},
	"LRANGE": {fn: cmdLRange, minArgs: 4, maxArgs: 4, readKeyed: true},
	"LINDEX": {fn: cmdLIndex, minArgs: 3, maxArgs: 3, readKeyed: true},

	// Hashes + TYPE (M11). HSET's pair validation (even field/value
	// count) lives in the handler — arity bounds can't express it.
	"HSET":    {fn: cmdHSet, minArgs: 4, maxArgs: -1, mutating: true},
	"HGET":    {fn: cmdHGet, minArgs: 3, maxArgs: 3, readKeyed: true},
	"HDEL":    {fn: cmdHDel, minArgs: 3, maxArgs: -1, mutating: true},
	"HEXISTS": {fn: cmdHExists, minArgs: 3, maxArgs: 3, readKeyed: true},
	"HKEYS":   {fn: cmdHKeys, minArgs: 2, maxArgs: 2, readKeyed: true},
	"HVALS":   {fn: cmdHVals, minArgs: 2, maxArgs: 2, readKeyed: true},
	"HLEN":    {fn: cmdHLen, minArgs: 2, maxArgs: 2, readKeyed: true},
	"HGETALL": {fn: cmdHGetAll, minArgs: 2, maxArgs: 2, readKeyed: true},
	"TYPE":    {fn: cmdType, minArgs: 2, maxArgs: 2, readKeyed: true},
}

// dispatch routes argv to its handler, validating the command exists
// and the arity is correct. Returns an error frame on lookup or arity
// failure.
func (s *Server) dispatch(cs *connState, argv [][]byte) resp.Value {
	if len(argv) == 0 {
		return resp.Error("ERR empty command")
	}
	name := upperASCII(argv[0])
	// Auth gating precedes even the existence check — like Redis, an
	// unauthenticated client learns nothing about the command table.
	// The whitelist (incl. unauthenticated PING, a deliberate deviation
	// from Redis that the roadmap mandates) is fixed by ROADMAP §M12.
	if !cs.authenticated && name != "AUTH" && name != "HELLO" && name != "PING" {
		return resp.Error("NOAUTH Authentication required.")
	}
	h, ok := commands[name]
	if !ok {
		return resp.Error(fmt.Sprintf("ERR unknown command '%s'", argv[0]))
	}
	if len(argv) < h.minArgs || (h.maxArgs >= 0 && len(argv) > h.maxArgs) {
		return resp.Error(fmt.Sprintf("ERR wrong number of arguments for '%s'", lowerASCII(argv[0])))
	}
	// Replicated propose gate. On the leader, a fresh client mutation is
	// resolved (wall-clock inputs → absolute) and proposed through Raft rather
	// than run locally; the reply comes back once the entry applies. Reads and
	// local-admin commands (h.mutating == false) always run locally. cs.applying
	// is true on the Apply and AOF-replay paths, which must run the handler
	// directly — this is what keeps the mutate→append exactly-once and avoids
	// re-proposing. Standalone (s.replicated == false) never takes this branch,
	// so its behaviour is byte-identical to v2.
	if s.replicated && h.mutating && !cs.applying {
		argv = s.resolveNondeterministic(argv)
		reply, err := s.cluster.Propose(cs.context(), argv)
		if err != nil {
			// On a follower, ToyRaft rejects the proposal with ErrNotLeader
			// carrying the leader hint. Surface it as a NOTLEADER redirect so an
			// operator — and the client's auto-redirect (M20) — can reach the
			// leader. Any other propose error is an infrastructure failure
			// (node stopping, proposal dropped mid-commit).
			if notLeader, ok := errors.AsType[*raft.ErrNotLeader](err); ok {
				return s.notLeaderReply(notLeader.LeaderHint)
			}
			return resp.Error(fmt.Sprintf("ERR replication failed: %s", err))
		}
		return reply
	}
	// Cluster read model (M20): a keyspace read on a non-leader is redirected to
	// the leader so reads are linearizable by default. A connection that issued
	// READONLY (cs.readonly) opts out and serves the read from local — possibly
	// stale — state instead. cs.applying reads (none today, but the Apply path is
	// leader-agnostic) and standalone mode always run locally.
	if s.replicated && h.readKeyed && !cs.applying && !cs.readonly && s.cluster.Role() != raft.Leader {
		return s.notLeaderReply(s.cluster.LeaderHint())
	}
	return h.fn(s, cs, argv)
}

// notLeaderReply formats the NOTLEADER error. When the leader advertised a
// client address the reply is "-NOTLEADER host:port", a machine-parseable
// redirect the client follows automatically; otherwise it falls back to an
// operator-readable, non-dialable hint (an un-migrated -peers entry, or no
// leader yet). hint is ToyRaft's leader node id, used only for the fallback text.
func (s *Server) notLeaderReply(hint raft.NodeID) resp.Value {
	if addr := s.cluster.LeaderClientAddr(); addr != "" {
		return resp.Error("NOTLEADER " + addr)
	}
	if hint != "" {
		return resp.Error(fmt.Sprintf("NOTLEADER leader is %s (no client address advertised)", hint))
	}
	return resp.Error("NOTLEADER no leader elected")
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
