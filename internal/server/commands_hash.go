package server

import (
	"errors"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Hash command handlers + TYPE (M11). Mutators append argv verbatim —
// HSET/HDEL replay deterministically, so no canonical rewrite is
// needed. HGETALL is the M11 command that exercises the RESP3 map
// frame: handlers return resp.Map and the writer's single downgrade
// point renders %N to RESP3 clients and a flat *2N array to RESP2
// (ADR-0011).

// cmdHSet implements HSET key field value [field value ...]. Returns
// the number of NEW fields created. Odd trailing pairs are an arity
// error (the dispatch table can only bound the minimum).
func cmdHSet(s *Server, _ *connState, argv [][]byte) resp.Value {
	if len(argv[2:])%2 != 0 {
		return resp.Error("ERR wrong number of arguments for 'hset'")
	}
	n, err := s.store.HSet(string(argv[1]), argv[2:]...)
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	if err := s.appendIfLive(argv); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Int(int64(n))
}

// cmdHGet implements HGET key field. Null when the key or field is
// missing.
func cmdHGet(s *Server, _ *connState, argv [][]byte) resp.Value {
	v, ok, err := s.store.HGet(string(argv[1]), string(argv[2]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	s.recordKeyspace(ok)
	if !ok {
		return resp.Null()
	}
	return resp.Bulk(v)
}

// cmdHDel implements HDEL key field [field ...]. Returns the count
// actually removed; appends only when at least one field was removed.
func cmdHDel(s *Server, _ *connState, argv [][]byte) resp.Value {
	fields := make([]string, 0, len(argv)-2)
	for _, f := range argv[2:] {
		fields = append(fields, string(f))
	}
	n, err := s.store.HDel(string(argv[1]), fields...)
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	if n > 0 {
		if err := s.appendIfLive(argv); err != nil {
			return resp.Error("ERR aof append failed")
		}
	}
	return resp.Int(int64(n))
}

// cmdHExists implements HEXISTS key field. Read-only 0/1 reply.
func cmdHExists(s *Server, _ *connState, argv [][]byte) resp.Value {
	ok, err := s.store.HExists(string(argv[1]), string(argv[2]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	if ok {
		return resp.Int(1)
	}
	return resp.Int(0)
}

// cmdHKeys implements HKEYS key. Read-only; order unspecified.
func cmdHKeys(s *Server, _ *connState, argv [][]byte) resp.Value {
	keys, err := s.store.HKeys(string(argv[1]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	out := make([]resp.Value, len(keys))
	for i, k := range keys {
		out[i] = resp.Bulk([]byte(k))
	}
	return resp.Array(out...)
}

// cmdHVals implements HVALS key. Read-only; order unspecified.
func cmdHVals(s *Server, _ *connState, argv [][]byte) resp.Value {
	vals, err := s.store.HVals(string(argv[1]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	out := make([]resp.Value, len(vals))
	for i, v := range vals {
		out[i] = resp.Bulk(v)
	}
	return resp.Array(out...)
}

// cmdHLen implements HLEN key. Read-only; missing keys count 0.
func cmdHLen(s *Server, _ *connState, argv [][]byte) resp.Value {
	n, err := s.store.HLen(string(argv[1]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	return resp.Int(int64(n))
}

// cmdHGetAll implements HGETALL key. Returns a map frame: native %N to
// RESP3 clients, flat field/value array to RESP2 (writer downgrade).
// A missing key is an empty map.
func cmdHGetAll(s *Server, _ *connState, argv [][]byte) resp.Value {
	flat, err := s.store.HGetAll(string(argv[1]))
	if errors.Is(err, store.ErrWrongType) {
		return wrongTypeErr()
	}
	out := make([]resp.Value, len(flat))
	for i, v := range flat {
		out[i] = resp.Bulk(v)
	}
	return resp.Map(out...)
}

// cmdType implements TYPE key. Simple-string reply: string | list |
// hash | none (Redis semantics — note: NOT an error for missing keys).
func cmdType(s *Server, _ *connState, argv [][]byte) resp.Value {
	typ, ok := s.store.Type(string(argv[1]))
	if !ok {
		return resp.String("none")
	}
	return resp.String(typ)
}
