package chaos

import (
	"fmt"
	"os"
	"testing"
)

// TestMain compiles the server binary once and reuses it across tests.
func TestMain(m *testing.M) {
	_, cleanup, err := BuildServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "chaos: BuildServer: %v\n", err)
		os.Exit(2)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestHarnessSmoke is a fast sanity check: start, ping via raw client, stop.
// Always runs (no -short gate) so the harness itself is exercised on every CI.
func TestHarnessSmoke(t *testing.T) {
	srv := NewServer(t, "no")
	srv.Start(t)
	t.Cleanup(srv.Stop)

	cli := NewRawClient(srv.Addr)
	defer cli.Close()

	rep, _, err := cli.Do("PING")
	if err != nil {
		t.Fatalf("PING: %v\nserver stderr:\n%s", err, srv.Stderr())
	}
	if rep != "PONG" {
		t.Fatalf("PING reply %q, want PONG", rep)
	}

	if _, _, err := cli.Do("SET", "k", "v"); err != nil {
		t.Fatalf("SET: %v", err)
	}
	got, _, err := cli.Do("GET", "k")
	if err != nil || got != "v" {
		t.Fatalf("GET=%q err=%v, want v", got, err)
	}
}
