// Command toykv is the in-memory key-value store server.
// Skeleton in M0; real server lifecycle lands in M1.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `toykv — in-memory key-value store server

usage:
  toykv [flags]

flags:
  -addr string        listen address (default ":6390")
  -dir  string        data directory  (default "./data")
  -appendfsync string fsync policy: always|everysec|no (default "always")
  -h, --help          show this help and exit

status: M0 placeholder. Real server lands in M1 (RESP echo).
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stdout, usage) }
	flag.Parse()
	fmt.Fprintln(os.Stdout, "toykv: M0 placeholder — server lands in M1")
}
