package server

import (
	"errors"
	"fmt"
	"path"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

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

// cmdSet implements SET key value [NX|XX]. Returns +OK on success, or
// nil-bulk when NX/XX rejects the write (matches Redis).
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
	return resp.OK()
}

// cmdDel removes one or more keys and returns the count actually
// deleted.
func cmdDel(s *Server, argv [][]byte) resp.Value {
	keys := make([]string, 0, len(argv)-1)
	for _, k := range argv[1:] {
		keys = append(keys, string(k))
	}
	return resp.Int(int64(s.store.Del(keys...)))
}

// cmdExists counts how many supplied keys exist. Duplicates count
// multiple times (Redis behaviour).
func cmdExists(s *Server, argv [][]byte) resp.Value {
	keys := make([]string, 0, len(argv)-1)
	for _, k := range argv[1:] {
		keys = append(keys, string(k))
	}
	return resp.Int(int64(s.store.Exists(keys...)))
}

// cmdIncr / cmdDecr map store errors to Redis-compatible RESP error
// strings.
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
	return resp.Int(n)
}

// cmdKeys returns an array of bulk-string keys matching the pattern.
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

// cmdFlushDB removes every key.
func cmdFlushDB(s *Server, _ [][]byte) resp.Value {
	s.store.FlushDB()
	return resp.OK()
}

// cmdDBSize returns the number of keys.
func cmdDBSize(s *Server, _ [][]byte) resp.Value {
	return resp.Int(int64(s.store.DBSize()))
}
