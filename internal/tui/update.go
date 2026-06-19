package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/cmdparse"
)

// Init kicks off the first refresh tick. NewModel seeds fetchGen=1 so
// the first refreshMsg matches without a bump here.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchRefresh(m.client, m.filter, m.focused, m.fetchGen),
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
	return m, fetchRefresh(m.client, m.filter, m.focused, m.fetchGen)
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
		return m.scheduleFetch()

	case refreshMsg:
		// Drop stale generations — a newer fetch has already been scheduled.
		// gen=0 is treated as untagged (test fixtures) and always applied.
		if msg.gen != 0 && msg.gen != m.fetchGen {
			return m, nil
		}
		if msg.err != "" {
			m.err = "refresh: " + msg.err
			// Clear value state — the underlying key may no longer exist.
			m.hasVal = false
		} else {
			m.err = ""
			m.keys = msg.keys
			m.status.DBSize = msg.dbsize
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
	// Help overlay swallows nav keys until dismissed.
	if m.showHelp {
		switch msg.String() {
		case "?", "esc", "q":
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
		if m.focus == FocusLeft {
			m.focus = FocusRight
		} else {
			m.focus = FocusLeft
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
		if m.cursor < len(m.keys)-1 {
			m.cursor++
			m.focused = m.keys[m.cursor].Name
			m.hasVal = false
			return m.scheduleFetch()
		}
		return m, nil
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.focused = m.keys[m.cursor].Name
			m.hasVal = false
			return m.scheduleFetch()
		}
		return m, nil
	case "r":
		return m.scheduleFetch()
	case "/":
		return m.enterInput(ModeFilter, "filter (glob): ", m.filter), nil
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

	switch mode {
	case ModeFilter:
		m.filter = val
		m.cursor = 0
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
