package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// tickCmd returns a tea.Cmd that emits a tickMsg after d. Started by
// Init and re-armed at the end of every refresh cycle.
func tickCmd(d time.Duration, n uint64) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{n: n} })
}

// fetchRefresh runs KEYS <pattern> + per-key TTL + DBSIZE + (if focused
// is non-empty) GET <focused>, then returns a refreshMsg stamped with
// `gen`. The Update loop bumps fetchGen on every nav/filter that
// schedules a fetch; replies whose gen no longer matches are dropped
// instead of overwriting the current focus.
func fetchRefresh(c Doer, pattern, focused string, gen uint64) tea.Cmd {
	if pattern == "" {
		pattern = "*"
	}
	return func() tea.Msg {
		start := time.Now()
		keys, err := doKeys(c, pattern)
		if err != nil {
			return refreshMsg{err: err.Error(), latency: time.Since(start), gen: gen}
		}
		infos := make([]KeyInfo, 0, len(keys))
		for _, k := range keys {
			ttl, err := doIntCmd(c, "TTL", k)
			if err != nil {
				return refreshMsg{err: err.Error(), latency: time.Since(start), gen: gen}
			}
			infos = append(infos, KeyInfo{Name: k, TTL: ttl})
		}
		dbsize, err := doIntCmd(c, "DBSIZE")
		if err != nil {
			return refreshMsg{err: err.Error(), latency: time.Since(start), gen: gen}
		}
		msg := refreshMsg{keys: infos, dbsize: dbsize, latency: time.Since(start), gen: gen}
		if focused != "" {
			v, err := c.Do("GET", focused)
			if err != nil {
				return refreshMsg{
					keys: infos, dbsize: dbsize,
					err: err.Error(), latency: time.Since(start), gen: gen,
				}
			}
			msg.value = v
			msg.hasVal = true
			if v.Kind == resp.KindBulkString && !v.IsNull {
				for i := range msg.keys {
					if msg.keys[i].Name == focused {
						msg.keys[i].Size = len(v.Bytes)
						break
					}
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

func doKeys(c Doer, pattern string) ([]string, error) {
	v, err := c.Do("KEYS", pattern)
	if err != nil {
		return nil, err
	}
	if v.Kind == resp.KindError {
		return nil, &respErr{msg: v.Str}
	}
	if v.IsNull {
		return nil, nil
	}
	out := make([]string, 0, len(v.Array))
	for _, el := range v.Array {
		if el.Kind == resp.KindBulkString && !el.IsNull {
			out = append(out, string(el.Bytes))
		}
	}
	return out, nil
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
