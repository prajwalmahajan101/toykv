package server

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// compatStep is one command in the dual-protocol sweep. wantR2 is the
// exact RESP2 wire reply; wantR3 is the RESP3 reply, or "" when it is
// byte-identical to RESP2 (the common case — most commands do not emit
// rich frames in M10).
type compatStep struct {
	args   []string
	wantR2 string
	wantR3 string
}

// compatSequence exercises every command in PRD §5.1 over one connection.
// State is deterministic within the sequence so the wire bytes are fixed.
// The only RESP2↔RESP3 divergences are the two logical-null replies
// migrated to resp.Null() (GET miss, NX failure): `$-1` on RESP2, `_` on
// RESP3. Everything else must be byte-identical across protocols — that
// is the M10 compat guarantee.
var compatSequence = []compatStep{
	{args: []string{"PING"}, wantR2: "+PONG\r\n"},
	{args: []string{"ECHO", "hi"}, wantR2: "$2\r\nhi\r\n"},
	{args: []string{"SET", "a", "1"}, wantR2: "+OK\r\n"},
	{args: []string{"GET", "a"}, wantR2: "$1\r\n1\r\n"},
	{args: []string{"GET", "zzz"}, wantR2: "$-1\r\n", wantR3: "_\r\n"},
	{args: []string{"SET", "a", "9", "NX"}, wantR2: "$-1\r\n", wantR3: "_\r\n"},
	{args: []string{"EXISTS", "a"}, wantR2: ":1\r\n"},
	{args: []string{"INCR", "ctr"}, wantR2: ":1\r\n"},
	{args: []string{"INCR", "ctr"}, wantR2: ":2\r\n"},
	{args: []string{"DECR", "ctr"}, wantR2: ":1\r\n"},
	{args: []string{"TTL", "a"}, wantR2: ":-1\r\n"},
	{args: []string{"EXPIRE", "a", "100"}, wantR2: ":1\r\n"},
	{args: []string{"PERSIST", "a"}, wantR2: ":1\r\n"},
	{args: []string{"DEL", "a"}, wantR2: ":1\r\n"},
	{args: []string{"KEYS", "*"}, wantR2: "*1\r\n$3\r\nctr\r\n"},
	{args: []string{"DBSIZE"}, wantR2: ":1\r\n"},
	{args: []string{"FLUSHDB"}, wantR2: "+OK\r\n"},
	{args: []string{"DBSIZE"}, wantR2: ":0\r\n"},
}

func TestRESP3_DualProtocolCompatSweep(t *testing.T) {
	t.Run("resp2", func(t *testing.T) { runCompatSweep(t, resp.Proto2) })
	t.Run("resp3", func(t *testing.T) { runCompatSweep(t, resp.Proto3) })
}

func runCompatSweep(t *testing.T, proto resp.Proto) {
	t.Helper()
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	w := resp.NewWriter(conn)

	// A RESP3 client negotiates first; the handshake reply is this
	// connection's first frame (id 1 on a fresh server). We assert its
	// exact bytes too — HELLO is the one command that differs by protocol
	// beyond the null migration.
	if proto == resp.Proto3 {
		sendCmd(t, w, "HELLO", "3")
		wantHello := encodeReply(t, helloReply(&connState{proto: resp.Proto3, id: 1}), resp.Proto3)
		if got := readN(t, conn, len(wantHello)); got != wantHello {
			t.Fatalf("HELLO 3 handshake:\n got %q\nwant %q", got, wantHello)
		}
	}

	for _, step := range compatSequence {
		want := step.wantR2
		if proto == resp.Proto3 && step.wantR3 != "" {
			want = step.wantR3
		}
		sendCmd(t, w, step.args...)
		if got := readN(t, conn, len(want)); got != want {
			t.Fatalf("%s: got %q, want %q", strings.Join(step.args, " "), got, want)
		}
	}
}

func sendCmd(t *testing.T, w *resp.Writer, args ...string) {
	t.Helper()
	elems := make([]resp.Value, len(args))
	for i, a := range args {
		elems[i] = resp.Bulk([]byte(a))
	}
	if err := w.WriteFrame(resp.Array(elems...)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// readN reads exactly n bytes under a deadline. Reading the known reply
// length avoids parsing RESP3 frames, which the RESP2-only reader cannot
// decode — the point of this test is the raw wire bytes anyway.
func readN(t *testing.T, conn net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v (partial=%q)", n, err, buf)
	}
	return string(buf)
}

// encodeReply renders a reply value to its wire bytes at protocol p,
// reusing the production writer so the expected HELLO bytes track the
// real encoder.
func encodeReply(t *testing.T, v resp.Value, p resp.Proto) string {
	t.Helper()
	var b strings.Builder
	w := resp.NewWriter(&b)
	if err := w.WriteFrameProto(v, p); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return b.String()
}
