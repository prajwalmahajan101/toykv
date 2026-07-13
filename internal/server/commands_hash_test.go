package server

import (
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

func TestHash_CRUDRoundTrip(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "HSET", "h", "f1", "v1", "f2", "v2")
		expectInt(t, r, 2)
		// Updating an existing field creates nothing new.
		writeCmd(t, w, "HSET", "h", "f1", "v1b")
		expectInt(t, r, 0)

		writeCmd(t, w, "HGET", "h", "f1")
		expectBulk(t, r, "v1b")
		writeCmd(t, w, "HGET", "h", "missing")
		expectNullBulk(t, r)
		writeCmd(t, w, "HGET", "nokey", "f")
		expectNullBulk(t, r)

		writeCmd(t, w, "HEXISTS", "h", "f2")
		expectInt(t, r, 1)
		writeCmd(t, w, "HEXISTS", "h", "nope")
		expectInt(t, r, 0)
		writeCmd(t, w, "HLEN", "h")
		expectInt(t, r, 2)

		writeCmd(t, w, "HDEL", "h", "f1", "nope")
		expectInt(t, r, 1)
		writeCmd(t, w, "HLEN", "h")
		expectInt(t, r, 1)
	})
}

func TestHash_KeysValsGetAll(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		// Single field keeps replies deterministic (map order).
		writeCmd(t, w, "HSET", "h", "f", "v")
		expectInt(t, r, 1)
		writeCmd(t, w, "HKEYS", "h")
		expectBulkArray(t, r, "f")
		writeCmd(t, w, "HVALS", "h")
		expectBulkArray(t, r, "v")

		// HGETALL on this RESP2 connection: the map frame downgrades
		// to a flat [field, value] array.
		writeCmd(t, w, "HGETALL", "h")
		expectBulkArray(t, r, "f", "v")

		// Missing key → empty results, not errors.
		writeCmd(t, w, "HKEYS", "nokey")
		expectBulkArray(t, r)
		writeCmd(t, w, "HVALS", "nokey")
		expectBulkArray(t, r)
		writeCmd(t, w, "HGETALL", "nokey")
		expectBulkArray(t, r)
		writeCmd(t, w, "HLEN", "nokey")
		expectInt(t, r, 0)
	})
}

func TestHash_EmptyCollectionDeletesKey(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "HSET", "h", "f", "v")
		expectInt(t, r, 1)
		writeCmd(t, w, "HDEL", "h", "f")
		expectInt(t, r, 1)
		writeCmd(t, w, "EXISTS", "h")
		expectInt(t, r, 0)
		writeCmd(t, w, "TYPE", "h")
		expectSimple(t, r, "none")
	})
}

func TestHash_OddPairsRejected(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "HSET", "h", "f1", "v1", "f2")
		expectErrContains(t, r, "wrong number of arguments for 'hset'")
		// Nothing was written.
		writeCmd(t, w, "EXISTS", "h")
		expectInt(t, r, 0)
	})
}

func TestHash_WrongType(t *testing.T) {
	const wrongType = "WRONGTYPE Operation against a key holding the wrong kind of value"
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "s", "v")
		expectSimple(t, r, "OK")
		for _, cmd := range [][]string{
			{"HSET", "s", "f", "v"},
			{"HGET", "s", "f"},
			{"HDEL", "s", "f"},
			{"HEXISTS", "s", "f"},
			{"HKEYS", "s"},
			{"HVALS", "s"},
			{"HLEN", "s"},
			{"HGETALL", "s"},
		} {
			writeCmd(t, w, cmd...)
			expectErrContains(t, r, wrongType)
		}
	})
}

func TestType_AllKinds(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "s", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "RPUSH", "l", "v")
		expectInt(t, r, 1)
		writeCmd(t, w, "HSET", "h", "f", "v")
		expectInt(t, r, 1)

		for key, want := range map[string]string{
			"s": "string", "l": "list", "h": "hash", "missing": "none",
		} {
			writeCmd(t, w, "TYPE", key)
			expectSimple(t, r, want)
		}
	})
}
