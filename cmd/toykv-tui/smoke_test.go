package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/tui"
)

// drive feeds msg into the model and returns the resulting Model plus
// the tea.Cmd (if any). The caller decides whether to run the Cmd —
// the model's own Init/refresh path schedules a tea.Tick that would
// block this single-threaded helper, so the test only invokes Cmds it
// expects to terminate (network round-trips).
func drive(t *testing.T, m tui.Model, msg tea.Msg) (tui.Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(tui.Model), cmd
}

// runReply executes a Cmd known to produce a replyMsg or refreshMsg
// and pumps that single message back into the model. Stops after one
// round so the test never hits a tickCmd.
func runReply(t *testing.T, m tui.Model, cmd tea.Cmd) tui.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	out := cmd()
	if out == nil {
		return m
	}
	next, _ := m.Update(out)
	return next.(tui.Model)
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func startServer(t *testing.T) (string, func()) {
	t.Helper()
	s, err := server.New(server.Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	var addr string
	for time.Now().Before(deadline) {
		if a := s.Addr(); a != "" {
			addr = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		cancel()
		t.Fatal("server never bound")
	}
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	}
}

// TestTUI_RoundtripSET drives the model end-to-end: open NewKV mode,
// type "foo bar", submit, then verify the server side has the value.
// This is M7's exit gate — a mutating command from PRD §5.1 issued via
// PRD §5.5 keybindings against a running server.
func TestTUI_RoundtripSET(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	c, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	m := tui.NewModel(c, addr, 2*time.Second, "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m, _ = drive(t, m, keyMsg("n"))
	for _, r := range "foo bar" {
		m, _ = drive(t, m, keyMsg(string(r)))
	}
	_, cmd := drive(t, m, keyMsg("enter"))
	_ = runReply(t, m, cmd) // executes SET, pumps replyMsg back

	// Verify via a second client (the TUI's client is busy serialising).
	verify, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("verify dial: %v", err)
	}
	defer verify.Close()
	v, err := verify.Do("GET", "foo")
	if err != nil {
		t.Fatalf("verify GET: %v", err)
	}
	if v.Kind != resp.KindBulkString || v.IsNull || string(v.Bytes) != "bar" {
		t.Fatalf("verify GET = %+v, want bulk=\"bar\"", v)
	}
}

// TestTUI_RoundtripINCR exercises the i keybinding against a populated
// key.
func TestTUI_RoundtripINCR(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	c, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Do("SET", "n", "10"); err != nil {
		t.Fatalf("seed SET: %v", err)
	}

	m := tui.NewModel(c, addr, 2*time.Second, "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	// Force a refresh to populate keys + focus, then run the refresh cmd.
	m, cmd := drive(t, m, keyMsg("r"))
	m = runReply(t, m, cmd)
	if m.FocusedKey() != "n" {
		t.Fatalf("expected focused=n after refresh, got %q", m.FocusedKey())
	}
	m, cmd = drive(t, m, keyMsg("i"))
	_ = runReply(t, m, cmd)

	v, err := c.Do("GET", "n")
	if err != nil {
		t.Fatalf("verify GET: %v", err)
	}
	if string(v.Bytes) != "11" {
		t.Fatalf("INCR did not roundtrip: GET n = %q", string(v.Bytes))
	}
}

// TestRun_BadAddrExit2 verifies the binary's run() returns the fatal
// exit code when the server is unreachable.
func TestRun_BadAddrExit2(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	code := run([]string{"-addr", addr, "-timeout", "200ms"}, io.Discard, io.Discard)
	if code != exitFatal {
		t.Fatalf("want exit %d, got %d", exitFatal, code)
	}
}
