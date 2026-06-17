// Package chaos drives the shipped toykv server under sustained mixed-command
// workload with random faults (SIGKILL→restart, SIGSTOP/SIGCONT pauses,
// BGREWRITEAOF mid-write) and asserts cross-restart invariants.
//
// Where test/e2e/ proves protocol compat against a stable subprocess, chaos
// composes the crash and pause faults M3/M4/M5 each own individually into
// long-running soaks. The harness is intentionally separate from e2e so the
// e2e suite stays fast (sub-second) and the chaos suite stays gated by -short.
package chaos

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Binary holds the absolute path of the server binary built in TestMain.
type Binary struct{ Server string }

var built Binary

// BuildServer compiles cmd/toykv into a temp dir and returns its path.
// Intended to be called once from TestMain.
func BuildServer() (Binary, func(), error) {
	tmp, err := os.MkdirTemp("", "toykv-chaos-bin-*")
	if err != nil {
		return Binary{}, func() {}, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		cleanup()
		return Binary{}, func() {}, err
	}

	path := filepath.Join(tmp, "toykv"+suffix)
	cmd := exec.Command("go", "build", "-o", path, "./cmd/toykv")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return Binary{}, func() {}, fmt.Errorf("go build cmd/toykv: %w\n%s", err, stderr.String())
	}
	built = Binary{Server: path}
	return built, cleanup, nil
}

func findRepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("go.mod not found above chaos harness")
}

// Server is a restartable toykv subprocess. Unlike e2e.Server it can be killed
// and re-Started with the same -dir to drive AOF replay.
type Server struct {
	Addr        string
	Dir         string
	AppendFsync string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stderr *threadSafeBuffer // persistent across restarts
}

// threadSafeBuffer wraps bytes.Buffer with a mutex so the harness can read
// stderr while the subprocess is still writing to it.
type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// NewServer reserves a free port and prepares a fresh data dir but does not
// start the process. Call Start to launch.
func NewServer(t *testing.T, fsync string) *Server {
	t.Helper()
	if built.Server == "" {
		t.Fatalf("chaos: server binary not built; call BuildServer in TestMain")
	}
	port := freePort(t)
	dir := t.TempDir()
	if fsync == "" {
		fsync = "always"
	}
	return &Server{
		Addr:        "127.0.0.1:" + strconv.Itoa(port),
		Dir:         dir,
		AppendFsync: fsync,
		stderr:      &threadSafeBuffer{},
	}
}

// Start launches the server and blocks until it answers PING.
func (s *Server) Start(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		t.Fatal("chaos: Start called while server already running")
	}

	args := []string{"-addr", s.Addr, "-dir", s.Dir, "-appendfsync", s.AppendFsync, "-log-level", "info"}
	//nolint:gosec // G204: built.Server is a path the test harness produced itself.
	cmd := exec.Command(built.Server, args...)
	cmd.Stderr = s.stderr // persistent buffer accumulates across restarts
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("chaos: start: %v", err)
	}
	s.cmd = cmd

	if err := waitReady(s.Addr, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("chaos: not ready on %s: %v\nstderr:\n%s", s.Addr, err, s.stderr.String())
	}
}

// Kill sends SIGKILL (durability test — no graceful drain) and waits.
func (s *Server) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGKILL)
	_ = s.cmd.Wait()
	s.cmd = nil
}

// Pause sends SIGSTOP (simulates a long GC / load spike).
func (s *Server) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return errors.New("not running")
	}
	return s.cmd.Process.Signal(syscall.SIGSTOP)
}

// Resume sends SIGCONT.
func (s *Server) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return errors.New("not running")
	}
	return s.cmd.Process.Signal(syscall.SIGCONT)
}

// Stderr returns whatever the subprocess has written to stderr so far.
func (s *Server) Stderr() string {
	s.mu.Lock()
	buf := s.stderr
	s.mu.Unlock()
	if buf == nil {
		return ""
	}
	return buf.String()
}

// Stop terminates if still running (test cleanup helper).
func (s *Server) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
			_ = conn.Close()
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err == nil && n >= 5 && bytes.HasPrefix(buf[:n], []byte("+PONG")) {
			return nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timed out waiting for +PONG")
	}
	return lastErr
}

// RawClient is a tiny single-connection RESP2 client used by the chaos workload.
// It reconnects on demand so SIGKILL/restart cycles don't strand it.
type RawClient struct {
	addr string

	mu sync.Mutex
	c  net.Conn
	br *bufio.Reader
}

