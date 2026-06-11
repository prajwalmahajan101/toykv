package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Environment variables used by the self-re-exec child mode of this
// test binary. When set, TestMain detects them and runs as a server
// instead of as a regular Go test process.
const (
	envChildMode = "TOYKV_AOF_CHILD"
	envChildDir  = "TOYKV_AOF_CHILD_DIR"
	envChildAddr = "TOYKV_AOF_CHILD_ADDR"
)

// TestMain implements the self-re-exec trick (LLD §M3 risk test). When
// TOYKV_AOF_CHILD=1 is set in the environment, the test binary runs as
// a toykv server bound to TOYKV_AOF_CHILD_ADDR with AOF in
// TOYKV_AOF_CHILD_DIR under fsync=always. The parent test process
// dials it, writes commands, SIGKILLs it, and verifies replay.
//
// The hand-off is one-way: child mode exits via os.Exit; it never
// returns control to the testing framework.
func TestMain(m *testing.M) {
	if os.Getenv(envChildMode) == "1" {
		runChildServer()
		return
	}
	os.Exit(m.Run())
}

func runChildServer() {
	dir := os.Getenv(envChildDir)
	addr := os.Getenv(envChildAddr)
	if dir == "" || addr == "" {
		fmt.Fprintln(os.Stderr, "child: TOYKV_AOF_CHILD_DIR and TOYKV_AOF_CHILD_ADDR must be set")
		os.Exit(2)
	}
	s, err := New(Config{
		Addr:        addr,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		Dir:         dir,
		FsyncPolicy: aof.FsyncAlways,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child New:", err)
		os.Exit(2)
	}
	// Start Run in a goroutine so we can wait for the listener to bind,
	// then signal the parent over stdout.
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a := s.Addr(); a != "" {
			fmt.Fprintln(os.Stdout, a)
			// Block until SIGKILL — we never close cleanly; that's the
			// point of this test.
			<-errCh
			_ = s.Close()
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "child: listener did not bind within 5s")
	os.Exit(2)
}

// TestAOF_CrashInjection_Always is the M3 milestone-owned risk test:
// every SET that the server acknowledged with +OK must survive a
// SIGKILL and be present after replay, under FsyncAlways.
func TestAOF_CrashInjection_Always(t *testing.T) {
	if testing.Short() {
		t.Skip("crash injection forks a subprocess; skipped under -short")
	}

	dir := t.TempDir()
	child, addr := startChild(t, dir, "TestAOF_CrashInjection_Always")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		_ = child.Process.Kill()
		t.Fatalf("dial child: %v", err)
	}
	rdr := resp.NewReader(c)
	wtr := resp.NewWriter(c)

	// Write a stream of SETs, recording each one for which we saw +OK.
	// Mid-stream, SIGKILL the child without warning.
	const total = 200
	killAt := 87 // arbitrary; any boundary works because each ack must survive
	acked := make(map[string]string, total)
	for i := 0; i < total; i++ {
		k := fmt.Sprintf("k%03d", i)
		v := fmt.Sprintf("v%03d", i)
		if err := wtr.WriteFrame(resp.Array(
			resp.Bulk([]byte("SET")),
			resp.Bulk([]byte(k)),
			resp.Bulk([]byte(v)),
		)); err != nil {
			break
		}
		if err := wtr.Flush(); err != nil {
			break
		}
		v2, err := rdr.ReadFrame()
		if err != nil {
			break // killed before ack landed
		}
		if v2.Kind == resp.KindSimpleString && v2.Str == "OK" {
			acked[k] = v
		}
		if i == killAt {
			if err := child.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("SIGKILL: %v", err)
			}
		}
	}
	_ = c.Close()
	_ = child.Wait()

	if len(acked) < killAt {
		t.Fatalf("only %d acks recorded before kill; test design broken", len(acked))
	}

	// Restart in-process (no need for a second subprocess) and verify.
	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s2)
	defer func() {
		cancel()
		<-errCh
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	missing := 0
	for k, v := range acked {
		writeCmd(t, w2, "GET", k)
		got := readReply(t, r2)
		if got.Kind != resp.KindBulkString || got.IsNull || string(got.Bytes) != v {
			missing++
			if missing <= 5 {
				t.Errorf("acked key %q: got %+v, want bulk %q", k, got, v)
			}
		}
	}
	if missing > 0 {
		t.Fatalf("%d/%d acked SETs lost across SIGKILL under FsyncAlways", missing, len(acked))
	}
}

// startChild forks the test binary into server mode and waits for it to
// print its bound address on stdout. parentTest is the name of the
// caller test — passed through -test.run so the child never tries to
// execute the rest of the test suite if TestMain ever fails to detect
// child mode.
func startChild(t *testing.T, dir, parentTest string) (*exec.Cmd, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// Pick an OS-assigned port via :0 sentinel.
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)

	cmd := exec.Command(exe, "-test.run=^"+parentTest+"$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(),
		envChildMode+"=1",
		envChildDir+"="+dir,
		envChildAddr+"="+addr,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		// Best-effort cleanup; the test usually kills the child already.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Wait for the ready line (the addr) with a timeout.
	type readyMsg struct {
		addr string
		err  error
	}
	ready := make(chan readyMsg, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			ready <- readyMsg{addr: strings.TrimSpace(sc.Text())}
		} else {
			ready <- readyMsg{err: fmt.Errorf("child closed stdout before ready: %v", sc.Err())}
		}
	}()

	select {
	case msg := <-ready:
		if msg.err != nil {
			t.Fatalf("child ready: %v", msg.err)
		}
		return cmd, msg.addr
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not become ready within 5s")
		return nil, ""
	}
}

// freePort asks the kernel for an unused TCP port and returns it.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
