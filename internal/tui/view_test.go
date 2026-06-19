package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

func TestView_BeforeSizeMsg(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	if !strings.Contains(m.View(), "waiting for terminal size") {
		t.Errorf("zero-size view should show wait banner")
	}
}

func TestView_PopulatedListAndStatus(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "always")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m, _ = runMsg(m, refreshMsg{
		keys:   []KeyInfo{{Name: "alpha", TTL: -1}, {Name: "beta", TTL: 30}},
		dbsize: 2,
		value:  resp.Bulk([]byte("hello")),
		hasVal: true,
	})
	out := m.View()
	if !strings.Contains(out, "alpha") {
		t.Errorf("view missing 'alpha'\n%s", out)
	}
	if !strings.Contains(out, "30s") {
		t.Errorf("view missing ttl for beta\n%s", out)
	}
	if !strings.Contains(out, "dbsize=2") {
		t.Errorf("view missing dbsize")
	}
	if !strings.Contains(out, "fsync=always") {
		t.Errorf("view missing fsync label")
	}
	if !strings.Contains(out, `"hello"`) {
		t.Errorf("view missing rendered value")
	}
	// Footer hints + help discoverability.
	if !strings.Contains(out, "? help") {
		t.Errorf("footer hint bar missing '? help'\n%s", out)
	}
	if !strings.Contains(out, "q quit") {
		t.Errorf("footer hint bar missing 'q quit'\n%s", out)
	}
}

func TestView_HelpOverlay(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	m, _ = runMsg(m, keyMsg("?"))
	out := m.View()
	for _, want := range []string{"Navigate", "Mutate", "View", "Meta", "j/k", "flushdb"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing %q\n%s", want, out)
		}
	}
	// Toggle off.
	m, _ = runMsg(m, keyMsg("?"))
	if strings.Contains(m.View(), "Navigate\nMutate") {
		t.Errorf("help overlay should be dismissed")
	}
}

func TestHelpOverlay_QQuitsWhileOpen(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	m, _ = runMsg(m, keyMsg("?"))
	if !m.showHelp {
		t.Fatalf("setup: help should be open")
	}
	_, cmd := runMsg(m, keyMsg("q"))
	if cmd == nil {
		t.Fatal("q with help open should issue quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q cmd did not produce QuitMsg")
	}
}

func TestHelpOverlay_EscDismissesWithoutQuit(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	m, _ = runMsg(m, keyMsg("?"))
	m, cmd := runMsg(m, keyMsg("esc"))
	if m.showHelp {
		t.Errorf("esc should dismiss help overlay")
	}
	if cmd != nil {
		t.Errorf("esc should not issue a tea.Cmd")
	}
}

func TestView_HelpOverlaySupersedesErrAndPrompt(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	// Trigger an error banner.
	m, _ = runMsg(m, replyMsg{err: "boom"})
	if !strings.Contains(m.View(), "boom") {
		t.Fatalf("setup: error banner expected before help opens")
	}
	// Open help — overlay should hide both banner and any prompt.
	m, _ = runMsg(m, keyMsg("?"))
	out := m.View()
	if strings.Contains(out, "boom") {
		t.Errorf("help overlay must hide the error banner while open\n%s", out)
	}
	if !strings.Contains(out, "toykv-tui · keybindings") {
		t.Errorf("help overlay title missing\n%s", out)
	}
	// Dismiss — banner returns.
	m, _ = runMsg(m, keyMsg("?"))
	if !strings.Contains(m.View(), "boom") {
		t.Errorf("error banner should return after help dismissed")
	}
}

func TestView_TerminalTooSmall(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View(), "too small") {
		t.Errorf("expected 'too small' banner at 40x10\n%s", m.View())
	}
}

func TestHighlightFilter_NoNestedEscapes(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.filter = "b*r"
	out := m.highlightFilter("bar", 10)
	// Sanity: padded to width.
	if len(stripANSI(out)) != 10 {
		t.Errorf("padded width != 10: %q (visible %q)", out, stripANSI(out))
	}
	// No nested escape: at any open ESC[, the *next* control byte before
	// the close 'm' must not start another ESC[.
	depth := 0
	for i := 0; i < len(out); i++ {
		if i+1 < len(out) && out[i] == 0x1b && out[i+1] == '[' {
			depth++
			if depth > 1 {
				t.Errorf("nested ANSI escape detected at byte %d: %q", i, out)
				break
			}
			continue
		}
		if depth > 0 && out[i] == 'm' {
			depth--
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		if !inEsc && i+1 < len(s) && s[i] == 0x1b && s[i+1] == '[' {
			inEsc = true
			i++
			continue
		}
		if inEsc {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				inEsc = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestView_BreakpointTransitions(t *testing.T) {
	cases := []struct {
		w, h int
		want LayoutKind
	}{
		{50, 20, LayoutTiny},
		{70, 20, LayoutStack},
		{90, 20, LayoutNarrow},
		{110, 20, LayoutMid},
		{130, 20, LayoutWide},
	}
	for _, c := range cases {
		m := NewModel(newFake(), ":6390", 2*time.Second, "")
		m.width, m.height = c.w, c.h
		if got := m.breakpoint(); got != c.want {
			t.Errorf("at %dx%d: breakpoint=%v want %v", c.w, c.h, got, c.want)
		}
	}
}

func TestView_ConfirmShowsPrompt(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	m, _ = runMsg(m, keyMsg("F"))
	if !strings.Contains(m.View(), "FLUSHDB") {
		t.Errorf("confirm prompt missing\n%s", m.View())
	}
}

func TestView_ErrorBanner(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m, _ = runMsg(m, replyMsg{err: "ERR boom"})
	if !strings.Contains(m.View(), "ERR boom") {
		t.Errorf("error banner missing\n%s", m.View())
	}
}
