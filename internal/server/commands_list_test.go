package server

import (
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// expectBulkArray asserts the next frame is an array of bulk strings
// with exactly the given values, in order.
func expectBulkArray(t *testing.T, r *resp.Reader, want ...string) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindArray || got.IsNull {
		t.Fatalf("got %+v, want array", got)
	}
	if len(got.Array) != len(want) {
		t.Fatalf("array len = %d, want %d (%+v)", len(got.Array), len(want), got.Array)
	}
	for i, w := range want {
		el := got.Array[i]
		if el.Kind != resp.KindBulkString || string(el.Bytes) != w {
			t.Fatalf("elem %d = %+v, want bulk %q", i, el, w)
		}
	}
}

func TestList_PushRangeRoundTrip(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "LPUSH", "l", "b", "a") // pushes b then a → a lands at head → [a, b]
		expectInt(t, r, 2)
		writeCmd(t, w, "RPUSH", "l", "c")
		expectInt(t, r, 3)
		writeCmd(t, w, "LLEN", "l")
		expectInt(t, r, 3)
		writeCmd(t, w, "LRANGE", "l", "0", "-1")
		expectBulkArray(t, r, "a", "b", "c")
		writeCmd(t, w, "LINDEX", "l", "-1")
		expectBulk(t, r, "c")
		writeCmd(t, w, "LINDEX", "l", "99")
		expectNullBulk(t, r)
	})
}

func TestList_PopSemantics(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "RPUSH", "l", "a", "b")
		expectInt(t, r, 2)
		writeCmd(t, w, "LPOP", "l")
		expectBulk(t, r, "a")
		writeCmd(t, w, "RPOP", "l")
		expectBulk(t, r, "b")
		// List is now empty → key deleted; further pops are null.
		writeCmd(t, w, "LPOP", "l")
		expectNullBulk(t, r)
		writeCmd(t, w, "EXISTS", "l")
		expectInt(t, r, 0)
		writeCmd(t, w, "LPOP", "missing")
		expectNullBulk(t, r)
	})
}

func TestList_EmptyKeyReads(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "LLEN", "missing")
		expectInt(t, r, 0)
		writeCmd(t, w, "LRANGE", "missing", "0", "-1")
		expectBulkArray(t, r) // empty array
	})
}

func TestList_BadIntegerArgs(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "RPUSH", "l", "a")
		expectInt(t, r, 1)
		writeCmd(t, w, "LRANGE", "l", "x", "1")
		expectErrContains(t, r, "not an integer")
		writeCmd(t, w, "LINDEX", "l", "x")
		expectErrContains(t, r, "not an integer")
	})
}

func TestList_WrongType(t *testing.T) {
	const wrongType = "WRONGTYPE Operation against a key holding the wrong kind of value"
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "s", "v")
		expectSimple(t, r, "OK")

		for _, cmd := range [][]string{
			{"LPUSH", "s", "x"},
			{"RPUSH", "s", "x"},
			{"LPOP", "s"},
			{"RPOP", "s"},
			{"LLEN", "s"},
			{"LRANGE", "s", "0", "-1"},
			{"LINDEX", "s", "0"},
		} {
			writeCmd(t, w, cmd...)
			expectErrContains(t, r, wrongType)
		}

		// And the mirror: string/list cross-type on a list key.
		writeCmd(t, w, "RPUSH", "l", "x")
		expectInt(t, r, 1)
		writeCmd(t, w, "GET", "l")
		expectErrContains(t, r, wrongType)
		writeCmd(t, w, "INCR", "l")
		expectErrContains(t, r, wrongType)
		// SET overwrites the list wholesale (Redis semantics).
		writeCmd(t, w, "SET", "l", "now-a-string")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "GET", "l")
		expectBulk(t, r, "now-a-string")
	})
}
