package e2e

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runRedisCLI invokes the real redis-cli against the supplied server and
// returns trimmed stdout. Caller handles t.Skip if redis-cli isn't on PATH.
func runRedisCLI(t *testing.T, addr string, args ...string) string {
	t.Helper()
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("bad addr %q", addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	full := append([]string{"-h", host, "-p", port}, args...)
	cmd := exec.CommandContext(ctx, "redis-cli", full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run redis-cli: %v\nstderr:%s", err, errb.String())
		}
		// Non-zero is fine for some negative cases; let the caller decide.
	}
	return strings.TrimRight(out.String(), "\n")
}

// TestRedisCLI_ByteCompat sweeps the PRD §5.1 surface with the real
// redis-cli binary, asserting exact output (newline-trimmed) for the
// commands whose output redis-cli renders deterministically across
// versions. Skips entirely when redis-cli isn't installed so the suite
// stays green on developer machines and macOS CI.
func TestRedisCLI_ByteCompat(t *testing.T) {
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not on PATH; CI installs redis-tools to exercise this")
	}
	s := StartServer(t, ServerOpts{})

	// Each case sends args; expects the trimmed stdout to equal want.
	type tc struct {
		name string
		args []string
		want string
	}
	cases := []tc{
		{"ping", []string{"PING"}, "PONG"},
		{"echo", []string{"ECHO", "hello"}, "hello"},
		{"set", []string{"SET", "k", "v"}, "OK"},
		{"get", []string{"GET", "k"}, "v"},
		{"exists-1", []string{"EXISTS", "k"}, "1"},
		{"exists-0", []string{"EXISTS", "missing"}, "0"},
		{"incr-fresh", []string{"INCR", "c"}, "1"},
		{"incr-again", []string{"INCR", "c"}, "2"},
		{"decr", []string{"DECR", "c"}, "1"},
		{"dbsize", []string{"DBSIZE"}, "3"}, // k, c, and what we set next? recount below.
		{"del", []string{"DEL", "k"}, "1"},
		{"flushdb", []string{"FLUSHDB"}, "OK"},
		{"dbsize-empty", []string{"DBSIZE"}, "0"},
		// TTL sentinels.
		{"ttl-missing", []string{"TTL", "missing"}, "-2"},
		{"setup-no-ttl", []string{"SET", "p", "v"}, "OK"},
		{"ttl-no-ttl", []string{"TTL", "p"}, "-1"},
		{"persist-no-ttl", []string{"PERSIST", "p"}, "0"},
		{"expire-ok", []string{"EXPIRE", "p", "100"}, "1"},
		{"persist-ok", []string{"PERSIST", "p"}, "1"},
	}

	// The "dbsize=3" assumption depends on prior steps: SET k v + INCR c (creates c).
	// That's 2 keys, so adjust the want before running.
	for i := range cases {
		if cases[i].name == "dbsize" {
			cases[i].want = strconv.Itoa(2)
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runRedisCLI(t, s.Addr, c.args...)
			if got != c.want {
				t.Fatalf("redis-cli %v: got %q want %q", c.args, got, c.want)
			}
		})
	}
}

// TestRedisCLI_GetNil verifies that redis-cli prints a nil bulk reply
// as the literal string "(nil)" — the convention every redis-cli version
// has used since 1.x.
func TestRedisCLI_GetNil(t *testing.T) {
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not on PATH")
	}
	s := StartServer(t, ServerOpts{})
	got := runRedisCLI(t, s.Addr, "GET", "missing")
	if got != "(nil)" {
		t.Fatalf("GET missing: want %q got %q", "(nil)", got)
	}
}
