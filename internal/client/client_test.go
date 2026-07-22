package client

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// fakeServer reads commands from one end of a net.Pipe and writes
// canned replies. Each entry in replies corresponds to one inbound
// command; the goroutine exits after exhausting the slice or the pipe
// is closed.
type fakeServer struct {
	t       *testing.T
	conn    net.Conn
	r       *resp.Reader
	w       *resp.Writer
	replies []resp.Value

	mu      sync.Mutex
	gotArgv [][][]byte
	done    chan struct{}
}

func newFakeServer(t *testing.T, replies []resp.Value) (*Client, *fakeServer) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	fs := &fakeServer{
		t:       t,
		conn:    serverSide,
		r:       resp.NewReader(serverSide),
		w:       resp.NewWriter(serverSide),
		replies: replies,
		done:    make(chan struct{}),
	}
	go fs.serve()
	return NewConn(clientSide), fs
}

func (fs *fakeServer) serve() {
	defer close(fs.done)
	defer fs.conn.Close()
	for i := 0; i < len(fs.replies); i++ {
		argv, err := fs.r.ReadCommand()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				fs.t.Errorf("fakeServer ReadCommand: %v", err)
			}
			return
		}
		fs.mu.Lock()
		// Copy because resp.Reader reuses the slice on next call.
		cpArgv := make([][]byte, len(argv))
		for j, a := range argv {
			cp := make([]byte, len(a))
			copy(cp, a)
			cpArgv[j] = cp
		}
		fs.gotArgv = append(fs.gotArgv, cpArgv)
		fs.mu.Unlock()

		if err := fs.w.WriteFrame(fs.replies[i]); err != nil {
			fs.t.Errorf("fakeServer WriteFrame: %v", err)
			return
		}
		if err := fs.w.Flush(); err != nil {
			fs.t.Errorf("fakeServer Flush: %v", err)
			return
		}
	}
}

func (fs *fakeServer) recorded() [][][]byte {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([][][]byte, len(fs.gotArgv))
	copy(out, fs.gotArgv)
	return out
}

