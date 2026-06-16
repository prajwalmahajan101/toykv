package e2e

import (
	"strings"
	"testing"
)

func TestCLI_OneShot_SetGet(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	if r := RunCLI(t, s.Addr, "", "SET", "k", "v"); r.ExitCode != 0 || r.Stdout != "OK\n" {
		t.Fatalf("SET: code=%d stdout=%q stderr=%q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if r := RunCLI(t, s.Addr, "", "GET", "k"); r.ExitCode != 0 || r.Stdout != "\"v\"\n" {
		t.Fatalf("GET: code=%d stdout=%q stderr=%q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if r := RunCLI(t, s.Addr, "", "GET", "missing"); r.ExitCode != 0 || r.Stdout != "(nil)\n" {
		t.Fatalf("GET missing: code=%d stdout=%q", r.ExitCode, r.Stdout)
	}
}

func TestCLI_OneShot_ServerError(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	// INCR on a non-integer payload → -ERR → exit code 1, error on stderr.
	_ = RunCLI(t, s.Addr, "", "SET", "k", "abc")
	r := RunCLI(t, s.Addr, "", "INCR", "k")
	if r.ExitCode != 1 {
		t.Fatalf("INCR on non-int: want exit 1, got %d (stdout=%q stderr=%q)", r.ExitCode, r.Stdout, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "(error)") {
		t.Fatalf("stderr missing '(error)' prefix: %q", r.Stderr)
	}
}

func TestCLI_Piped_MultilineScript(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	stdin := strings.Join([]string{
		"SET a 1",
		"SET b 2",
		"# comment line is skipped",
		"",
		"DBSIZE",
		"GET a",
	}, "\n") + "\n"

	r := RunCLI(t, s.Addr, stdin)
	if r.ExitCode != 0 {
		t.Fatalf("piped: code=%d stderr=%q", r.ExitCode, r.Stderr)
	}
	want := "OK\nOK\n(integer) 2\n\"1\"\n"
	if r.Stdout != want {
		t.Fatalf("piped stdout:\n got  %q\n want %q", r.Stdout, want)
	}
}

func TestCLI_Raw_Output(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	_ = RunCLI(t, s.Addr, "", "SET", "k", "hello")
	r := RunCLI(t, s.Addr, "", "-raw", "GET", "k")
	// -raw drops the surrounding quotes and prints bytes verbatim.
	if r.ExitCode != 0 || r.Stdout != "hello\n" {
		t.Fatalf("raw GET: code=%d stdout=%q", r.ExitCode, r.Stdout)
	}
}
