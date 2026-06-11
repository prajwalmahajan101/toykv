package server

import (
	"errors"
	"fmt"
	"path"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// appendIfLive writes argv to the AOF when persistence is enabled and
// the server is serving live traffic. During replay s.aof is nil so
// this is a no-op, which is how the same handlers serve both paths.
//
// Returning an error from here means the durability contract failed:
// the in-memory store is now ahead of disk. Per LLD §5.4 the documented
// behaviour is to drop the conn and exit. v1 stops at "drop the conn
// and let the operator decide" — restart will re-derive state from the
// AOF, which is the source of truth on disk.
func (s *Server) appendIfLive(argv [][]byte) error {
	if s.aof == nil {
		return nil
	}
	if err := s.aof.Append(argv); err != nil {
		s.log.Error("aof append failed", "err", err, "argv0", string(argv[0]))
		return err
	}
	return nil
}

// cmdPing implements PING. With no argument it returns +PONG. With one
// argument it echoes the argument as a bulk string — matching Redis's
// behaviour.
func cmdPing(_ *Server, argv [][]byte) resp.Value {
	if len(argv) == 1 {
		return resp.String("PONG")
	}
	return resp.Bulk(argv[1])
}

// cmdEcho returns its argument as a bulk string.
func cmdEcho(_ *Server, argv [][]byte) resp.Value {
	return resp.Bulk(argv[1])
}

// cmdGet returns the bulk value, or nil-bulk when the key is missing.
func cmdGet(s *Server, argv [][]byte) resp.Value {
	v, ok := s.store.Get(string(argv[1]))
	if !ok {
		return resp.NullBulk()
	}
	return resp.Bulk(v)
}

// cmdSet implements SET key value [NX|XX]. On success it appends the
// canonical 3-arg form (no NX/XX) so the AOF can be replayed against an
// empty store without re-evaluating conditionals.
func cmdSet(s *Server, argv [][]byte) resp.Value {
	opts := store.SetOpts{Mode: store.SetAlways}
	if len(argv) == 4 {
		switch upperASCII(argv[3]) {
		case "NX":
			opts.Mode = store.SetNX
		case "XX":
			opts.Mode = store.SetXX
		default:
			return resp.Error("ERR syntax error")
		}
	}
	if ok := s.store.Set(string(argv[1]), argv[2], opts); !ok {
		return resp.NullBulk()
	}
	if err := s.appendIfLive([][]byte{[]byte("SET"), argv[1], argv[2]}); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.OK()
}

// cmdDel removes one or more keys and returns the count actually
// deleted. Appends only when at least one key was removed.
func cmdDel(s *Server, argv [][]byte) resp.Value {
	keys := make([]string, 0, len(argv)-1)
	for _, k := range argv[1:] {
		keys = append(keys, string(k))
	}
	n := s.store.Del(keys...)
	if n > 0 {
		if err := s.appendIfLive(argv); err != nil {
			return resp.Error("ERR aof append failed")
		}
	}
	return resp.Int(int64(n))
}

// cmdExists counts how many supplied keys exist. Read-only — no AOF
// append.
func cmdExists(s *Server, argv [][]byte) resp.Value {
	keys := make([]string, 0, len(argv)-1)
	for _, k := range argv[1:] {
		keys = append(keys, string(k))
	}
	return resp.Int(int64(s.store.Exists(keys...)))
}

func cmdIncr(s *Server, argv [][]byte) resp.Value { return incrDecr(s, argv, true) }
func cmdDecr(s *Server, argv [][]byte) resp.Value { return incrDecr(s, argv, false) }

func incrDecr(s *Server, argv [][]byte, up bool) resp.Value {
	var (
		n   int64
		err error
	)
	if up {
		n, err = s.store.Incr(string(argv[1]))
	} else {
		n, err = s.store.Decr(string(argv[1]))
	}
	switch {
	case errors.Is(err, store.ErrNotInteger):
		return resp.Error("ERR value is not an integer or out of range")
	case errors.Is(err, store.ErrOverflow):
		return resp.Error("ERR increment or decrement would overflow")
	case err != nil:
		return resp.Error(fmt.Sprintf("ERR %s", err))
	}
	if err := s.appendIfLive(argv); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.Int(n)
}

// cmdKeys returns an array of bulk-string keys matching the pattern.
// Read-only.
func cmdKeys(s *Server, argv [][]byte) resp.Value {
	keys, err := s.store.Keys(string(argv[1]))
	if err != nil {
		if errors.Is(err, path.ErrBadPattern) {
			return resp.Error("ERR bad pattern")
		}
		return resp.Error(fmt.Sprintf("ERR %s", err))
	}
	out := make([]resp.Value, len(keys))
	for i, k := range keys {
		out[i] = resp.Bulk([]byte(k))
	}
	return resp.Array(out...)
}

// cmdFlushDB removes every key and persists the act.
func cmdFlushDB(s *Server, argv [][]byte) resp.Value {
	s.store.FlushDB()
	if err := s.appendIfLive(argv); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.OK()
}

// cmdDBSize returns the number of keys. Read-only.
func cmdDBSize(s *Server, _ [][]byte) resp.Value {
	return resp.Int(int64(s.store.DBSize()))
}
