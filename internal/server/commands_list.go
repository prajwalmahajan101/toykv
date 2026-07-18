package server

import (
	"context"
	"errors"
	"strconv"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// List command handlers (M11). Mutators append their argv verbatim —
// LPUSH/RPUSH/LPOP/RPOP are deterministic on replay, so no canonical
// rewrite is needed (unlike SET's EX→PXAT normalisation, ADR-0004).

// cmdLPush implements LPUSH key value [value ...]. Returns the new list
// length. Values push left-to-right, so the last argument ends at the
// head (Redis semantics).
func cmdLPush(s *Server, cs *connState, argv [][]byte) resp.Value {
	return pushCmd(cs.context(), s, argv, true)
}

// cmdRPush implements RPUSH key value [value ...].
func cmdRPush(s *Server, cs *connState, argv [][]byte) resp.Value {
	return pushCmd(cs.context(), s, argv, false)
}

func pushCmd(ctx context.Context, s *Server, argv [][]byte, front bool) resp.Value {
	var (
		n   int
		err error
	)
	if front {
		n, err = s.store.LPush(string(argv[1]), argv[2:]...)
	} else {
		n, err = s.store.RPush(string(argv[1]), argv[2:]...)
	}
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	if err := s.appendIfLive(ctx, argv); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Int(int64(n))
}

// cmdLPop implements LPOP key. Returns the popped element or null when
// the key is missing. Appends only when an element was actually
// removed — a no-op pop must not land in the AOF.
func cmdLPop(s *Server, cs *connState, argv [][]byte) resp.Value {
	return popCmd(cs.context(), s, argv, true)
}

// cmdRPop implements RPOP key.
func cmdRPop(s *Server, cs *connState, argv [][]byte) resp.Value {
	return popCmd(cs.context(), s, argv, false)
}

func popCmd(ctx context.Context, s *Server, argv [][]byte, front bool) resp.Value {
	var (
		v   []byte
		ok  bool
		err error
	)
	if front {
		v, ok, err = s.store.LPop(string(argv[1]))
	} else {
		v, ok, err = s.store.RPop(string(argv[1]))
	}
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	if !ok {
		return resp.Null()
	}
	if err := s.appendIfLive(ctx, argv); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Bulk(v)
}

// cmdLLen implements LLEN key. Read-only; missing keys count 0.
func cmdLLen(s *Server, _ *connState, argv [][]byte) resp.Value {
	n, err := s.store.LLen(string(argv[1]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	return resp.Int(int64(n))
}

// cmdLRange implements LRANGE key start stop. Read-only. Missing keys
// yield an empty array (Redis semantics).
func cmdLRange(s *Server, _ *connState, argv [][]byte) resp.Value {
	start, err1 := strconv.Atoi(string(argv[2]))
	stop, err2 := strconv.Atoi(string(argv[3]))
	if err1 != nil || err2 != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	vals, err := s.store.LRange(string(argv[1]), start, stop)
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	out := make([]resp.Value, len(vals))
	for i, v := range vals {
		out[i] = resp.Bulk(v)
	}
	return resp.Array(out...)
}

// cmdLIndex implements LINDEX key index. Read-only; out-of-range or
// missing yields null.
func cmdLIndex(s *Server, _ *connState, argv [][]byte) resp.Value {
	i, convErr := strconv.Atoi(string(argv[2]))
	if convErr != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	v, ok, err := s.store.LIndex(string(argv[1]), i)
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	s.recordKeyspace(ok)
	if !ok {
		return resp.Null()
	}
	return resp.Bulk(v)
}
