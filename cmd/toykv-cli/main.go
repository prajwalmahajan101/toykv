// Command toykv-cli is a line-oriented RESP2 client for toykv, modelled
// on redis-cli. It supports one-shot, REPL (when stdin is a TTY), and
// piped-stdin modes; pretty-prints replies by default with -raw for
// script use. See docs/PRD.md §5.6.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

const usage = `toykv-cli — RESP2 client for toykv

usage:
  toykv-cli [flags] [command [args...]]

flags:
  -addr     string    server address (default "127.0.0.1:6390")
  -raw                raw output (bulk-strings as bytes, no quoting)
  -timeout  duration  connect timeout (default 5s)
  -h, --help          show this help and exit

modes:
  toykv-cli CMD arg ...             # one-shot
  toykv-cli                         # REPL when stdin is a TTY
  echo "CMD args" | toykv-cli       # piped (one command per line)

exit status: 0 reply ok · 1 RESP -ERR · 2 connection or parse failure
`

// Exit codes per PRD §5.6.
const (
	exitOK    = 0
	exitErr   = 1
	exitFatal = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("toykv-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stdout, usage) }

	addr := fs.String("addr", "127.0.0.1:6390", "server address")
	raw := fs.Bool("raw", false, "raw output (no pretty-print)")
	timeout := fs.Duration("timeout", 5*time.Second, "connect timeout")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitFatal
	}

	cli, err := client.DialTimeout(*addr, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "toykv-cli: dial %s: %v\n", *addr, err)
		return exitFatal
	}
	defer cli.Close()

	pr := &printer{out: stdout, err: stderr, raw: *raw}

	// One-shot mode.
	if fs.NArg() > 0 {
		return doOneShot(cli, pr, fs.Args())
	}

	// REPL vs piped: pick by whether stdin is a TTY.
	if isTTY(stdin) {
		return doREPL(cli, pr, stdin, stdout, *addr)
	}
	return doPiped(cli, pr, stdin)
}

// doOneShot sends a single command and exits with the appropriate code.
func doOneShot(cli *client.Client, pr *printer, argv []string) int {
	v, err := cli.Do(argv...)
	if err != nil {
		fmt.Fprintf(pr.err, "toykv-cli: %v\n", err)
		return exitFatal
	}
	pr.print(v)
	if v.Kind == resp.KindError {
		return exitErr
	}
	return exitOK
}

// doREPL drives an interactive prompt until EOF / quit / exit.
func doREPL(cli *client.Client, pr *printer, stdin io.Reader, stdout io.Writer, addr string) int {
	br := bufio.NewReader(stdin)
	prompt := fmt.Sprintf("toykv:%s> ", addr)
	for {
		fmt.Fprint(stdout, prompt)
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if strings.TrimSpace(line) == "" {
					fmt.Fprintln(stdout)
					return exitOK
				}
				// Fall through to dispatch the final line without newline.
			} else {
				fmt.Fprintf(pr.err, "toykv-cli: read: %v\n", err)
				return exitFatal
			}
		}
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if errors.Is(err, io.EOF) {
				return exitOK
			}
			continue
		}
		if trimmed == "quit" || trimmed == "exit" {
			return exitOK
		}
		if rc := dispatchLine(cli, pr, line); rc == exitFatal {
			return exitFatal
		}
		if errors.Is(err, io.EOF) {
			return exitOK
		}
	}
}

// doPiped reads commands one per line from stdin until EOF.
func doPiped(cli *client.Client, pr *printer, stdin io.Reader) int {
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	last := exitOK
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rc := dispatchLine(cli, pr, line)
		if rc == exitFatal {
			return exitFatal
		}
		last = rc
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(pr.err, "toykv-cli: read: %v\n", err)
		return exitFatal
	}
	return last
}

// dispatchLine tokenises a line and sends it; returns the reply's exit
// code. Parse errors do not kill REPL/piped sessions (they print and
// return exitErr); transport errors return exitFatal.
func dispatchLine(cli *client.Client, pr *printer, line string) int {
	argv, err := tokenise(line)
	if err != nil {
		fmt.Fprintf(pr.err, "(error) parse: %v\n", err)
		return exitErr
	}
	if len(argv) == 0 {
		return exitOK
	}
	v, err := cli.Do(argv...)
	if err != nil {
		fmt.Fprintf(pr.err, "toykv-cli: %v\n", err)
		return exitFatal
	}
	pr.print(v)
	if v.Kind == resp.KindError {
		return exitErr
	}
	return exitOK
}

// isTTY reports whether r is a terminal (character device). Stdlib only.
func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