// NewRawClient builds a lazy client. The first command opens the connection.
func NewRawClient(addr string) *RawClient { return &RawClient{addr: addr} }

func (c *RawClient) dial() error {
	if c.c != nil {
		return nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	c.c = conn
	c.br = bufio.NewReader(conn)
	return nil
}

func (c *RawClient) reset() {
	if c.c != nil {
		_ = c.c.Close()
	}
	c.c = nil
	c.br = nil
}

// Close drops the underlying socket.
func (c *RawClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset()
}

// Do sends a command (already RESP-encoded as []parts) and returns the raw reply
// line. Bulk strings are returned without the surrounding $len/CRLF. Nil replies
// return ("", true, nil).
func (c *RawClient) Do(parts ...string) (reply string, isNil bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.dial(); err != nil {
		return "", false, err
	}

	// Encode *N\r\n + $len\r\nval\r\n per part.
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(parts))
	for _, p := range parts {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(p), p)
	}
	_ = c.c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, werr := c.c.Write(buf.Bytes()); werr != nil {
		c.reset()
		return "", false, werr
	}

	line, lerr := c.br.ReadString('\n')
	if lerr != nil {
		c.reset()
		return "", false, lerr
	}
	if len(line) < 3 {
		c.reset()
		return "", false, fmt.Errorf("short reply %q", line)
	}
	switch line[0] {
	case '+', ':':
		return line[1 : len(line)-2], false, nil
	case '-':
		return "", false, fmt.Errorf("server error: %s", line[1:len(line)-2])
	case '$':
		n, perr := strconv.Atoi(line[1 : len(line)-2])
		if perr != nil {
			c.reset()
			return "", false, fmt.Errorf("bad bulk length %q", line)
		}
		if n < 0 {
			return "", true, nil
		}
		body := make([]byte, n+2)
		if _, rerr := io.ReadFull(c.br, body); rerr != nil {
			c.reset()
			return "", false, rerr
		}
		return string(body[:n]), false, nil
	default:
		c.reset()
		return "", false, fmt.Errorf("unknown reply prefix %q", line[0])
	}
}

// Workload runs N goroutines mixing SET/GET/DEL/INCR/EXPIRE until ctx is done.
// Acknowledged SETs are reported via OnAckSet so invariants can be checked
// across restarts. INCR replies are reported via OnAckIncr (post-ack value).
type Workload struct {
	Addr       string
	Goroutines int
	KeySpace   int    // number of distinct SET keys (cycled by worker)
	CounterKey string // INCR target; empty disables INCR

	OnAckSet  func(key, val string)
	OnAckIncr func(value int64)

	errs atomic.Int64
	ops  atomic.Int64
}

// Errors returns the count of command errors seen during the workload.
func (w *Workload) Errors() int64 { return w.errs.Load() }

// Ops returns the count of commands attempted (success + error).
func (w *Workload) Ops() int64 { return w.ops.Load() }

// Run blocks until ctx is cancelled. Each goroutine reconnects on any error
// (typical during a restart window) and keeps going.
func (w *Workload) Run(ctx context.Context) {
	if w.Goroutines <= 0 {
		w.Goroutines = 4
	}
	if w.KeySpace <= 0 {
		w.KeySpace = 64
	}
	var wg sync.WaitGroup
	for g := 0; g < w.Goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cli := NewRawClient(w.Addr)
			defer cli.Close()

			var i uint64
			for ctx.Err() == nil {
				i++
				w.ops.Add(1)
				key := fmt.Sprintf("k:%d:%d", id, i%uint64(w.KeySpace))
				val := fmt.Sprintf("v-%d-%d", id, i)

				// Round-robin: SET, GET, INCR (if configured), DEL of an older key.
				switch i % 4 {
				case 0:
					if _, _, err := cli.Do("SET", key, val); err != nil {
						w.errs.Add(1)
						break
					}
					if w.OnAckSet != nil {
						w.OnAckSet(key, val)
					}
				case 1:
					if _, _, err := cli.Do("GET", key); err != nil {
						w.errs.Add(1)
					}
				case 2:
					if w.CounterKey == "" {
						break
					}
					rep, _, err := cli.Do("INCR", w.CounterKey)
					if err != nil {
						w.errs.Add(1)
						break
					}
					if n, perr := strconv.ParseInt(rep, 10, 64); perr == nil && w.OnAckIncr != nil {
						w.OnAckIncr(n)
					}
				case 3:
					if _, _, err := cli.Do("DEL", key); err != nil {
						w.errs.Add(1)
					}
				}
			}
		}(g)
	}
	wg.Wait()
}
