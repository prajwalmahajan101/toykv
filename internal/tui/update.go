package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/cmdparse"
)

// Init kicks off the first refresh tick. NewModel seeds fetchGen=1 so
// the first refreshMsg matches without a bump here.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchRefresh(m.client, m.filter, m.pageCursor, m.pageCount, m.focused, m.fetchGen),
		tickCmd(m.refresh, m.tickN+1),
	)
}

// scheduleFetch bumps the fetch generation and returns both the updated
// model and the tea.Cmd that runs the refresh. Callers chain through:
//
//	m, cmd := m.scheduleFetch()
//	return m, cmd
//
// Any refreshMsg arriving with a stale gen is silently dropped by Update.
func (m Model) scheduleFetch() (Model, tea.Cmd) {
	m.fetchGen++
	return m, fetchRefresh(m.client, m.filter, m.pageCursor, m.pageCount, m.focused, m.fetchGen)
}

// resetPaging returns to the first SCAN page. Called when the match
// pattern changes or the keyspace is wiped (FLUSHDB), where prior
// cursors are meaningless.
func (m Model) resetPaging() Model {
	m.pageCursor = 0
	m.nextCursor = 0
	m.cursorStack = nil
	m.cursor = 0
	return m
}

// enterAuthPrompt switches to the masked password prompt. Idempotent so a
// burst of -NOAUTH replies (refresh + a pending mutation) doesn't wipe a
// password the user is mid-way through typing.
func (m Model) enterAuthPrompt() Model {
	if m.mode == ModeAuth {
		return m
	}
	m.mode = ModeAuth
	m.prompt = "password: "
	m.input.EchoMode = textinput.EchoPassword
	m.input.SetValue("")
	m.input.Focus()
	return m
}

// isNoAuth reports whether a reply error is the server's auth gate.
func isNoAuth(errStr string) bool {
	return strings.HasPrefix(errStr, "NOAUTH")
}

// Update is the Bubble Tea reducer. See LLD §7.3.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		// Stale ticks (from a previous tickN) are dropped.
		if msg.n != m.tickN+1 {
			return m, nil
		}
		m.tickN = msg.n
		// While the password prompt is up, don't hammer the server with
		// refetches that just re-trip -NOAUTH (and would clobber the
		// half-typed password). Keep the tick chain alive.
		if m.mode == ModeAuth {
			return m, tickCmd(m.refresh, m.tickN+1)
		}
		return m.scheduleFetch()

	case refreshMsg:
		// Drop stale generations — a newer fetch has already been scheduled.
		// gen=0 is treated as untagged (test fixtures) and always applied.
		if msg.gen != 0 && msg.gen != m.fetchGen {
			return m, nil
		}
		if msg.err != "" {
			// Clear value state — the underlying key may no longer exist.
			m.hasVal = false
			if isNoAuth(msg.err) {
				m = m.enterAuthPrompt()
			} else {
				m.err = "refresh: " + msg.err
			}
		} else {
			m.err = ""
			m.keys = msg.keys
			m.nextCursor = msg.nextCursor
			m.status.DBSize = msg.info.dbsize
			m.status.Uptime = msg.info.uptime
			m.status.Clients = msg.info.clients
			m.status.FsyncLabel = msg.info.fsync
			if m.cursor >= len(m.keys) {
				m.cursor = len(m.keys) - 1
				if m.cursor < 0 {
					m.cursor = 0
				}
			}
			if len(m.keys) == 0 {
				m.focused = ""
				m.hasVal = false
			} else {
				m.focused = m.keys[m.cursor].Name
			}
			if msg.hasVal {
				m.value = msg.value
				m.hasVal = true
			}
		}
		m.status.Latency = msg.latency
		return m, tickCmd(m.refresh, m.tickN+1)

	case replyMsg:
		if msg.err != "" {
			if isNoAuth(msg.err) {
				m.status.Latency = msg.latency
				return m.enterAuthPrompt(), nil
			}
			m.err = msg.err
		} else {
			m.err = ""
		}
		m.status.Latency = msg.latency
		if msg.refresh {
			return m.scheduleFetch()
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case ModeNormal:
		return m.handleNormalKey(msg)
	case ModeConfirm:
		return m.handleConfirmKey(msg)
	default:
		return m.handleInputKey(msg)
	}
}

