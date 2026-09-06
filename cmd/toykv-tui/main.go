// Command toykv-tui is the Bubble Tea TUI client for toykv. It drives
// a running server through the shared internal/client package (M6) and
// renders a two-pane keys/value layout per PRD §5.5.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/tui"
)

const usage = `toykv-tui — Bubble Tea TUI for toykv

usage:
  toykv-tui [flags]

flags:
  -addr     string    server address (default "127.0.0.1:6390")
  -a        string    password for AUTH (non-interactive; prompts on -NOAUTH otherwise)
  -refresh  duration  poll interval  (default 2s)
  -timeout  duration  connect timeout (default 5s)
  -fsync    string    fsync label override for the status bar (INFO is used when available)
  -log      string    write structured logs to this file (default off)
  -h, --help          show this help and exit

keybindings (PRD §5.5):
  j/k    move cursor             /    match (server-side SCAN)
  [ / ]  prev/next SCAN page     g/G  top/bottom
  n      SET new (key value)     e    edit focused value
  d      DEL focused (confirm)   t    EXPIRE focused (seconds)
  i      INCR focused            D    DECR focused
  F      FLUSHDB (confirm)       r    force refresh
  :      raw RESP prompt         q    quit

If the server requires a password, the TUI prompts on the first -NOAUTH
reply; pass -a to authenticate non-interactively at launch.
`

const (
	exitOK    = 0
	exitFatal = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// restoreTerminal best-effort reverts the terminal to a usable state if
// Bubble Tea hasn't (or couldn't) do so itself. The escape sequences
// leave the alt screen and re-show the cursor; harmless on already-
// restored terminals.
func restoreTerminal(w io.Writer) {
	fmt.Fprint(w, "\x1b[?1049l\x1b[?25h")
}

func run(args []string, stdout, stderr io.Writer) (code int) {
	fs := flag.NewFlagSet("toykv-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stdout, usage) }

	addr := fs.String("addr", "127.0.0.1:6390", "server address")
	pass := fs.String("a", "", "password for AUTH (non-interactive)")
	refresh := fs.Duration("refresh", 2*time.Second, "poll interval")
	timeout := fs.Duration("timeout", 5*time.Second, "connect timeout")
	fsync := fs.String("fsync", "", "fsync label override for status bar (INFO used when available)")
	logPath := fs.String("log", "", "write structured logs to this file (default off)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitFatal
	}

	// Optional file log. The TUI package itself never writes to stdio
	// (would corrupt the alt screen); this handler is for cmd-side
	// diagnostics only.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "toykv-tui: open log %s: %v\n", *logPath, err)
			return exitFatal
		}
		defer f.Close()
		logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	logger.Info("toykv-tui starting", "addr", *addr, "refresh", *refresh)

	// Terminal-restoration guard: if anything panics before, during, or
	// after p.Run, leave the alt screen first so the trace prints to a
	// usable terminal.
	defer func() {
		if r := recover(); r != nil {
			restoreTerminal(stdout)
			fmt.Fprintf(stderr, "toykv-tui: panic: %v\n%s\n", r, debug.Stack())
			code = exitFatal
		}
	}()

	c, err := client.DialClusterTimeout(*addr, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "toykv-tui: dial %s: %v\n", *addr, err)
		logger.Error("dial failed", "addr", *addr, "err", err.Error())
		return exitFatal
	}
	defer c.Close()

	// Non-interactive AUTH: authenticate before the TUI starts so the
	// first refresh doesn't trip -NOAUTH. When -a is absent the TUI
	// prompts for a password on the first -NOAUTH reply instead.
	if *pass != "" {
		reply, err := c.Do("AUTH", *pass)
		if err != nil {
			fmt.Fprintf(stderr, "toykv-tui: AUTH: %v\n", err)
			logger.Error("auth failed", "err", err.Error())
			return exitFatal
		}
		if reply.Kind == resp.KindError {
			fmt.Fprintf(stderr, "toykv-tui: AUTH: %s\n", reply.Str)
			logger.Error("auth rejected", "err", reply.Str)
			return exitFatal
		}
	}

	model := tui.NewModel(c, *addr, *refresh, *fsync)
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Bubble Tea handles SIGINT (Ctrl+C) natively; wire SIGTERM so kill
	// from a supervisor also exits cleanly via the program's own quit
	// path (which restores the terminal).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		if _, ok := <-sigCh; ok {
			p.Quit()
		}
	}()

	if _, err := p.Run(); err != nil {
		restoreTerminal(stdout)
		fmt.Fprintf(stderr, "toykv-tui: %v\n", err)
		logger.Error("program exited with error", "err", err.Error())
		return exitFatal
	}
	return exitOK
}
