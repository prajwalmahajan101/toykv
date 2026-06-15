// Command toykv-tui is the Bubble Tea TUI client for toykv. It drives
// a running server through the shared internal/client package (M6) and
// renders a two-pane keys/value layout per PRD §5.5.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/tui"
)

const usage = `toykv-tui — Bubble Tea TUI for toykv

usage:
  toykv-tui [flags]

flags:
  -addr     string    server address (default "127.0.0.1:6390")
  -refresh  duration  poll interval  (default 2s)
  -timeout  duration  connect timeout (default 5s)
  -fsync    string    fsync label shown in the status bar (informational only)
  -h, --help          show this help and exit

keybindings (PRD §5.5):
  j/k    move cursor             /    filter (client-side glob)
  n      SET new (key value)     e    edit focused value
  d      DEL focused (confirm)   t    EXPIRE focused (seconds)
  i      INCR focused            D    DECR focused
  F      FLUSHDB (confirm)       r    force refresh
  :      raw RESP prompt         q    quit
`

const (
	exitOK    = 0
	exitFatal = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("toykv-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stdout, usage) }

	addr := fs.String("addr", "127.0.0.1:6390", "server address")
	refresh := fs.Duration("refresh", 2*time.Second, "poll interval")
	timeout := fs.Duration("timeout", 5*time.Second, "connect timeout")
	fsync := fs.String("fsync", "", "fsync label for status bar (informational)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitFatal
	}

	c, err := client.DialTimeout(*addr, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "toykv-tui: dial %s: %v\n", *addr, err)
		return exitFatal
	}
	defer c.Close()

	model := tui.NewModel(c, *addr, *refresh, *fsync)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "toykv-tui: %v\n", err)
		return exitFatal
	}
	return exitOK
}
