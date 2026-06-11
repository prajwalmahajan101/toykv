package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// setupServer constructs a Server bound to a random port with a fresh
// in-memory store. Use runServer to start it.
func setupServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// runServer starts s.Run in a goroutine and waits for Addr() to become
// available. It returns the error channel; callers should cancel the
// returned ctx (or its parent) to stop the server, then drain the
// channel.
func runServer(t *testing.T, s *Server) (context.Context, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Addr() != "" {
			return ctx, cancel, errCh
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("server did not bind within 2s")
	return ctx, cancel, errCh
}

func dial(t *testing.T, addr string) (net.Conn, *resp.Reader, *resp.Writer) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c, resp.NewReader(c), resp.NewWriter(c)
}

func writeCmd(t *testing.T, w *resp.Writer, args ...string) {
	t.Helper()
	elems := make([]resp.Value, len(args))
	for i, a := range args {
		elems[i] = resp.Bulk([]byte(a))
	}
	if err := w.WriteFrame(resp.Array(elems...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func readReply(t *testing.T, r *resp.Reader) resp.Value {
	t.Helper()
	v, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return v
}

func TestPingPong(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "PING")
	got := readReply(t, r)
	if got.Kind != resp.KindSimpleString || got.Str != "PONG" {
		t.Fatalf("got %+v, want +PONG", got)
	}
}

func TestPingWithArg(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "PING", "hello")
	got := readReply(t, r)
	if got.Kind != resp.KindBulkString || string(got.Bytes) != "hello" {
		t.Fatalf("got %+v, want bulk 'hello'", got)
	}
}

func TestEcho(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "ECHO", "hello world")
	got := readReply(t, r)
	if got.Kind != resp.KindBulkString || string(got.Bytes) != "hello world" {
		t.Fatalf("got %+v, want bulk 'hello world'", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "XYZ")
	got := readReply(t, r)
	if got.Kind != resp.KindError {
		t.Fatalf("got %+v, want error", got)
	}
	if !strings.Contains(got.Str, "unknown command") || !strings.Contains(got.Str, "XYZ") {
		t.Fatalf("error message %q does not name unknown command", got.Str)
	}
}

func TestArityErrorEcho(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "ECHO")
	got := readReply(t, r)
	if got.Kind != resp.KindError {
		t.Fatalf("got %+v, want error", got)
	}
	if !strings.Contains(got.Str, "wrong number of arguments") {
		t.Fatalf("error message %q does not flag wrong arity", got.Str)
	}
}

// TestArityErrorPingTooMany covers the maxArgs branch in dispatch.
func TestArityErrorPingTooMany(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "PING", "a", "b")
	got := readReply(t, r)
	if got.Kind != resp.KindError || !strings.Contains(got.Str, "wrong number of arguments") {
		t.Fatalf("got %+v, want arity error", got)
	}
}

func TestMalformedDropsConn(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Send junk that is not a valid RESP frame.
	if _, err := c.Write([]byte("?not-a-frame\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Server replies with one error frame then closes the conn.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := resp.NewReader(c)
	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Kind != resp.KindError {
		t.Fatalf("got %+v, want error frame", got)
	}

	// Subsequent read should hit EOF.
	if _, err := r.ReadFrame(); err == nil {
		t.Fatal("expected EOF after malformed input, got nil")
	}
}

func TestShutdownDrains(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)

	// Open a conn and leave it idle.
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
}

func TestMultipleConcurrentConns(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	const conns = 10
	const pings = 50

	var wg sync.WaitGroup
	wg.Add(conns)
	for i := 0; i < conns; i++ {
		go func() {
			defer wg.Done()
			c, r, w := dial(t, s.Addr())
			defer c.Close()
			for j := 0; j < pings; j++ {
				writeCmd(t, w, "PING")
				v := readReply(t, r)
				if v.Kind != resp.KindSimpleString || v.Str != "PONG" {
					t.Errorf("got %+v, want +PONG", v)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestNew_RejectsEmptyAddr(t *testing.T) {
	if _, err := New(Config{Store: store.New()}); err == nil {
		t.Fatal("want error for empty addr, got nil")
	}
}

func TestNew_RejectsNilStore(t *testing.T) {
	if _, err := New(Config{Addr: ":0"}); err == nil {
		t.Fatal("want error for nil store, got nil")
	}
}

func TestAddr_BeforeRun(t *testing.T) {
	s := setupServer(t)
	if got := s.Addr(); got != "" {
		t.Fatalf("got %q, want empty string before Run", got)
	}
}

func TestDispatch_EmptyArgv(t *testing.T) {
	s := setupServer(t)
	got := s.dispatch(nil)
	if got.Kind != resp.KindError {
		t.Fatalf("got %+v, want error", got)
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct {
		in, want time.Duration
	}{
		{0, 5 * time.Millisecond},
		{5 * time.Millisecond, 10 * time.Millisecond},
		{500 * time.Millisecond, time.Second},
		{2 * time.Second, time.Second}, // cap
	}
	for _, c := range cases {
		if got := nextBackoff(c.in); got != c.want {
			t.Errorf("nextBackoff(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
