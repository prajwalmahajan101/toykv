package server

import (
	"errors"
	"strconv"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Atomic keyspace commands (M15): RENAME / RENAMENX / COPY. Each maps a
// single store-level move/copy to its Redis wire semantics. They are
// recorded verbatim in the AOF (like DEL) and replayed deterministically —
// no AOF format bump (ADR-0016).

// cmdRename implements RENAME key newkey. Redis returns +OK on success and
// "-ERR no such key" when the source is absent. The destination is
// overwritten; TTL and value type travel with the key.
func cmdRename(s *Server, _ *connState, argv [][]byte) resp.Value {
	src, dst := string(argv[1]), string(argv[2])
	err := s.store.Rename(src, dst)
	if errors.Is(err, store.ErrNoKey) {
		return resp.Error("ERR no such key")
	}
	// A self-rename (src == dst) mutated nothing persistent, so skip the
	// append; every other success moved the key and must be durable.
	if src != dst {
		if aerr := s.appendIfLive(argv); aerr != nil {
			return resp.Error("ERR aof append failed")
		}
	}
	return resp.OK()
}

// cmdRenameNX implements RENAMENX key newkey: move only when the
// destination does not exist. Returns :1 on a move, :0 when the
// destination exists, and "-ERR no such key" when the source is absent.
func cmdRenameNX(s *Server, _ *connState, argv [][]byte) resp.Value {
	moved, err := s.store.RenameNX(string(argv[1]), string(argv[2]))
	if errors.Is(err, store.ErrNoKey) {
		return resp.Error("ERR no such key")
	}
	if !moved {
		return resp.Int(0)
	}
	if aerr := s.appendIfLive(argv); aerr != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Int(1)
}

// cmdCopy implements COPY source destination [DB index] [REPLACE]. Returns
// :1 when the value is copied, :0 when the source is missing or the
// destination exists without REPLACE. toykv is single-DB, so DB is accepted
// only for index 0 (which every real Redis client, incl. go-redis, sends by
// default); any other index is out of range. An unknown token is a syntax
// error.
func cmdCopy(s *Server, _ *connState, argv [][]byte) resp.Value {
	replace := false
	for i := 3; i < len(argv); {
		switch upperASCII(argv[i]) {
		case "REPLACE":
			replace = true
			i++
		case "DB":
			if i+1 >= len(argv) {
				return resp.Error("ERR syntax error")
			}
			db, err := strconv.Atoi(string(argv[i+1]))
			if err != nil {
				return resp.Error("ERR value is not an integer or out of range")
			}
			if db != 0 {
				return resp.Error("ERR DB index is out of range")
			}
			i += 2
		default:
			return resp.Error("ERR syntax error")
		}
	}
	copied, err := s.store.Copy(string(argv[1]), string(argv[2]), replace)
	if errors.Is(err, store.ErrSameObject) {
		return resp.Error("ERR source and destination objects are the same")
	}
	if !copied {
		return resp.Int(0)
	}
	if aerr := s.appendIfLive(argv); aerr != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Int(1)
}
