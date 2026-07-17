package tui

import (
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// tickMsg fires every refresh interval and triggers a fetchRefresh.
type tickMsg struct{ n uint64 }

// refreshMsg carries the result of a periodic SCAN / TYPE / TTL / value +
// INFO sweep. gen is the fetch generation that scheduled this sweep;
// Update drops any reply whose gen no longer matches Model.fetchGen so
// stale replies from rapid cursor moves don't paint over the current
// focus. nextCursor is the SCAN cursor for the following page (0 ⇒ last).
type refreshMsg struct {
	keys       []KeyInfo
	nextCursor uint64
	value      resp.Value
	hasVal     bool
	info       infoStatus
	latency    time.Duration
	err        string
	gen        uint64
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
