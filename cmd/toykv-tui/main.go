// Command toykv-tui is the Bubble Tea TUI client for toykv.
// Skeleton in M0; real TUI lands in M7.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `toykv-tui — Bubble Tea TUI for toykv

usage:
  toykv-tui [flags]

flags:
  -addr    string  server address     (default "127.0.0.1:6390")
  -refresh duration  poll interval    (default 500ms)
  -h, --help       show this help and exit

status: M0 placeholder. Real TUI lands in M7.
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stdout, usage) }
	flag.Parse()
	fmt.Fprintln(os.Stdout, "toykv-tui: M0 placeholder — TUI lands in M7")
}
