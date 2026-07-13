package server

import (
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// argvOf splits a space-separated command line into a [][]byte argv, the
// shape dispatch expects. Handlers are exercised directly here so the
// tests don't depend on the RESP2-only wire reader (which cannot parse
// RESP3 reply frames).
func argvOf(line string) [][]byte {
	parts := strings.Fields(line)
	argv := make([][]byte, len(parts))
	for i, p := range parts {
		argv[i] = []byte(p)
	}
	return argv
}

// mapField returns the value paired with key in a HELLO handshake map.
func mapField(t *testing.T, v resp.Value, key string) resp.Value {
	t.Helper()
	if v.Kind != resp.KindMap {
		t.Fatalf("reply kind %q, want map", byte(v.Kind))
	}
	for i := 0; i+1 < len(v.Array); i += 2 {
		if string(v.Array[i].Bytes) == key {
			return v.Array[i+1]
		}
	}
	t.Fatalf("map has no field %q", key)
	return resp.Value{}
}

func TestHello_NoArg_KeepsProto2(t *testing.T) {
	s := setupServer(t)
	cs := newConnState(7)

	got := s.dispatch(cs, argvOf("HELLO"))
	if cs.proto != resp.Proto2 {
		t.Fatalf("proto = %d, want 2 (no-arg HELLO must not switch)", cs.proto)
	}
	if f := mapField(t, got, "proto"); f.Int != 2 {
		t.Fatalf("proto field = %d, want 2", f.Int)
	}
	if f := mapField(t, got, "server"); string(f.Bytes) != "toykv" {
		t.Fatalf("server field = %q, want toykv", f.Bytes)
	}
	if f := mapField(t, got, "id"); f.Int != 7 {
		t.Fatalf("id field = %d, want 7", f.Int)
	}
	if f := mapField(t, got, "version"); string(f.Bytes) != serverVersion {
		t.Fatalf("version field = %q, want %q", f.Bytes, serverVersion)
	}
}

func TestHello_Proto3_UpgradesConnection(t *testing.T) {
	s := setupServer(t)
	cs := newConnState(1)

	got := s.dispatch(cs, argvOf("HELLO 3"))
	if cs.proto != resp.Proto3 {
		t.Fatalf("proto = %d, want 3 after HELLO 3", cs.proto)
	}
	if f := mapField(t, got, "proto"); f.Int != 3 {
		t.Fatalf("proto field = %d, want 3", f.Int)
	}
}

func TestHello_Proto2_DowngradesBack(t *testing.T) {
	s := setupServer(t)
	cs := newConnState(1)

	s.dispatch(cs, argvOf("HELLO 3"))
	got := s.dispatch(cs, argvOf("HELLO 2"))
	if cs.proto != resp.Proto2 {
		t.Fatalf("proto = %d, want 2 after HELLO 2", cs.proto)
	}
	if f := mapField(t, got, "proto"); f.Int != 2 {
		t.Fatalf("proto field = %d, want 2", f.Int)
	}
}

func TestHello_UnsupportedProto_NoProtoAndNoSwitch(t *testing.T) {
	s := setupServer(t)
	for _, arg := range []string{"HELLO 9", "HELLO 1", "HELLO abc", "HELLO 0"} {
		cs := newConnState(1)
		got := s.dispatch(cs, argvOf(arg))
		if got.Kind != resp.KindError || !strings.HasPrefix(got.Str, "NOPROTO") {
			t.Fatalf("%q: got %+v, want NOPROTO error", arg, got)
		}
		if cs.proto != resp.Proto2 {
			t.Fatalf("%q: proto changed to %d on rejected HELLO", arg, cs.proto)
		}
	}
}

func TestHello_AuthClause_ErrorsWithoutRequirepass(t *testing.T) {
	s := setupServer(t)
	cs := newConnState(1)

	got := s.dispatch(cs, argvOf("HELLO 3 AUTH user pass"))
	if got.Kind != resp.KindError || !strings.Contains(got.Str, "no password is set") {
		t.Fatalf("got %+v, want AUTH-without-password error", got)
	}
	// A rejected AUTH must leave the protocol untouched.
	if cs.proto != resp.Proto2 {
		t.Fatalf("proto changed to %d despite AUTH failure", cs.proto)
	}
}

func TestHello_MalformedAuthClause_SyntaxError(t *testing.T) {
	s := setupServer(t)
	// Valid protover but a broken AUTH tail (missing password / wrong verb).
	for _, arg := range []string{"HELLO 3 AUTH user", "HELLO 3 NOTAUTH a b"} {
		cs := newConnState(1)
		got := s.dispatch(cs, argvOf(arg))
		if got.Kind != resp.KindError || !strings.Contains(got.Str, "Syntax error") {
			t.Fatalf("%q: got %+v, want syntax error", arg, got)
		}
	}
}
