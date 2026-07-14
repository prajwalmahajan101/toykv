package server

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// setupAuthServer constructs a Server that requires pass, bound to a
// random port with a fresh in-memory store.
func setupAuthServer(t *testing.T, pass string) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		RequirePass: pass,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestAuth_GatingMatrix drives one representative command per class on
// an unauthenticated connection and expects NOAUTH for everything
// outside the {AUTH, HELLO, PING} whitelist.
func TestAuth_GatingMatrix(t *testing.T) {
	s := setupAuthServer(t, "s3cret")
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	defer func() { _ = c.Close() }()

	gated := [][]string{
		{"GET", "k"},            // strings
		{"SET", "k", "v"},       // strings, mutating
		{"EXPIRE", "k", "10"},   // TTL
		{"LPUSH", "k", "v"},     // lists
		{"HSET", "k", "f", "v"}, // hashes
		{"FLUSHDB"},             // admin
		{"BGREWRITEAOF"},        // admin, background
		{"NOSUCHCMD"},           // unknown — must not leak existence
		{"DBSIZE"},              // introspection
	}
	for _, argv := range gated {
		writeCmd(t, w, argv...)
		reply := readReply(t, r)
		if reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "NOAUTH") {
			t.Errorf("%v: got %+v, want NOAUTH error", argv, reply)
		}
	}

	// Whitelisted commands still work unauthenticated.
	writeCmd(t, w, "PING")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "PONG" {
		t.Errorf("PING: got %+v, want +PONG", reply)
	}
	writeCmd(t, w, "HELLO")
	if reply := readReply(t, r); reply.Kind == resp.KindError {
		t.Errorf("HELLO: got error %q, want handshake reply", reply.Str)
	}
}

// TestAuth_Command covers the AUTH verb: both arg forms, wrong
// credentials, re-auth, and command execution after success.
func TestAuth_Command(t *testing.T) {
	s := setupAuthServer(t, "s3cret")
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	defer func() { _ = c.Close() }()

	// Wrong password.
	writeCmd(t, w, "AUTH", "nope")
	if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "WRONGPASS") {
		t.Fatalf("AUTH wrong pass: got %+v, want WRONGPASS", reply)
	}
	// Wrong username — same error, no oracle for which part failed.
	writeCmd(t, w, "AUTH", "admin", "s3cret")
	if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "WRONGPASS") {
		t.Fatalf("AUTH wrong user: got %+v, want WRONGPASS", reply)
	}
	// Still gated after failed attempts.
	writeCmd(t, w, "GET", "k")
	if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "NOAUTH") {
		t.Fatalf("GET after failed AUTH: got %+v, want NOAUTH", reply)
	}

	// One-arg form authenticates.
	writeCmd(t, w, "AUTH", "s3cret")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
		t.Fatalf("AUTH: got %+v, want +OK", reply)
	}
	// Commands now execute.
	writeCmd(t, w, "SET", "k", "v")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
		t.Fatalf("SET after AUTH: got %+v, want +OK", reply)
	}
	// Two-arg form (explicit default user) re-authenticates fine.
	writeCmd(t, w, "AUTH", "default", "s3cret")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
		t.Fatalf("AUTH default: got %+v, want +OK", reply)
	}
}

// TestAuth_NoPasswordSet: AUTH against a server without requirepass
// returns Redis 7's exact error, hint included.
func TestAuth_NoPasswordSet(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	defer func() { _ = c.Close() }()

	writeCmd(t, w, "AUTH", "anything")
	reply := readReply(t, r)
	want := "ERR Client sent AUTH, but no password is set. Did you mean AUTH <username> <password>?"
	if reply.Kind != resp.KindError || reply.Str != want {
		t.Fatalf("AUTH with no requirepass: got %+v, want %q", reply, want)
	}
}

// TestAuth_NoRequirepassUnaffected proves a server without requirepass
// behaves exactly as before: every connection is pre-authenticated.
func TestAuth_NoRequirepassUnaffected(t *testing.T) {
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	defer func() { _ = c.Close() }()

	writeCmd(t, w, "SET", "k", "v")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
		t.Fatalf("SET: got %+v, want +OK", reply)
	}
	writeCmd(t, w, "GET", "k")
	if reply := readReply(t, r); reply.Kind != resp.KindBulkString || string(reply.Bytes) != "v" {
		t.Fatalf("GET: got %+v, want \"v\"", reply)
	}
}