func TestClient_Do_AllFrameKinds(t *testing.T) {
	t.Parallel()
	replies := []resp.Value{
		resp.OK(),
		resp.Error("ERR unknown command 'NOPE'"),
		resp.Int(42),
		resp.Bulk([]byte("hello")),
		resp.NullBulk(),
		resp.Array(resp.Bulk([]byte("a")), resp.Bulk([]byte("b"))),
	}
	c, fs := newFakeServer(t, replies)
	defer c.Close()

	cases := []struct {
		name string
		want resp.Value
	}{
		{"simple-string", resp.OK()},
		{"error", resp.Error("ERR unknown command 'NOPE'")},
		{"integer", resp.Int(42)},
		{"bulk", resp.Bulk([]byte("hello"))},
		{"null-bulk", resp.NullBulk()},
		{"array", resp.Array(resp.Bulk([]byte("a")), resp.Bulk([]byte("b")))},
	}

	for _, tc := range cases {
		got, err := c.Do("PING") // command bytes don't matter for the reply
		if err != nil {
			t.Fatalf("%s: Do: %v", tc.name, err)
		}
		if !equalValue(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}

	<-fs.done
	gotArgv := fs.recorded()
	if len(gotArgv) != len(cases) {
		t.Fatalf("recorded %d commands, want %d", len(gotArgv), len(cases))
	}
	for i, argv := range gotArgv {
		if len(argv) != 1 || string(argv[0]) != "PING" {
			t.Errorf("argv[%d] = %q, want [PING]", i, argv)
		}
	}
}

// TestClient_Do_DecodesRESP3 is the end-to-end gate for the codec-symmetry
// fix: a client sends HELLO 3 and then HGETALL, and the server replies with
// native RESP3 frames (`%` map, `_` null). Before the reader learned the
// RESP3 kinds this failed with `resp: unknown prefix '%'`; the whole point
// is that the client now decodes the map. The server side writes with
// WriteFrameProto(…, Proto3) because Client.Do's own writer is RESP2-only.
func TestClient_Do_DecodesRESP3(t *testing.T) {
	t.Parallel()
	clientSide, serverSide := net.Pipe()
	c := NewConn(clientSide)
	defer c.Close()

	// helloReply mimics the HELLO 3 handshake map; hgetallReply is the
	// HGETALL map with a nil value to exercise the `_` null decode too.
	helloReply := resp.Map(
		resp.Bulk([]byte("proto")), resp.Int(3),
	)
	hgetallReply := resp.Map(
		resp.Bulk([]byte("f1")), resp.Bulk([]byte("v1")),
		resp.Bulk([]byte("f2")), resp.Null(),
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverSide.Close()
		sr := resp.NewReader(serverSide)
		sw := resp.NewWriter(serverSide)
		for _, reply := range []resp.Value{helloReply, hgetallReply} {
			if _, err := sr.ReadCommand(); err != nil {
				t.Errorf("server ReadCommand: %v", err)
				return
			}
			if err := sw.WriteFrameProto(reply, resp.Proto3); err != nil {
				t.Errorf("server WriteFrameProto: %v", err)
				return
			}
			if err := sw.Flush(); err != nil {
				t.Errorf("server Flush: %v", err)
				return
			}
		}
	}()

	hello, err := c.Do("HELLO", "3")
	if err != nil {
		t.Fatalf("HELLO 3: %v", err)
	}
	if hello.Kind != resp.KindMap {
		t.Fatalf("HELLO 3 reply kind = %q, want map", byte(hello.Kind))
	}

	got, err := c.Do("HGETALL", "h")
	if err != nil {
		t.Fatalf("HGETALL: %v", err)
	}
	if got.Kind != resp.KindMap {
		t.Fatalf("HGETALL reply kind = %q, want map", byte(got.Kind))
	}
	if len(got.Array) != 4 {
		t.Fatalf("HGETALL map has %d flat elements, want 4", len(got.Array))
	}
	if string(got.Array[0].Bytes) != "f1" || string(got.Array[1].Bytes) != "v1" {
		t.Errorf("pair 0 = (%q,%q), want (f1,v1)", got.Array[0].Bytes, got.Array[1].Bytes)
	}
	if string(got.Array[2].Bytes) != "f2" {
		t.Errorf("pair 1 field = %q, want f2", got.Array[2].Bytes)
	}
	if got.Array[3].Kind != resp.KindNull {
		t.Errorf("pair 1 value kind = %q, want null", byte(got.Array[3].Kind))
	}

	<-done
}

func TestClient_Do_EncodesMultiArg(t *testing.T) {
	t.Parallel()
	c, fs := newFakeServer(t, []resp.Value{resp.OK()})
	defer c.Close()

	if _, err := c.Do("SET", "k", "v", "EX", "60"); err != nil {
		t.Fatalf("Do: %v", err)
	}
	<-fs.done

	rec := fs.recorded()
	if len(rec) != 1 {
		t.Fatalf("recorded %d commands, want 1", len(rec))
	}
	want := []string{"SET", "k", "v", "EX", "60"}
	if len(rec[0]) != len(want) {
		t.Fatalf("argv len = %d, want %d", len(rec[0]), len(want))
	}
	for i, s := range want {
		if string(rec[0][i]) != s {
			t.Errorf("argv[%d] = %q, want %q", i, rec[0][i], s)
		}
	}
}

func TestClient_DoBytes_BinarySafe(t *testing.T) {
	t.Parallel()
	c, fs := newFakeServer(t, []resp.Value{resp.OK()})
	defer c.Close()

	payload := []byte{0x00, 0x01, '\r', '\n', 0xff, 'a'}
	if _, err := c.DoBytes([][]byte{[]byte("SET"), []byte("k"), payload}); err != nil {
		t.Fatalf("DoBytes: %v", err)
	}
	<-fs.done

	rec := fs.recorded()
	if len(rec) != 1 || len(rec[0]) != 3 {
		t.Fatalf("rec = %v", rec)
	}
	if !bytes.Equal(rec[0][2], payload) {
		t.Errorf("payload = %v, want %v", rec[0][2], payload)
	}
}

func TestClient_Do_EmptyArgv(t *testing.T) {
	t.Parallel()
	c, _ := newFakeServer(t, nil)
	defer c.Close()

	if _, err := c.Do(); err == nil {
		t.Fatal("Do() with empty argv: want error, got nil")
	}
}

func TestClient_Close_Idempotent(t *testing.T) {
	t.Parallel()
	c, _ := newFakeServer(t, nil)
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := c.Do("PING"); !errors.Is(err, ErrClosed) {
		t.Errorf("Do after Close: want ErrClosed, got %v", err)
	}
}

func TestClient_TransportError_MarksClosed(t *testing.T) {
	t.Parallel()
	// Spin up a real TCP listener that closes immediately.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}()
	defer l.Close()

	c, err := DialTimeout(addr, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	// First Do should fail (server hung up); client transitions to closed.
	if _, err := c.Do("PING"); err == nil {
		t.Fatal("first Do: want error, got nil")
	}
	if _, err := c.Do("PING"); !errors.Is(err, ErrClosed) {
		t.Errorf("second Do: want ErrClosed, got %v", err)
	}
}

func TestClient_Do_SerialUnderConcurrency(t *testing.T) {
	t.Parallel()
	const n = 50
	replies := make([]resp.Value, n)
	for i := range replies {
		replies[i] = resp.Int(int64(i))
	}
	c, fs := newFakeServer(t, replies)
	defer c.Close()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.Do("INCR", "counter"); err != nil {
				t.Errorf("Do: %v", err)
			}
		}()
	}
	wg.Wait()
	<-fs.done

	if got := len(fs.recorded()); got != n {
		t.Errorf("recorded %d commands, want %d", got, n)
	}
}

// equalValue is a deep-equality helper for resp.Value (avoids
// importing reflect or pulling testify for one assertion).
func equalValue(a, b resp.Value) bool {
	if a.Kind != b.Kind || a.IsNull != b.IsNull {
		return false
	}
	switch a.Kind {
	case resp.KindSimpleString, resp.KindError:
		return a.Str == b.Str
	case resp.KindInteger:
		return a.Int == b.Int
	case resp.KindBulkString:
		return bytes.Equal(a.Bytes, b.Bytes)
	case resp.KindArray:
		if len(a.Array) != len(b.Array) {
			return false
		}
		for i := range a.Array {
			if !equalValue(a.Array[i], b.Array[i]) {
				return false
			}
		}
		return true
	}
	return false
}
