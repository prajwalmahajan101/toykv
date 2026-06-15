package main_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// cliBin is the built path of toykv-cli; populated by TestMain.
var cliBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "toykv-cli-bin-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	cliBin = filepath.Join(tmp, "toykv-cli")
	build := exec.Command("go", "build", "-o", cliBin, "github.com/prajwalmahajan101/toykv/cmd/toykv-cli")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build toykv-cli: " + err.Error())
	}
	os.Exit(m.Run())
}

// startServer boots an in-process toykv server on 127.0.0.1:0 and
// returns its bound address plus a cleanup func.
func startServer(t *testing.T) (string, func()) {
	t.Helper()
	s, err := server.New(server.Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	var addr string
	for time.Now().Before(deadline) {
		if a := s.Addr(); a != "" {
			addr = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		cancel()
		t.Fatal("server never bound")
	}
	// Wait until the listener actually accepts.
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	}
	return addr, cleanup
}

func runCLI(t *testing.T, addr string, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	all := append([]string{"-addr", addr}, args...)
	cmd := exec.Command(cliBin, all...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outB, errB bytes.Buffer
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	err := cmd.Run()
	code = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("exec %v: %v", all, err)
		}
	}
	return outB.String(), errB.String(), code
}

func TestCLI_OneShot_SetGet(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	out, _, code := runCLI(t, addr, "", "SET", "k", "hello")
	if code != 0 || strings.TrimSpace(out) != "OK" {
		t.Fatalf("SET: out=%q code=%d", out, code)
	}
	out, _, code = runCLI(t, addr, "", "GET", "k")
	if code != 0 || strings.TrimSpace(out) != `"hello"` {
		t.Fatalf("GET: out=%q code=%d", out, code)
	}
}

func TestCLI_OneShot_GetMissing(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	out, _, code := runCLI(t, addr, "", "GET", "nope")
	if code != 0 || strings.TrimSpace(out) != "(nil)" {
		t.Fatalf("GET missing: out=%q code=%d", out, code)
	}
}

func TestCLI_OneShot_IncrInteger(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	out, _, code := runCLI(t, addr, "", "INCR", "n")
	if code != 0 || strings.TrimSpace(out) != "(integer) 1" {
		t.Fatalf("INCR: out=%q code=%d", out, code)
	}
}

func TestCLI_OneShot_ErrorExit1(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	_, errOut, code := runCLI(t, addr, "", "GET") // missing arg
	if code != 1 {
		t.Fatalf("want exit 1, got %d (stderr=%q)", code, errOut)
	}
	if !strings.HasPrefix(errOut, "(error)") {
		t.Fatalf("want stderr starting with (error); got %q", errOut)
	}
}

func TestCLI_DialFailureExit2(t *testing.T) {
	// Pick an addr nothing is listening on.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, errOut, code := runCLI(t, addr, "", "PING")
	if code != 2 {
		t.Fatalf("want exit 2, got %d (stderr=%q)", code, errOut)
	}
}

func TestCLI_Raw_Bulk(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	_, _, _ = runCLI(t, addr, "", "SET", "k", "hi")
	out, _, code := runCLI(t, addr, "", "-raw", "GET", "k")
	if code != 0 || out != "hi\n" {
		t.Fatalf("raw GET: out=%q code=%d", out, code)
	}
}

func TestCLI_Piped(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	in := "SET a 1\nINCR a\nGET a\n"
	out, _, code := runCLI(t, addr, in)
	if code != 0 {
		t.Fatalf("piped: code=%d out=%q", code, out)
	}
	want := "OK\n(integer) 2\n\"2\"\n"
	if out != want {
		t.Fatalf("piped out mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestCLI_Piped_LastReplyDrivesExit(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()
	in := "SET k v\nGET\n" // last cmd errors
	out, errOut, code := runCLI(t, addr, in)
	if code != 1 {
		t.Fatalf("want exit 1, got %d (out=%q err=%q)", code, out, errOut)
	}
}
