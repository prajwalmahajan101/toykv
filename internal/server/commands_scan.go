package server

import (
	"errors"
	"path"
	"strconv"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// cmdScan implements SCAN cursor [MATCH pattern] [COUNT n] — cursor-based
// iteration over the keyspace, the large-keyspace alternative to KEYS.
// The reply is Redis's two-element array: a bulk-string next cursor
// (0 signals completion) followed by an array of matched keys. Read-only;
// identical bytes on RESP2 and RESP3.
func cmdScan(s *Server, _ *connState, argv [][]byte) resp.Value {
	cursor, err := strconv.ParseUint(string(argv[1]), 10, 64)
	if err != nil {
		return resp.Error("ERR invalid cursor")
	}

	match := "*" // no MATCH → match everything
	count := 0   // 0 → store applies the Redis default (10)
	for i := 2; i < len(argv); i += 2 {
		if i+1 >= len(argv) {
			return resp.Error("ERR syntax error")
		}
		switch upperASCII(argv[i]) {
		case "MATCH":
			match = string(argv[i+1])
		case "COUNT":
			n, err := strconv.Atoi(string(argv[i+1]))
			if err != nil || n <= 0 {
				return resp.Error("ERR syntax error")
			}
			count = n
		default:
			return resp.Error("ERR syntax error")
		}
	}

	keys, next, err := s.store.Scan(cursor, match, count)
	if err != nil {
		if errors.Is(err, path.ErrBadPattern) {
			return resp.Error("ERR bad pattern")
		}
		return resp.Error("ERR " + err.Error())
	}

	keyVals := make([]resp.Value, len(keys))
	for i, k := range keys {
		keyVals[i] = resp.Bulk([]byte(k))
	}
	return resp.Array(
		resp.Bulk([]byte(strconv.FormatUint(next, 10))),
		resp.Array(keyVals...),
	)
}
