package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// fakeDoer is a recordable Doer for Update tests. Each scripted reply
// is matched against the next Do call's argv[0]; unknown commands
// return a generic +OK.
type fakeDoer struct {
	mu      sync.Mutex
	calls   [][]string
	replies map[string]resp.Value
}

func newFake() *fakeDoer { return &fakeDoer{replies: map[string]resp.Value{}} }

func (f *fakeDoer) Do(argv ...string) (resp.Value, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	if v, ok := f.replies[argv[0]]; ok {
		return v, nil
	}
	return resp.OK(), nil
}

func keyMsg(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeString(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.Update(keyMsg(string(r)))
		m = next.(Model)
	}
	return m
}

func runMsg(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestNormalKey_QuitReturnsQuitCmd(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q should return a tea.Cmd")
	}
	// We can't compare cmd to tea.Quit directly, but invoking it yields
	// a tea.QuitMsg.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q cmd did not produce QuitMsg")
	}
}

func TestRefreshMsg_PopulatesKeysAndCursor(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.height = 20
	m.width = 80
	m, _ = runMsg(m, refreshMsg{
		keys:   []KeyInfo{{Name: "a", TTL: -1}, {Name: "b", TTL: 60}},
		dbsize: 2,
	})
	if len(m.Keys()) != 2 {
		t.Fatalf("keys not loaded: %+v", m.Keys())
	}
	if m.FocusedKey() != "a" {
		t.Errorf("focused=%q want a", m.FocusedKey())
	}
	if m.status.DBSize != 2 {
		t.Errorf("dbsize not propagated")
	}
}

func TestNormalKey_JKMovesCursor(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	})
	m, _ = runMsg(m, keyMsg("j"))
	if m.FocusedKey() != "b" {
		t.Fatalf("after j focused=%q", m.FocusedKey())
	}
	m, _ = runMsg(m, keyMsg("k"))
	if m.FocusedKey() != "a" {
		t.Fatalf("after k focused=%q", m.FocusedKey())
	}
}

func TestNewKVMode_SubmitTokenisesAndSendsSET(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, keyMsg("n"))
	if m.ModeNow() != ModeNewKV {
		t.Fatalf("expected NewKV mode, got %v", m.ModeNow())
	}
	m = typeString(m, "foo bar")
	m, cmd := runMsg(m, keyMsg("enter"))
	if m.ModeNow() != ModeNormal {
		t.Fatalf("after enter mode=%v", m.ModeNow())
	}
	if cmd == nil {
		t.Fatal("submit should issue a tea.Cmd")
	}
	cmd() // drive the underlying client.Do
	last := f.calls[len(f.calls)-1]
	want := []string{"SET", "foo", "bar"}
	if !equalArgv(last, want) {
		t.Errorf("last call %v want %v", last, want)
	}
}

func TestConfirmMode_FlushDB(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, keyMsg("F"))
	if m.ModeNow() != ModeConfirm {
		t.Fatalf("expected Confirm mode")
	}
	_, cmd := runMsg(m, keyMsg("y"))
	if cmd == nil {
		t.Fatal("confirm y should fire FLUSHDB")
	}
	cmd()
	last := f.calls[len(f.calls)-1]
	if !equalArgv(last, []string{"FLUSHDB"}) {
		t.Errorf("got %v", last)
	}
}

func TestConfirmMode_DeniedDoesNothing(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, keyMsg("F"))
	m, cmd := runMsg(m, keyMsg("n"))
	if m.ModeNow() != ModeNormal {
		t.Fatalf("n should drop back to Normal")
	}
	if cmd != nil {
		t.Errorf("n should not fire a command")
	}
	if len(f.calls) != 0 {
		t.Errorf("no calls expected, got %v", f.calls)
	}
}

func TestRawCmdMode_TokenisesAndDispatches(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, keyMsg(":"))
	if m.ModeNow() != ModeRawCmd {
		t.Fatalf("expected RawCmd mode")
	}
	m = typeString(m, "PING")
	_, cmd := runMsg(m, keyMsg("enter"))
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	cmd()
	if !equalArgv(f.calls[len(f.calls)-1], []string{"PING"}) {
		t.Errorf("got %v", f.calls)
	}
}

func TestExpireMode_RejectsNonInteger(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}})
	m, _ = runMsg(m, keyMsg("t"))
	m = typeString(m, "abc")
	m, cmd := runMsg(m, keyMsg("enter"))
	if cmd != nil {
		t.Errorf("non-integer EXPIRE should not issue a cmd")
	}
	if m.LastErr() == "" {
		t.Errorf("expected error banner")
	}
}

func TestEscFromInputModeReturnsToNormal(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, keyMsg("/"))
	if m.ModeNow() != ModeFilter {
		t.Fatalf("expected Filter mode")
	}
	m, _ = runMsg(m, keyMsg("esc"))
	if m.ModeNow() != ModeNormal {
		t.Fatalf("esc should drop to Normal, got %v", m.ModeNow())
	}
}

func TestRefreshMsg_StaleGenIgnored(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.height, m.width = 20, 100
	// Seed with gen=1.
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "a"}, {Name: "b"}}, dbsize: 2, gen: 1,
	})
	if len(m.Keys()) != 2 {
		t.Fatalf("seed not applied: %+v", m.Keys())
	}
	// Schedule a new fetch (bumps to gen=2), then deliver a stale gen=1 reply.
	m, _ = m.scheduleFetch()
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "z"}}, dbsize: 99, gen: 1,
	})
	if len(m.Keys()) != 2 || m.status.DBSize != 2 {
		t.Errorf("stale gen=1 reply should be dropped; keys=%v dbsize=%d",
			m.Keys(), m.status.DBSize)
	}
	// Current gen reply lands.
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "z"}}, dbsize: 99, gen: m.fetchGen,
	})
	if len(m.Keys()) != 1 || m.Keys()[0].Name != "z" {
		t.Errorf("current gen reply should land; got %+v", m.Keys())
	}
}

func TestRefreshMsg_ErrClearsValueState(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.height, m.width = 20, 100
	// Populate, including a fetched value.
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "k"}}, dbsize: 1,
		value:  resp.Bulk([]byte("hi")),
		hasVal: true, gen: 1,
	})
	if !m.hasVal {
		t.Fatalf("expected hasVal=true after seed")
	}
	// A later refresh fails — stale value must be cleared and banner stamped.
	m, _ = m.scheduleFetch()
	m, _ = runMsg(m, refreshMsg{err: "ERR boom", gen: m.fetchGen})
	if m.hasVal {
		t.Errorf("hasVal should be cleared on refresh error")
	}
	if !strings.Contains(m.LastErr(), "refresh:") {
		t.Errorf("err banner missing 'refresh:' prefix: %q", m.LastErr())
	}
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
