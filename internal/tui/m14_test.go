package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// --- commands: parse/helper coverage -------------------------------------

func TestParseInfo_ExtractsStatusFields(t *testing.T) {
	body := "# Server\r\nredis_version:2.0\r\nuptime_in_seconds:4212\r\n\r\n" +
		"# Clients\r\nconnected_clients:3\r\n\r\n" +
		"# Persistence\r\nappendfsync:everysec\r\n\r\n" +
		"# Keyspace\r\ndb0:keys=17,expires=2\r\n\r\n"
	got := parseInfo(body)
	if got.uptime != 4212 {
		t.Errorf("uptime=%d want 4212", got.uptime)
	}
	if got.clients != 3 {
		t.Errorf("clients=%d want 3", got.clients)
	}
	if got.fsync != "everysec" {
		t.Errorf("fsync=%q want everysec", got.fsync)
	}
	if got.dbsize != 17 {
		t.Errorf("dbsize=%d want 17 (first field of db0)", got.dbsize)
	}
}

func TestParseInfo_EmptyKeyspaceDefaultsZero(t *testing.T) {
	// db0:keys= is omitted when the keyspace is empty.
	got := parseInfo("# Keyspace\r\n\r\n")
	if got.dbsize != 0 {
		t.Errorf("dbsize=%d want 0 for empty keyspace", got.dbsize)
	}
}

func TestDoScanPage_ParsesCursorAndKeys(t *testing.T) {
	f := newFake()
	f.replies["SCAN"] = resp.Array(
		resp.Bulk([]byte("42")),
		resp.Array(resp.Bulk([]byte("k1")), resp.Bulk([]byte("k2"))),
	)
	keys, next, err := doScanPage(f, 0, "*", 50)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if next != 42 {
		t.Errorf("next=%d want 42", next)
	}
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Errorf("keys=%v", keys)
	}
	// The COUNT/MATCH form must actually be sent.
	last := f.calls[len(f.calls)-1]
	if !equalArgv(last, []string{"SCAN", "0", "MATCH", "*", "COUNT", "50"}) {
		t.Errorf("SCAN argv=%v", last)
	}
}

func TestDoType_ReturnsLabel(t *testing.T) {
	f := newFake()
	f.replies["TYPE"] = resp.String("list")
	got, err := doType(f, "q")
	if err != nil || got != KindList {
		t.Errorf("doType=%q err=%v want list", got, err)
	}
}

func TestDoValue_ByKind(t *testing.T) {
	f := newFake()
	f.replies["GET"] = resp.Bulk([]byte("hello"))
	f.replies["LRANGE"] = resp.Array(resp.Bulk([]byte("a")), resp.Bulk([]byte("b")), resp.Bulk([]byte("c")))
	f.replies["HGETALL"] = resp.Array(
		resp.Bulk([]byte("f1")), resp.Bulk([]byte("v1")),
		resp.Bulk([]byte("f2")), resp.Bulk([]byte("v2")),
	)

	if _, size, _ := doValue(f, "s", KindString); size != 5 {
		t.Errorf("string size=%d want 5", size)
	}
	if _, size, _ := doValue(f, "l", KindList); size != 3 {
		t.Errorf("list size=%d want 3 (elements)", size)
	}
	if _, size, _ := doValue(f, "h", KindHash); size != 2 {
		t.Errorf("hash size=%d want 2 (fields)", size)
	}
}

// --- update: paging ------------------------------------------------------

func seedPage(t *testing.T, m Model, keys []string, next uint64) Model {
	t.Helper()
	infos := make([]KeyInfo, len(keys))
	for i, k := range keys {
		infos[i] = KeyInfo{Name: k, TTL: -1}
	}
	m, _ = runMsg(m, refreshMsg{keys: infos, nextCursor: next})
	return m
}

func TestPaging_NextPushesCursorStack(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.width, m.height = 100, 30
	m = seedPage(t, m, []string{"a", "b"}, 7) // next cursor 7 ⇒ more pages

	genBefore := m.fetchGen
	m, cmd := runMsg(m, keyMsg("]"))
	if m.pageCursor != 7 {
		t.Errorf("pageCursor=%d want 7 after next", m.pageCursor)
	}
	if len(m.cursorStack) != 1 || m.cursorStack[0] != 0 {
		t.Errorf("cursorStack=%v want [0]", m.cursorStack)
	}
	if cmd == nil || m.fetchGen == genBefore {
		t.Errorf("next page should schedule a fetch (gen %d→%d)", genBefore, m.fetchGen)
	}

	// Land on page 2 (last page: next=0), then step back to page 1.
	m = seedPage(t, m, []string{"c", "d"}, 0)
	m, cmd = runMsg(m, keyMsg("]"))
	if cmd != nil || m.pageCursor != 7 {
		t.Errorf("next on last page must be a no-op; cursor=%d", m.pageCursor)
	}
	m, cmd = runMsg(m, keyMsg("["))
	if m.pageCursor != 0 || len(m.cursorStack) != 0 || cmd == nil {
		t.Errorf("prev should pop to cursor 0; cursor=%d stack=%v", m.pageCursor, m.cursorStack)
	}
}

func TestPaging_PrevOnFirstPageIsNoop(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m = seedPage(t, m, []string{"a"}, 0)
	_, cmd := runMsg(m, keyMsg("["))
	if cmd != nil {
		t.Errorf("prev on first page must be a no-op")
	}
}

