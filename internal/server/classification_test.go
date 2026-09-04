package server

import (
	"sort"
	"testing"
)

// TestMutatingClassification pins the exact set of commands flagged mutating in
// the dispatch table. Under -replicate these are the commands proposed through
// Raft; the set MUST equal the handlers that call appendIfLive (the replicated
// state changes). If a new mutating command is added without appendIfLive — or
// vice versa — this test forces the classification to be reconsidered.
func TestMutatingClassification(t *testing.T) {
	// The 18 mutating commands (ROADMAP §M18): exactly the appendIfLive callers.
	want := []string{
		"COPY", "DECR", "DEL", "EXPIRE", "FLUSHDB", "HDEL", "HSET", "INCR",
		"LPOP", "LPUSH", "PERSIST", "PEXPIRE", "PEXPIREAT", "RENAME", "RENAMENX",
		"RPOP", "RPUSH", "SET",
	}
	sort.Strings(want)

	var got []string
	for name, h := range commands {
		if h.mutating {
			got = append(got, name)
		}
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("mutating set size = %d, want %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mutating set mismatch at %d: got %q want %q\n got=%v\nwant=%v", i, got[i], want[i], got, want)
		}
	}
}

// TestReadAndAdminNotMutating guards a representative sample of read and
// local-admin commands against being accidentally flagged mutating (which
// would wrongly route them through Raft).
func TestReadAndAdminNotMutating(t *testing.T) {
	nonMutating := []string{
		// reads
		"GET", "EXISTS", "KEYS", "SCAN", "DBSIZE", "TTL", "PTTL", "LLEN",
		"LRANGE", "LINDEX", "HGET", "HEXISTS", "HKEYS", "HVALS", "HLEN",
		"HGETALL", "TYPE",
		// local-admin (never replicated)
		"PING", "ECHO", "HELLO", "AUTH", "INFO", "BGREWRITEAOF",
	}
	for _, name := range nonMutating {
		h, ok := commands[name]
		if !ok {
			t.Errorf("command %q not found in dispatch table", name)
			continue
		}
		if h.mutating {
			t.Errorf("command %q is flagged mutating but must not be", name)
		}
	}
}
