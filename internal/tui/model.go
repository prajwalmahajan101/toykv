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
	TTL  int64  // -1 = no expiry, -2 = key missing (raced)
	Size int    // bytes of stringified value; 0 if unknown
	Kind string // RESP type label (currently always "string"; forward-compat)
}

// Focus tracks which pane keystrokes are scoped to. Two-pane layouts
// behave as if focusLeft (the value pane is read-only); focusRight is
// only meaningful in the stacked breakpoint where Tab toggles it.
type Focus int

const (
	FocusLeft Focus = iota
	FocusRight
)

// LayoutKind selects body geometry based on terminal size; see
// (Model).breakpoint().
type LayoutKind int

const (
	LayoutWide   LayoutKind = iota // ≥ 120 cols: two-pane 40/60 + size column
	LayoutMid                      // 100–119 cols: two-pane 40/60, no size column
	LayoutNarrow                   // 80–99 cols:  two-pane 50/50, name + ttl only
	LayoutStack                    // 60–79 cols:  stacked single pane
	LayoutTiny                     // < 60 cols or < 16 rows: too-small banner
)

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

	focus       Focus
	showHelp    bool
	valueScroll int // line offset for the value pane (Stack + FocusRight only)
	st          styles

	tickN    uint64 // incremented every tickMsg; used to filter stale refreshes
	fetchGen uint64 // bumped before scheduling a fetch; replies with older gen are dropped
}

// NewModel constructs a fresh model bound to client and refresh interval.
func NewModel(client Doer, addr string, refresh time.Duration, fsyncLabel string) Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 0

	return Model{
		client:   client,
		refresh:  refresh,
		input:    ti,
		mode:     ModeNormal,
		status:   StatusLine{Addr: addr, FsyncLabel: fsyncLabel},
		focus:    FocusLeft,
		st:       newStyles(noColorEnv()),
		fetchGen: 1, // first Init() refresh inherits gen=1; bumps happen on every scheduleFetch.
	}
}

// FocusedKey returns the currently-focused key name (or "" if none).
func (m Model) FocusedKey() string { return m.focused }

// Mode exposes the current input mode (used by tests).
func (m Model) Mode() Mode { return m.mode }

// Keys exposes the current key list (used by tests).
func (m Model) Keys() []KeyInfo { return m.keys }

// LastErr exposes the current error banner (used by tests).
func (m Model) LastErr() string { return m.err }

// breakpoint reports the layout to use for the current terminal size.
// Must be cheap — called on every render.
func (m Model) breakpoint() LayoutKind {
	if m.width < 60 || m.height < 16 {
		return LayoutTiny
	}
	switch {
	case m.width >= 120:
		return LayoutWide
	case m.width >= 100:
		return LayoutMid
	case m.width >= 80:
		return LayoutNarrow
	default:
		return LayoutStack
	}
}

// colWidths returns the per-column widths used to render the key list at
// the given left-pane width. Columns include name, size, ttl; when size
// is suppressed (e.g. Narrow layout) it returns 0.
func (m Model) colWidths(leftWidth int) (name, size, ttl int) {
	ttl = 7
	switch m.breakpoint() {
	case LayoutWide:
		size = 7
	case LayoutMid, LayoutStack:
		size = 7
	case LayoutNarrow:
		size = 0
	default:
		size = 0
	}
	// Leading cursor gutter (2) + per-column separator (2 * cols).
	const gutter = 2
	sep := 2
	cols := 2 + boolToInt(size > 0)
	name = leftWidth - gutter - ttl - size - sep*(cols-1)
	if name < 8 {
		name = 8
	}
	return
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
