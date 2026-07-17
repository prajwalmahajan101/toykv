package tui

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// tickCmd returns a tea.Cmd that emits a tickMsg after d. Started by
// Init and re-armed at the end of every refresh cycle.
func tickCmd(d time.Duration, n uint64) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{n: n} })
}

// infoStatus holds the status-bar fields parsed out of an INFO reply.
type infoStatus struct {
	dbsize  int64
	fsync   string
	uptime  int64
	clients int64
}

// fetchRefresh runs one SCAN page (cursor/count/match) + per-key TYPE and
// TTL + the focused key's typed value + INFO, then returns a refreshMsg
// stamped with `gen`. The Update loop bumps fetchGen on every nav/filter/
// page move that schedules a fetch; replies whose gen no longer matches
// are dropped instead of overwriting the current focus.
func fetchRefresh(c Doer, pattern string, cursor uint64, count int, focused string, gen uint64) tea.Cmd {
	if pattern == "" {
		pattern = "*"
	}
	return func() tea.Msg {
		start := time.Now()
		fail := func(err error) tea.Msg {
			return refreshMsg{err: err.Error(), latency: time.Since(start), gen: gen}
		}

		names, next, err := doScanPage(c, cursor, pattern, count)
		if err != nil {
			return fail(err)
		}
		infos := make([]KeyInfo, 0, len(names))
		for _, k := range names {
			kind, err := doType(c, k)
			if err != nil {
				return fail(err)
			}
			ttl, err := doIntCmd(c, "TTL", k)
			if err != nil {
				return fail(err)
			}
			infos = append(infos, KeyInfo{Name: k, TTL: ttl, Kind: kind})
		}

		info, err := doInfo(c)
		if err != nil {
			return fail(err)
		}

		msg := refreshMsg{keys: infos, nextCursor: next, info: info, latency: time.Since(start), gen: gen}
		if focused != "" {
			kind := kindOf(infos, focused)
			v, size, err := doValue(c, focused, kind)
			if err != nil {
				msg.err = err.Error()
				return msg
			}
			msg.value = v
			msg.hasVal = true
			for i := range msg.keys {
				if msg.keys[i].Name == focused {
					msg.keys[i].Size = size
					break
				}
			}
		}
		return msg
	}
}

// runMutating issues argv via the client and emits a replyMsg.
// refresh=true asks the Update loop to schedule an immediate refetch.
func runMutating(c Doer, refresh bool, argv ...string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		v, err := c.Do(argv...)
		if err != nil {
			return replyMsg{err: err.Error(), latency: time.Since(start), refresh: refresh}
		}
		errStr := ""
		if v.Kind == resp.KindError {
			errStr = v.Str
		}
		return replyMsg{value: v, latency: time.Since(start), err: errStr, refresh: refresh}
	}
}

// authCmd sends AUTH <pass> and reports the outcome via replyMsg. On
// success the Update loop schedules a refetch (refresh=true).
func authCmd(c Doer, pass string) tea.Cmd {
	return runMutating(c, true, "AUTH", pass)
}

// doScanPage issues SCAN cursor MATCH pattern COUNT count and returns the
// page's keys plus the next cursor (0 ⇒ last page). The reply is Redis's
// two-element array: [next-cursor(bulk), [keys(bulk)...]].
func doScanPage(c Doer, cursor uint64, pattern string, count int) (keys []string, next uint64, err error) {
	v, err := c.Do("SCAN", strconv.FormatUint(cursor, 10), "MATCH", pattern, "COUNT", strconv.Itoa(count))
	if err != nil {
		return nil, 0, err
	}
	if v.Kind == resp.KindError {
		return nil, 0, &respErr{msg: v.Str}
	}
	if v.IsNull || len(v.Array) != 2 {
		return nil, 0, nil
	}
	nextRaw := v.Array[0]
	if nextRaw.Kind == resp.KindBulkString {
		if n, perr := strconv.ParseUint(string(nextRaw.Bytes), 10, 64); perr == nil {
			next = n
		}
	}
	list := v.Array[1]
	out := make([]string, 0, len(list.Array))
	for _, el := range list.Array {
		if el.Kind == resp.KindBulkString && !el.IsNull {
			out = append(out, string(el.Bytes))
		}
	}
	return out, next, nil
}

