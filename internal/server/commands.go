package server

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"time"

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

// cmdSet implements SET key value [NX|XX] [EX seconds | PX milliseconds
// | EXAT unix-seconds | PXAT unix-milliseconds]. Token order is free;
// at most one of NX/XX and at most one expiry token may appear.
//
// On success it appends the canonical 3-arg form (no NX/XX, no expiry
// tokens). That keeps the AOF a strict v1 record this milestone — TTLs
// do not survive restart yet. PR C (AOF v2) introduces canonical PXAT
// encoding to close the gap.
func cmdSet(s *Server, argv [][]byte) resp.Value {
	opts, err := parseSetOptions(argv[3:], s.now())
	if err != nil {
		return resp.Error(err.Error())
	}
	if ok := s.store.Set(string(argv[1]), argv[2], opts); !ok {
		return resp.NullBulk()
	}
	if err := s.appendIfLive([][]byte{[]byte("SET"), argv[1], argv[2]}); err != nil {
		return resp.Error("ERR aof append failed")
	}
	return resp.OK()
}

// parseSetOptions consumes the trailing tokens of a SET argv (after key
// and value). Tokens may appear in any order but each group (NX/XX,
// EX/PX/EXAT/PXAT) may appear at most once.
func parseSetOptions(tokens [][]byte, now time.Time) (store.SetOpts, error) {
	var (
		opts      store.SetOpts
		modeSet   bool
		expireSet bool
	)
	opts.Mode = store.SetAlways

	for i := 0; i < len(tokens); i++ {
		switch tok := upperASCII(tokens[i]); tok {
		case "NX", "XX":
			if modeSet {
				return store.SetOpts{}, errors.New("ERR syntax error")
			}
			modeSet = true
			if tok == "NX" {
				opts.Mode = store.SetNX
			} else {
				opts.Mode = store.SetXX
			}
		case "EX", "PX", "EXAT", "PXAT":
			if expireSet {
				return store.SetOpts{}, errors.New("ERR syntax error")
			}
			if i+1 >= len(tokens) {
				return store.SetOpts{}, errors.New("ERR syntax error")
			}
			n, err := strconv.ParseInt(string(tokens[i+1]), 10, 64)
			if err != nil {
				return store.SetOpts{}, errors.New("ERR value is not an integer or out of range")
			}
			expireAt, err := computeExpireAt(tok, n, now)
			if err != nil {
				return store.SetOpts{}, err
			}
			opts.ExpireAt = expireAt
			expireSet = true
			i++
		default:
			return store.SetOpts{}, errors.New("ERR syntax error")
		}
	}
	return opts, nil
}

// computeExpireAt translates a SET expiry token + integer into an
// absolute time. EX/PX must be strictly positive (matches Redis: "ERR
// invalid expire time"); EXAT/PXAT accept any value, including past
// instants, so that AOF replay (PR C) can re-apply canonical deadlines
// without revalidation.
func computeExpireAt(token string, n int64, now time.Time) (time.Time, error) {
	switch token {
	case "EX":
		if n <= 0 {
			return time.Time{}, errors.New("ERR invalid expire time in 'set' command")
		}
		return now.Add(time.Duration(n) * time.Second), nil
	case "PX":
		if n <= 0 {
			return time.Time{}, errors.New("ERR invalid expire time in 'set' command")
		}
		return now.Add(time.Duration(n) * time.Millisecond), nil
	case "EXAT":
		return time.Unix(n, 0), nil
	case "PXAT":
		return time.UnixMilli(n), nil
	}
	// unreachable per caller's switch
	return time.Time{}, errors.New("ERR syntax error")
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

// cmdExpire implements EXPIRE key seconds. Returns 1 if the key existed
// and the TTL was set, 0 otherwise.
//
// Not appended to the AOF in this milestone (v1 record format cannot
// express a TTL). PR C adds the canonical PEXPIREAT encoding.
func cmdExpire(s *Server, argv [][]byte) resp.Value {
	return setExpiry(s, argv, time.Second)
}

// cmdPExpire implements PEXPIRE key milliseconds.
func cmdPExpire(s *Server, argv [][]byte) resp.Value {
	return setExpiry(s, argv, time.Millisecond)
}

func setExpiry(s *Server, argv [][]byte, unit time.Duration) resp.Value {
	n, err := strconv.ParseInt(string(argv[2]), 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	expireAt := s.now().Add(time.Duration(n) * unit)
	if s.store.Expire(string(argv[1]), expireAt) {
		return resp.Int(1)
	}
	return resp.Int(0)
}

// cmdPExpireAt implements PEXPIREAT key unix-milliseconds. The absolute
// form is canonical for AOF replay (PR C) — it's exposed on the wire
// for Redis compatibility and so replay never needs a non-public
// command path.
func cmdPExpireAt(s *Server, argv [][]byte) resp.Value {
	ms, err := strconv.ParseInt(string(argv[2]), 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	if s.store.Expire(string(argv[1]), time.UnixMilli(ms)) {
		return resp.Int(1)
	}
	return resp.Int(0)
}

// cmdTTL returns remaining TTL in seconds. -2 for missing/expired, -1
// for a key with no TTL, ≥0 otherwise. Sub-second remainders truncate
// (Redis behaviour; use PTTL for millisecond precision).
func cmdTTL(s *Server, argv [][]byte) resp.Value {
	return ttlReply(s.store.TTL(string(argv[1])), time.Second)
}

// cmdPTTL returns remaining TTL in milliseconds.
func cmdPTTL(s *Server, argv [][]byte) resp.Value {
	return ttlReply(s.store.TTL(string(argv[1])), time.Millisecond)
}

func ttlReply(d time.Duration, unit time.Duration) resp.Value {
	switch d {
	case store.TTLNoKey:
		return resp.Int(-2)
	case store.TTLNoExpire:
		return resp.Int(-1)
	default:
		return resp.Int(int64(d / unit))
	}
}

// cmdPersist clears any TTL on key. Returns 1 if a TTL was removed,
// 0 if the key was missing or already had no TTL.
//
// Not appended to the AOF in this milestone (no v1 representation).
func cmdPersist(s *Server, argv [][]byte) resp.Value {
	if s.store.Persist(string(argv[1])) {
		return resp.Int(1)
	}
	return resp.Int(0)
}
