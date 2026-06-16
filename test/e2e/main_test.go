package e2e

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_, cleanup, err := BuildBinaries()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build binaries: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestHarness_StartStop is a smoke test for the harness itself: spin a server
// on a random port, confirm PING works, and ensure cleanup runs cleanly.
func TestHarness_StartStop(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	if s.Addr == "" {
		t.Fatal("empty server addr")
	}
}
