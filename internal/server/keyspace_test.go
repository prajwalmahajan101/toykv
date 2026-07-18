package server

import (
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

func TestRename_Wire(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "src", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "RENAME", "src", "dst")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "GET", "dst")
		expectBulk(t, r, "v")
		writeCmd(t, w, "GET", "src")
		expectNullBulk(t, r)

		// Missing source → -ERR no such key.
		writeCmd(t, w, "RENAME", "missing", "x")
		expectErrContains(t, r, "no such key")
	})
}

func TestRename_PreservesTTL_Wire(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "src", "v", "EX", "1000")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "RENAME", "src", "dst")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "TTL", "dst")
		got := readReply(t, r)
		if got.Kind != resp.KindInteger || got.Int <= 0 || got.Int > 1000 {
			t.Fatalf("TTL dst = %+v, want an integer in (0,1000]", got)
		}
	})
}

func TestRenameNX_Wire(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "a", "1")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "RENAMENX", "a", "b") // b absent → moved
		expectInt(t, r, 1)
		writeCmd(t, w, "SET", "a", "2")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "RENAMENX", "a", "b") // b exists → not moved
		expectInt(t, r, 0)
		writeCmd(t, w, "RENAMENX", "missing", "x")
		expectErrContains(t, r, "no such key")
	})
}

func TestCopy_Wire(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "src", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "COPY", "src", "dst") // fresh → 1
		expectInt(t, r, 1)
		writeCmd(t, w, "COPY", "src", "dst") // exists, no REPLACE → 0
		expectInt(t, r, 0)

		writeCmd(t, w, "SET", "src", "v2")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "COPY", "src", "dst", "REPLACE") // → 1
		expectInt(t, r, 1)
		writeCmd(t, w, "GET", "dst")
		expectBulk(t, r, "v2")

		writeCmd(t, w, "COPY", "src", "src") // same object → error
		expectErrContains(t, r, "same")
		writeCmd(t, w, "COPY", "src", "dst", "DB", "1") // DB unsupported → syntax error
		expectErrContains(t, r, "syntax")
		writeCmd(t, w, "COPY", "missing", "x") // missing source → 0
		expectInt(t, r, 0)
	})
}

func TestRenameCopy_TypedValues_Wire(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		// List survives RENAME.
		writeCmd(t, w, "RPUSH", "lst", "a", "b")
		expectInt(t, r, 2)
		writeCmd(t, w, "RENAME", "lst", "lst2")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "LRANGE", "lst2", "0", "-1")
		got := readReply(t, r)
		if got.Kind != resp.KindArray || len(got.Array) != 2 {
			t.Fatalf("LRANGE lst2 = %+v, want 2-element array", got)
		}

		// Hash survives COPY.
		writeCmd(t, w, "HSET", "h", "f", "v")
		expectInt(t, r, 1)
		writeCmd(t, w, "COPY", "h", "h2")
		expectInt(t, r, 1)
		writeCmd(t, w, "HGET", "h2", "f")
		expectBulk(t, r, "v")
	})
}