func (m Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Help overlay: q (and Ctrl+C) always quit the app — Esc / ? only
	// dismiss the overlay. The universal "quit" reflex must work even
	// with help open.
	if m.showHelp {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?", "esc":
			m.showHelp = false
		}
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.showHelp = true
		return m, nil
	case "tab":
		// Focus is only meaningful in the stacked layout where the value
		// pane occupies its own row and j/k scrolls it. In two-pane modes
		// the right pane is read-only — toggling focus only repainted the
		// border accent, an empty affordance.
		if m.breakpoint() == LayoutStack {
			if m.focus == FocusLeft {
				m.focus = FocusRight
			} else {
				m.focus = FocusLeft
			}
		}
		return m, nil
	case "g":
		if len(m.keys) > 0 {
			m.cursor = 0
			m.focused = m.keys[0].Name
			m.hasVal = false
			return m.scheduleFetch()
		}
		return m, nil
	case "G":
		if len(m.keys) > 0 {
			m.cursor = len(m.keys) - 1
			m.focused = m.keys[m.cursor].Name
			m.hasVal = false
			return m.scheduleFetch()
		}
		return m, nil
	case "j", "down":
		if m.breakpoint() == LayoutStack && m.focus == FocusRight {
			m.valueScroll++
			return m, nil
		}
		if m.cursor < len(m.keys)-1 {
			m.cursor++
			m.focused = m.keys[m.cursor].Name
			m.hasVal = false
			m.valueScroll = 0
			return m.scheduleFetch()
		}
		return m, nil
	case "k", "up":
		if m.breakpoint() == LayoutStack && m.focus == FocusRight {
			if m.valueScroll > 0 {
				m.valueScroll--
			}
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
			m.focused = m.keys[m.cursor].Name
			m.hasVal = false
			m.valueScroll = 0
			return m.scheduleFetch()
		}
		return m, nil
	case "r":
		return m.scheduleFetch()
	case "]", "pgdown":
		// Next SCAN page. nextCursor == 0 means this is the last page.
		if m.nextCursor != 0 {
			m.cursorStack = append(m.cursorStack, m.pageCursor)
			m.pageCursor = m.nextCursor
			m.cursor = 0
			m.focused = ""
			m.hasVal = false
			m.valueScroll = 0
			return m.scheduleFetch()
		}
		return m, nil
	case "[", "pgup":
		// Previous SCAN page, popped off the cursor stack.
		if len(m.cursorStack) > 0 {
			m.pageCursor = m.cursorStack[len(m.cursorStack)-1]
			m.cursorStack = m.cursorStack[:len(m.cursorStack)-1]
			m.cursor = 0
			m.focused = ""
			m.hasVal = false
			m.valueScroll = 0
			return m.scheduleFetch()
		}
		return m, nil
	case "/":
		return m.enterInput(ModeFilter, "match (glob): ", m.filter), nil
	case "n":
		return m.enterInput(ModeNewKV, "SET key value: ", ""), nil
	case "e":
		if m.focused == "" {
			return m, nil
		}
		return m.enterInput(ModeEdit, "new value for "+m.focused+": ", ""), nil
	case "t":
		if m.focused == "" {
			return m, nil
		}
		return m.enterInput(ModeExpire, "EXPIRE "+m.focused+" seconds: ", ""), nil
	case "d":
		if m.focused == "" {
			return m, nil
		}
		m.mode = ModeConfirm
		m.confirm = ConfirmDel
		m.prompt = fmt.Sprintf("DEL %s ? (y/N)", m.focused)
		return m, nil
	case "F":
		m.mode = ModeConfirm
		m.confirm = ConfirmFlushDB
		m.prompt = "FLUSHDB ? (y/N)"
		return m, nil
	case "i":
		if m.focused == "" {
			return m, nil
		}
		return m, runMutating(m.client, true, "INCR", m.focused)
	case "D":
		if m.focused == "" {
			return m, nil
		}
		return m, runMutating(m.client, true, "DECR", m.focused)
	case ":":
		return m.enterInput(ModeRawCmd, ": ", ""), nil
	}
	return m, nil
}

func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		action := m.confirm
		m.mode = ModeNormal
		m.confirm = ConfirmNone
		m.prompt = ""
		switch action {
		case ConfirmFlushDB:
			// The keyspace is about to be emptied — prior page cursors are
			// meaningless, so the post-flush refetch starts from page 0.
			m = m.resetPaging()
			m.focused = ""
			return m, runMutating(m.client, true, "FLUSHDB")
		case ConfirmDel:
			return m, runMutating(m.client, true, "DEL", m.focused)
		}
		return m, nil
	case "n", "N", "esc", "q":
		m.mode = ModeNormal
		m.confirm = ConfirmNone
		m.prompt = ""
		return m, nil
	}
	return m, nil
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.prompt = ""
		m.input.Blur()
		m.input.SetValue("")
		m.input.EchoMode = textinput.EchoNormal
		return m, nil
	case "enter":
		return m.submitInput()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) submitInput() (tea.Model, tea.Cmd) {
	val := strings.TrimSpace(m.input.Value())
	mode := m.mode
	focused := m.focused
	m.mode = ModeNormal
	m.prompt = ""
	m.input.Blur()
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal

	switch mode {
	case ModeAuth:
		// Empty password: just dismiss the prompt.
		if val == "" {
			return m, nil
		}
		return m, authCmd(m.client, val)
	case ModeFilter:
		// A new match pattern invalidates the cursor stack — re-scan from 0.
		m.filter = val
		m = m.resetPaging()
		m.focused = ""
		m.hasVal = false
		return m.scheduleFetch()
	case ModeNewKV:
		argv, err := cmdparse.Tokenise(val)
		if err != nil || len(argv) < 2 {
			m.err = "SET needs key and value"
			return m, nil
		}
		setArgs := append([]string{"SET"}, argv...)
		return m, runMutating(m.client, true, setArgs...)
	case ModeEdit:
		if focused == "" {
			return m, nil
		}
		return m, runMutating(m.client, true, "SET", focused, val)
	case ModeExpire:
		if focused == "" {
			return m, nil
		}
		if _, err := strconv.Atoi(val); err != nil {
			m.err = "EXPIRE: seconds must be integer"
			return m, nil
		}
		return m, runMutating(m.client, true, "EXPIRE", focused, val)
	case ModeRawCmd:
		argv, err := cmdparse.Tokenise(val)
		if err != nil {
			m.err = "parse: " + err.Error()
			return m, nil
		}
		if len(argv) == 0 {
			return m, nil
		}
		return m, runMutating(m.client, true, argv...)
	}
	return m, nil
}

func (m Model) enterInput(mode Mode, prompt, initial string) Model {
	m.mode = mode
	m.prompt = prompt
	m.input.SetValue(initial)
	m.input.CursorEnd()
	m.input.Focus()
	return m
}