// doType issues TYPE key and returns the type label (string|list|hash|
// none). TYPE replies as a simple string.
func doType(c Doer, key string) (string, error) {
	v, err := c.Do("TYPE", key)
	if err != nil {
		return "", err
	}
	if v.Kind == resp.KindError {
		return "", &respErr{msg: v.Str}
	}
	if v.Kind == resp.KindSimpleString {
		return v.Str, nil
	}
	if v.Kind == resp.KindBulkString && !v.IsNull {
		return string(v.Bytes), nil
	}
	return KindNone, nil
}

// doValue fetches the focused key's value according to its kind and
// returns the raw reply plus a size measure (string: bytes; list:
// elements; hash: fields). Unknown/none kinds fall back to GET.
func doValue(c Doer, key, kind string) (resp.Value, int, error) {
	switch kind {
	case KindList:
		v, err := c.Do("LRANGE", key, "0", "-1")
		if err != nil {
			return resp.Value{}, 0, err
		}
		return v, len(v.Array), nil
	case KindHash:
		v, err := c.Do("HGETALL", key)
		if err != nil {
			return resp.Value{}, 0, err
		}
		// RESP2 HGETALL is a flat [f1,v1,...]; each pair is one field.
		return v, len(v.Array) / 2, nil
	default:
		v, err := c.Do("GET", key)
		if err != nil {
			return resp.Value{}, 0, err
		}
		size := 0
		if v.Kind == resp.KindBulkString && !v.IsNull {
			size = len(v.Bytes)
		}
		return v, size, nil
	}
}

// doInfo issues INFO and parses the status-bar fields out of the
// verbatim/bulk body.
func doInfo(c Doer) (infoStatus, error) {
	v, err := c.Do("INFO")
	if err != nil {
		return infoStatus{}, err
	}
	if v.Kind == resp.KindError {
		return infoStatus{}, &respErr{msg: v.Str}
	}
	return parseInfo(string(v.Bytes)), nil
}

// parseInfo pulls the fields the status bar shows out of an INFO body.
// The body is Redis's "# Section\r\nkey:value\r\n" text; db0:keys= is only
// present when the keyspace is non-empty, so dbsize defaults to 0.
func parseInfo(body string) infoStatus {
	var s infoStatus
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "appendfsync:"):
			s.fsync = strings.TrimPrefix(line, "appendfsync:")
		case strings.HasPrefix(line, "uptime_in_seconds:"):
			s.uptime = atoi64(strings.TrimPrefix(line, "uptime_in_seconds:"))
		case strings.HasPrefix(line, "connected_clients:"):
			s.clients = atoi64(strings.TrimPrefix(line, "connected_clients:"))
		case strings.HasPrefix(line, "db0:keys="):
			// db0:keys=N[,expires=...] — take the first field.
			rest := strings.TrimPrefix(line, "db0:keys=")
			if i := strings.IndexByte(rest, ','); i >= 0 {
				rest = rest[:i]
			}
			s.dbsize = atoi64(rest)
		}
	}
	return s
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// kindOf returns the TYPE label recorded for name in the current page,
// or "" if not found.
func kindOf(infos []KeyInfo, name string) string {
	for _, k := range infos {
		if k.Name == name {
			return k.Kind
		}
	}
	return ""
}

func doIntCmd(c Doer, argv ...string) (int64, error) {
	v, err := c.Do(argv...)
	if err != nil {
		return 0, err
	}
	if v.Kind == resp.KindError {
		return 0, &respErr{msg: v.Str}
	}
	return v.Int, nil
}

type respErr struct{ msg string }

func (e *respErr) Error() string { return e.msg }