func TestFilterSubmit_ResetsPaging(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m.width, m.height = 100, 30
	m = seedPage(t, m, []string{"a", "b"}, 9)
	m, _ = runMsg(m, keyMsg("]")) // advance to page 2
	if m.pageCursor == 0 {
		t.Fatal("precondition: should be off page 1")
	}
	m, _ = runMsg(m, keyMsg("/"))
	m = typeString(m, "foo*")
	m, _ = runMsg(m, keyMsg("enter"))
	if m.filter != "foo*" {
		t.Errorf("filter=%q want foo*", m.filter)
	}
	if m.pageCursor != 0 || len(m.cursorStack) != 0 {
		t.Errorf("filter change must reset paging; cursor=%d stack=%v", m.pageCursor, m.cursorStack)
	}
}

// --- update: auth --------------------------------------------------------

func TestRefreshNoAuth_EntersMaskedPrompt(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, refreshMsg{err: "NOAUTH Authentication required."})
	if m.Mode() != ModeAuth {
		t.Fatalf("NOAUTH refresh should enter ModeAuth, got %v", m.Mode())
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Errorf("auth prompt should mask input")
	}
	// The error banner should not leak the raw NOAUTH string.
	if strings.Contains(m.LastErr(), "NOAUTH") {
		t.Errorf("NOAUTH should route to the prompt, not the error banner: %q", m.LastErr())
	}
}

func TestReplyNoAuth_EntersPrompt(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, replyMsg{err: "NOAUTH Authentication required."})
	if m.Mode() != ModeAuth {
		t.Fatalf("NOAUTH reply should enter ModeAuth, got %v", m.Mode())
	}
}

func TestAuthSubmit_SendsAUTHAndUnmasks(t *testing.T) {
	f := newFake()
	m := NewModel(f, ":6390", 2*time.Second, "")
	m, _ = runMsg(m, refreshMsg{err: "NOAUTH Authentication required."})
	m = typeString(m, "s3cret")
	m, cmd := runMsg(m, keyMsg("enter"))
	if m.Mode() != ModeNormal {
		t.Fatalf("after submit mode=%v want Normal", m.Mode())
	}
	if m.input.EchoMode != textinput.EchoNormal {
		t.Errorf("echo mode should reset to normal after auth submit")
	}
	if cmd == nil {
		t.Fatal("auth submit should issue AUTH")
	}
	cmd()
	last := f.calls[len(f.calls)-1]
	if !equalArgv(last, []string{"AUTH", "s3cret"}) {
		t.Errorf("last call %v want [AUTH s3cret]", last)
	}
}

func TestTickDuringAuth_DoesNotRefetch(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, refreshMsg{err: "NOAUTH Authentication required."})
	genBefore := m.fetchGen
	m, cmd := runMsg(m, tickMsg{n: m.tickN + 1})
	if m.fetchGen != genBefore {
		t.Errorf("tick during auth must not schedule a fetch (gen %d→%d)", genBefore, m.fetchGen)
	}
	if cmd == nil {
		t.Errorf("tick during auth should still re-arm the tick")
	}
}

// --- view ----------------------------------------------------------------

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{{45, "45s"}, {600, "10m"}, {7200, "2h"}, {172800, "2d"}}
	for _, c := range cases {
		if got := formatUptime(c.in); got != c.want {
			t.Errorf("formatUptime(%d)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestHeader_ShowsUptimeAndClients(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = runMsg(m, refreshMsg{
		keys: []KeyInfo{{Name: "k", TTL: -1}},
		info: infoStatus{dbsize: 1, fsync: "always", uptime: 600, clients: 2},
	})
	out := m.View()
	if !strings.Contains(out, "up=10m") {
		t.Errorf("header missing uptime\n%s", out)
	}
	if !strings.Contains(out, "clients=2") {
		t.Errorf("header missing clients\n%s", out)
	}
	if !strings.Contains(out, "fsync=always") {
		t.Errorf("header missing INFO-driven fsync\n%s", out)
	}
}

func TestFsyncOverride_WinsOverInfo(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "custom")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}, info: infoStatus{fsync: "always"}})
	if !strings.Contains(m.View(), "fsync=custom") {
		t.Errorf("-fsync override should win over INFO appendfsync")
	}
}

func TestStatus_PageIndicator(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = seedPage(t, m, []string{"a"}, 5) // more pages follow
	if !strings.Contains(m.View(), "page 1 →") {
		t.Errorf("status should show 'page 1 →' when a next page exists\n%s", m.View())
	}
}

func TestRenderHash_FieldValuePairs(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = runMsg(m, refreshMsg{
		keys:   []KeyInfo{{Name: "h", TTL: -1, Kind: KindHash}},
		info:   infoStatus{dbsize: 1},
		value:  resp.Array(resp.Bulk([]byte("name")), resp.Bulk([]byte("neo")), resp.Bulk([]byte("role")), resp.Bulk([]byte("the one"))),
		hasVal: true,
	})
	out := m.View()
	for _, want := range []string{"name", "role", "neo", "the one"} {
		if !strings.Contains(out, want) {
			t.Errorf("hash view missing %q\n%s", want, out)
		}
	}
	// Field/value separator distinguishes the hash layout from a plain list.
	if !strings.Contains(out, ": ") {
		t.Errorf("hash view should use 'field: value' separator\n%s", out)
	}
}
