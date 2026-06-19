package tui

import (
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// tickMsg fires every refresh interval and triggers a fetchRefresh.
type tickMsg struct{ n uint64 }

// refreshMsg carries the result of a periodic KEYS / TTL / GET sweep.
// gen is the fetch generation that scheduled this sweep; Update drops
// any reply whose gen no longer matches Model.fetchGen so stale GETs
// from rapid cursor moves don't paint over the current focus.
type refreshMsg struct {
	keys    []KeyInfo
	dbsize  int64
	value   resp.Value
	hasVal  bool
	latency time.Duration
	err     string
	gen     uint64
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
