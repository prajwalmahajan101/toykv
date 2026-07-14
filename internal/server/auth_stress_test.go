package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// TestAuth_ConcurrentStress is M12's owned risk test (ROADMAP §M12): N
// connections racing AUTH + commands. It proves three invariants under
// the race detector:
//
//  1. no auth-state bleed — a connection that never authenticates stays
//     gated even while its neighbours are authenticated and writing;
//  2. no command executes before its connection authenticates — an
//     unauthenticated SET must never become visible to an authed GET;
//  3. authenticating mid-stream flips exactly that connection's gate.
func TestAuth_ConcurrentStress(t *testing.T) {
	const (
		pass    = "s3cret"
		nAuthed = 20 // authenticate up front, then hammer SET/GET
		nUnauth = 20 // never authenticate; every reply must be NOAUTH
		nMid    = 10 // start gated, AUTH mid-stream, then write
		nOps    = 50
	)

	s := setupAuthServer(t, pass)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()
	addr := s.Addr()

	var wg sync.WaitGroup

	// Authenticated writers: AUTH, then SET/GET their own key space.
	for i := range nAuthed {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c, r, w := dial(t, addr)
			defer func() { _ = c.Close() }()
			writeCmd(t, w, "AUTH", pass)
			if reply := readReply(t, r); reply.Str != "OK" {
				t.Errorf("authed[%d]: AUTH got %+v", id, reply)
				return
			}
			key := fmt.Sprintf("authed:%d", id)
			for op := range nOps {
				val := fmt.Sprintf("v%d", op)
				writeCmd(t, w, "SET", key, val)
				if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
					t.Errorf("authed[%d]: SET got %+v", id, reply)
					return
				}
				writeCmd(t, w, "GET", key)
				if reply := readReply(t, r); reply.Kind != resp.KindBulkString || string(reply.Bytes) != val {
					t.Errorf("authed[%d]: GET got %+v, want %q", id, reply, val)
					return
				}
			}
		}(i)
	}

	// Unauthenticated connections: every gated command must reply NOAUTH,
	// never a data reply — even while neighbours are authenticated.
	for i := range nUnauth {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c, r, w := dial(t, addr)
			defer func() { _ = c.Close() }()
			key := fmt.Sprintf("unauth:%d", id)
			for op := range nOps {
				writeCmd(t, w, "SET", key, fmt.Sprintf("stolen%d", op))
				if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "NOAUTH") {
					t.Errorf("unauth[%d]: SET got %+v, want NOAUTH", id, reply)
					return
				}
				writeCmd(t, w, "GET", fmt.Sprintf("authed:%d", id%nAuthed))
				if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "NOAUTH") {
					t.Errorf("unauth[%d]: GET got %+v, want NOAUTH", id, reply)
					return
				}
			}
		}(i)
	}

	// Mid-stream authenticators: gated first, AUTH, then writes succeed.
	for i := range nMid {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c, r, w := dial(t, addr)
			defer func() { _ = c.Close() }()
			key := fmt.Sprintf("mid:%d", id)
			for op := range nOps / 2 {
				writeCmd(t, w, "SET", key, fmt.Sprintf("early%d", op))
				if reply := readReply(t, r); reply.Kind != resp.KindError || !strings.HasPrefix(reply.Str, "NOAUTH") {
					t.Errorf("mid[%d]: pre-AUTH SET got %+v, want NOAUTH", id, reply)
					return
				}
			}
			writeCmd(t, w, "AUTH", "default", pass)
			if reply := readReply(t, r); reply.Str != "OK" {
				t.Errorf("mid[%d]: AUTH got %+v", id, reply)
				return
			}
			for op := range nOps / 2 {
				writeCmd(t, w, "SET", key, fmt.Sprintf("late%d", op))
				if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
					t.Errorf("mid[%d]: post-AUTH SET got %+v", id, reply)
					return
				}
			}
		}(i)
	}

	wg.Wait()

	// No pre-auth command may have executed: neither the unauth'd SETs
	// nor the mid-stream pre-AUTH SETs can be visible.
	c, r, w := dial(t, addr)
	defer func() { _ = c.Close() }()
	writeCmd(t, w, "AUTH", pass)
	if reply := readReply(t, r); reply.Str != "OK" {
		t.Fatalf("verifier AUTH: %+v", reply)
	}
	for i := range nUnauth {
		writeCmd(t, w, "GET", fmt.Sprintf("unauth:%d", i))
		if reply := readReply(t, r); !reply.IsNull {
			t.Errorf("unauth:%d exists (%+v) — an unauthenticated SET executed", i, reply)
		}
	}
	for i := range nMid {
		writeCmd(t, w, "GET", fmt.Sprintf("mid:%d", i))
		reply := readReply(t, r)
		if reply.IsNull || !strings.HasPrefix(string(reply.Bytes), "late") {
			t.Errorf("mid:%d = %+v, want a post-AUTH value — a pre-AUTH SET executed or none landed", i, reply)
		}
	}
}
