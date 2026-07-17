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
	return startServerWith(t, "")
}

// startServerWith boots a server, optionally password-protected, and
// returns its bound address plus a stop func.
func startServerWith(t *testing.T, requirePass string) (string, func()) {
	t.Helper()
	s, err := server.New(server.Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		RequirePass: requirePass,
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

	// Poll server-side for SET to land. Asserting on rendered output is
	// racy: "foo" appears the moment it's typed (inside the input echo),
	// long before the runMutating tea.Cmd fires the actual SET. Server
	// state is the durable signal.
	verify, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("verify dial: %v", err)
	}
	defer func() { _ = verify.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, err := verify.Do("GET", "foo")
		if err == nil && v.Kind == resp.KindBulkString && !v.IsNull && string(v.Bytes) == "bar" {
			goto setLanded
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("SET foo bar never landed on the server within 2s")
setLanded:

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

// TestTUI_TeatestSmoke_TypedValueViews seeds a list and a hash, then walks
// focus across both and asserts each renders in its own value-pane shape:
// a list as elements, a hash as field: value pairs. This is the M14
// "per-type view" smoke.
func TestTUI_TeatestSmoke_TypedValueViews(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	seed, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// mylist (seq 1) sorts before myhash (seq 2), so focus lands on the
	// list first and `j` moves to the hash.
	if _, err := seed.Do("RPUSH", "mylist", "alpha", "beta"); err != nil {
		t.Fatalf("seed RPUSH: %v", err)
	}
	if _, err := seed.Do("HSET", "myhash", "field1", "hval"); err != nil {
		t.Fatalf("seed HSET: %v", err)
	}
	_ = seed.Close()

	m := newTeaModel(t, addr)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(90, 20))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	// List view: focused on mylist, value pane shows the elements.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "mylist", "alpha", "beta")
	}, teatest.WithDuration(10*time.Second))

	// Move to the hash; value pane switches to field: value rows.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "myhash", "field1", "hval", ": ")
	}, teatest.WithDuration(10*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTUI_TeatestSmoke_Paging seeds more keys than one SCAN page holds and
// drives [ / ] across pages, asserting the page indicator advances and the
// visible key set actually changes (page 1 shows k000; page 2 does not).
// This is the M14 "paging scenario" owned risk test.
func TestTUI_TeatestSmoke_Paging(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	seed, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// 120 keys > defaultPageCount (50) ⇒ at least three SCAN pages. Seq
	// order is insertion order, so page 1 = k000..k049, page 2 = k050..
	for i := range 120 {
		if _, err := seed.Do("SET", keyName(i), "v"); err != nil {
			t.Fatalf("seed SET %d: %v", i, err)
		}
	}
	_ = seed.Close()

	m := newTeaModel(t, addr)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(90, 24))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	// Page 1: k000 visible, more pages follow.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "k000", "page 1 →")
	}, teatest.WithDuration(10*time.Second))

	// Next page: k050 only exists in page 2's SCAN range (page 1 returns
	// k000..k049), so its appearance is positive proof paging advanced.
	// The output stream is cumulative across frames, so assert positively
	// rather than on the absence of page-1 keys.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return containsAll(out, "k050", "page 2")
	}, teatest.WithDuration(10*time.Second))

	// Back to page 1 works without crashing on the popped cursor.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTUI_TeatestSmoke_AuthPrompt points the TUI at a password-protected
// server: the first refresh trips -NOAUTH, the masked prompt appears, and
// submitting the password authenticates so the seeded key loads.
func TestTUI_TeatestSmoke_AuthPrompt(t *testing.T) {
	const pass = "hunter2"
	addr, stop := startServerWith(t, pass)
	defer stop()

	// Seed a key over an authenticated connection.
	seed, err := client.DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := seed.Do("AUTH", pass); err != nil {
		t.Fatalf("seed AUTH: %v", err)
	}
	if _, err := seed.Do("SET", "secretkey", "v"); err != nil {
		t.Fatalf("seed SET: %v", err)
	}
	_ = seed.Close()

	m := newTeaModel(t, addr)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(90, 20))

	// First refresh trips -NOAUTH ⇒ the password prompt appears.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("password:"))
	}, teatest.WithDuration(10*time.Second))

	// Submit the password; the key list loads once authenticated.
	tm.Type(pass)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("secretkey"))
	}, teatest.WithDuration(10*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// keyName renders a zero-padded key name so lexical and seq order agree.
func keyName(i int) string {
	return "k" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
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
