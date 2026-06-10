// Command toykv-cli is a line-oriented RESP client for toykv.
// Skeleton in M0; real CLI lands in M6.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `toykv-cli — RESP client for toykv

usage:
  toykv-cli [flags] [command [args...]]

flags:
  -addr string  server address (default "127.0.0.1:6390")
  -raw          raw output (no pretty-print)
  -h, --help    show this help and exit

modes:
  toykv-cli                 # interactive REPL (when stdin is a TTY)
  toykv-cli CMD arg ...     # one-shot
  echo "CMD args" | toykv-cli  # piped

status: M0 placeholder. Real CLI lands in M6.
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stdout, usage) }
	flag.Parse()
	fmt.Fprintln(os.Stdout, "toykv-cli: M0 placeholder — CLI lands in M6")
}
