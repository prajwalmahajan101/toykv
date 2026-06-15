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
	if !strings.Contains(out, "ttl=30s") {
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
