package tui

import (
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// tickMsg fires every refresh interval and triggers a fetchRefresh.
type tickMsg struct{ n uint64 }

// refreshMsg carries the result of a periodic KEYS / TTL / GET sweep.
type refreshMsg struct {
	keys    []KeyInfo
	dbsize  int64
	value   resp.Value
	hasVal  bool
	latency time.Duration
	err     string
}

// replyMsg carries the result of a one-shot mutating command issued in
// response to a user keystroke (SET, DEL, INCR, EXPIRE, FLUSHDB, raw).
type replyMsg struct {
	value   resp.Value
	latency time.Duration
	err     string
	// refresh forces an immediate post-reply refetch if true.
	refresh bool
}
