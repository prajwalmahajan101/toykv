package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// Doer is the subset of *internal/client.Client the TUI depends on.
// Pulling it behind an interface lets Update tests drive Model without
// a real network.
type Doer interface {
	Do(argv ...string) (resp.Value, error)
}

// Mode enumerates the TUI's input modes. Normal mode dispatches the
// PRD §5.5 keybindings; the other modes consume keystrokes into the
// textinput / confirm prompts.
type Mode int

const (
	ModeNormal Mode = iota
	ModeFilter
	ModeNewKV   // n: SET <key> <value>  (tokenised via cmdparse)
	ModeEdit    // e: SET <focused> <value>
	ModeExpire  // t: EXPIRE <focused> <seconds>
	ModeRawCmd  // : raw RESP command
	ModeConfirm // y/n prompt (FLUSHDB, DEL)
)

// ConfirmAction names which action a Confirm prompt commits to on "y".
type ConfirmAction int

const (
	ConfirmNone ConfirmAction = iota
	ConfirmFlushDB
	ConfirmDel
)

// KeyInfo is one row in the left pane.
type KeyInfo struct {
	Name string
	TTL  int64 // -1 = no expiry, -2 = key missing (raced)
	Size int   // bytes of stringified value; 0 if unknown
}

// StatusLine aggregates the status bar fields.
type StatusLine struct {
	Addr       string
	DBSize     int64
	FsyncLabel string // user-supplied via -fsync flag (see ADR-0009 §4)
	Latency    time.Duration
}

// Model is the Bubble Tea model. See LLD §7.1.
type Model struct {
	client  Doer
	refresh time.Duration

	keys    []KeyInfo
	cursor  int
	filter  string // empty ⇒ "*"
	focused string // name of cursor key, "" if list empty
	value   resp.Value
	hasVal  bool

	mode    Mode
	input   textinput.Model
	confirm ConfirmAction
	prompt  string // status message for input modes

	status StatusLine
	err    string // last-error banner; cleared on next successful reply
	width  int
	height int

	tickN uint64 // incremented every tickMsg; used to filter stale refreshes
}

// NewModel constructs a fresh model bound to client and refresh interval.
func NewModel(client Doer, addr string, refresh time.Duration, fsyncLabel string) Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 0

	return Model{
		client:  client,
		refresh: refresh,
		input:   ti,
		mode:    ModeNormal,
		status:  StatusLine{Addr: addr, FsyncLabel: fsyncLabel},
	}
}

// FocusedKey returns the currently-focused key name (or "" if none).
func (m Model) FocusedKey() string { return m.focused }

// Mode exposes the current input mode (used by tests).
func (m Model) ModeNow() Mode { return m.mode }

// Keys exposes the current key list (used by tests).
func (m Model) Keys() []KeyInfo { return m.keys }

// LastErr exposes the current error banner (used by tests).
func (m Model) LastErr() string { return m.err }
