package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/tui"
)

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
			_ = c.Close()
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

// newTeaModel builds a TUI model wired to addr. Centralised so each test
// stays focused on assertions rather than setup.
func newTeaModel(t *testing.T, addr string) tui.Model {
	t.Helper()
	c, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return tui.NewModel(c, addr, 2*time.Second, "")
}

// containsAll reports whether haystack contains every needle. Used inside
// teatest.WaitFor predicates to assert on rendered output fragments.
func containsAll(haystack []byte, needles ...string) bool {
	for _, n := range needles {
		if !bytes.Contains(haystack, []byte(n)) {
			return false
		}
	}
	return true
}

// TestTUI_TeatestSmoke_SET drives the TUI model through teatest's
// virtual terminal: open NewKV mode (n), type "foo bar", submit, then
// verify the server side actually stored the pair. Asserting on rendered
// output (via teatest.WaitFor) gives a smoke check that the model both
// dispatched the command and reflected the reply in the UI.
func TestTUI_TeatestSmoke_SET(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	m := newTeaModel(t, addr)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 20))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	tm.Type("foo bar")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// Wait until the value pane shows the reply ("OK") OR the key list
	// picks up the new "foo" entry — whichever lands first.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "foo") || containsAll(out, "OK")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(25*time.Millisecond))

	// Server-side ground truth: GET via a second client.
	verify, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("verify dial: %v", err)
	}
	defer func() { _ = verify.Close() }()
	v, err := verify.Do("GET", "foo")
	if err != nil {
		t.Fatalf("verify GET: %v", err)
	}
	if v.Kind != resp.KindBulkString || v.IsNull || string(v.Bytes) != "bar" {
		t.Fatalf("verify GET = %+v, want bulk=\"bar\"", v)
	}

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTUI_TeatestSmoke_INCR seeds n=10, focuses it after a refresh, sends
// `i`, and verifies the server-side counter advanced to 11.
func TestTUI_TeatestSmoke_INCR(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	seed, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := seed.Do("SET", "n", "10"); err != nil {
		t.Fatalf("seed SET: %v", err)
	}
	_ = seed.Close()

	m := newTeaModel(t, addr)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 20))

	// `r` forces a refresh so the key list populates and focus lands on n.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "n")
	}, teatest.WithDuration(2*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})

	// Poll the server for the increment to land. teatest.WaitFor would also
	// work but the rendered TUI doesn't necessarily echo the integer reply
	// inline — server-side state is the durable signal.
	c, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("verify dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, err := c.Do("GET", "n")
		if err == nil && v.Kind == resp.KindBulkString && string(v.Bytes) == "11" {
			tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
			tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("INCR never landed on the server within 2s")
}

// TestRun_BadAddrExit2 verifies the binary's run() returns the fatal
// exit code when the server is unreachable. This is a pure CLI test, not
// a TUI one — kept here because run() is defined in this package.
func TestRun_BadAddrExit2(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	code := run([]string{"-addr", addr, "-timeout", "200ms"}, io.Discard, io.Discard)
	if code != exitFatal {
		t.Fatalf("want exit %d, got %d", exitFatal, code)
	}
}

// TestRun_LogFileWrittenOnDialFailure exercises the --log flag and
// confirms a startup record reaches the file even when dial fails.
func TestRun_LogFileWrittenOnDialFailure(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()

	logPath := t.TempDir() + "/tui.log"
	code := run([]string{"-addr", addr, "-timeout", "200ms", "-log", logPath},
		io.Discard, io.Discard)
	if code != exitFatal {
		t.Fatalf("want exit %d, got %d", exitFatal, code)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(data, []byte("toykv-tui starting")) {
		t.Errorf("log file missing startup line:\n%s", data)
	}
	if !bytes.Contains(data, []byte("dial failed")) {
		t.Errorf("log file missing dial-failed line:\n%s", data)
	}
}
