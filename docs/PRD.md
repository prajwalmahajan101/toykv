# toykv — Product Requirements Document

> Source of truth for *what* toykv is and *what done looks like*.
> Derived from `~/work/project-todo/projects/toykv.md`. If this drifts, that file wins.

| Field | Value |
|---|---|
| Project | toykv |
| Version target | v1.0.0 |
| Owner | Prajwal Mahajan |
| Status | Draft — pre-implementation |
| Last updated | 2026-06-11 |
| Module | `github.com/prajwalmahajan101/toykv` |
| Go version | 1.26 |
| License | MIT |

---

## 1. Problem & Why

`toymq` proves a stateful, persistent network server in Go (the **log** pattern: append, replay, durable). The natural companion is a key-value store (the **map** pattern: in-memory, mutable, expirable).

Building both demonstrates internalisation of the two foundational network-server primitives in backend systems and sets up `tinyraft` — a KV state machine is the canonical Raft demonstration.

**Non-goal:** competing with Redis. This is a learning exercise sized to a long weekend.

## 2. Target users

1. **The author** — as a learning vehicle and portfolio artefact.
2. **`redis-cli` / `go-redis/v9` users** — must be able to point existing Redis tooling at toykv for the supported command subset, unmodified.
3. **`tinyraft` (future)** — will consume toykv as a replicated state machine.
4. **`prajwal-resilience-kit` (future)** — first integration target for the Redis adapter.

## 3. Goals

- G1. RESP2-compatible wire protocol for a documented command subset.
- G2. AOF-based persistence with configurable fsync policy.
- G3. TTL semantics matching Redis (lazy + sweep).
- G4. Interactive **TUI** for live inspection and full-control mutation of the store over RESP.
- G5. Line-oriented **CLI** (`toykv-cli`) for scripting and one-shot commands over RESP.
- G6. Zero acknowledged-write loss under `appendfsync=always` across crash/restart.
- G7. Three single-binary builds (server, CLI, TUI); stdlib-only server dependencies.

## 4. Non-goals (v1)

- Lists, sets, sorted sets, hashes (string→string only).
- Pub/Sub, replication, clustering, sentinel.
- RDB snapshots, Lua scripting, transactions, `WATCH`/`MULTI`.
- Authentication (bind to localhost; document the choice).
- Multi-database (`SELECT`); single DB only.

## 5. Functional requirements

### 5.1 Wire protocol (RESP2 subset)

`PING`, `ECHO`, `GET`, `SET key value [EX seconds] [NX|XX]`, `DEL`, `EXISTS`, `EXPIRE`, `TTL`, `INCR`, `DECR`, `KEYS pattern`, `FLUSHDB`, `DBSIZE`.

Acceptance: `redis-cli -p 6390 <cmd>` returns the same reply shape as Redis for every supported command.

### 5.2 Storage

- In-memory `map[string]entry` guarded by a single `sync.RWMutex`.
- Entries carry value bytes + optional expiry timestamp.

### 5.3 TTL

- **Lazy:** every read/write checks expiry first.
- **Sweep:** background goroutine ticks at 1 Hz, samples and evicts expired keys.
- `EXPIRE` sets absolute expiry, `TTL` returns remaining seconds (`-2` no-key, `-1` no-expiry, n≥0 otherwise).

### 5.4 Persistence (AOF)

- Every mutating command appended to `toykv.aof` *after* in-memory mutation commits.
- Replay AOF on startup in order; server accepts no connections until replay completes.
- `appendfsync` policy: `always` (default) | `everysec` | `no`.
- `BGREWRITEAOF`: snapshot current state to `toykv.aof.tmp`, atomic rename over `toykv.aof`.

### 5.5 TUI (`toykv-tui`)

Separate binary. Talks RESP over TCP (no in-process coupling).

| Element | Behaviour |
|---|---|
| Left pane | Key list — type · TTL countdown · size |
| Right pane | Focused key's value (string view; raw bytes toggle) |
| Status bar | Server addr · `DBSIZE` · fsync policy · last cmd latency |
| Refresh | ~2 Hz via `KEYS *` + per-key `TTL`/`GET`; `-refresh` flag tunes interval |

**Keybindings:** `j/k` navigate · `/` filter (client-side glob) · `enter` inspect · `e` edit value · `d` `DEL` · `t` `EXPIRE` · `n` `SET` new · `i` `INCR` · `D` `DECR` · `F` `FLUSHDB` (confirm) · `r` force refresh · `:` raw RESP prompt · `q` quit.

Acceptance: every mutating command in §5.1 is reachable from the TUI.

### 5.6 CLI (`toykv-cli`)

Separate binary. Line-oriented RESP client modelled on `redis-cli` shape.

| Mode | Invocation | Behaviour |
|---|---|---|
| One-shot | `toykv-cli -addr :6390 SET k v` | Sends one command, prints reply, exits with status reflecting success |
| Interactive REPL | `toykv-cli -addr :6390` | Prompt `toykv:6390>`; readline-style history (in-memory v1); `quit`/Ctrl-D to exit |
| Piped | `echo "SET k v" \| toykv-cli -addr :6390` | Read commands from stdin one per line, print replies, exit on EOF |
| Raw | `toykv-cli -addr :6390 -raw GET k` | Print bulk-string replies as raw bytes (no quoting); script-friendly |

- Connection is a single `net.Conn` reused across commands in REPL/piped modes.
- Replies pretty-printed by default (`+OK`, `(nil)`, `(integer) 42`, `"value"`) — matches `redis-cli` output shape for the supported subset.
- `-raw` disables pretty-printing for pipelines.
- Exit status: `0` on RESP `+OK`/`:n`/`$…`, `1` on RESP `-ERR`, `2` on connection/parse failure.
- Acceptance: every command in §5.1 is invokable from both one-shot and REPL modes.

### 5.7 Config / flags

Server: `toykv -addr :6390 -dir /var/lib/toykv -appendfsync always`
CLI: `toykv-cli -addr :6390 [-raw] [cmd args...]`
TUI: `toykv-tui -addr :6390 [-refresh 500ms]`

Stdlib `flag` only.

### 5.8 v2.0 functional additions (M10–M17)

Delivered in the v2 cycle on top of the v1 surface above; all additive except
protected mode (the deliberate break that earns the major).

- **RESP3 (M10).** `HELLO [proto [AUTH user pass]]` negotiates per-connection
  protocol; RESP3 is opt-in and never sent to a RESP2 client. Richer reply
  frames (map / set / double / boolean / null / verbatim) for `HELLO 3` clients.
- **Value types (M11).** Lists (`LPUSH`/`RPUSH`/`LPOP`/`RPOP`/`LLEN`/`LRANGE`/
  `LINDEX`) and hashes (`HSET`/`HGET`/`HDEL`/`HEXISTS`/`HKEYS`/`HVALS`/`HLEN`/
  `HGETALL`), `TYPE`, and Redis-exact `WRONGTYPE` errors. AOF bumps to v3
  (replays v1/v2/v3).
- **AUTH + TLS (M12).** `-requirepass` + `AUTH`; TLS via `-tls-cert`/`-tls-key`.
  An unauthenticated connection may run only `AUTH`, `HELLO`, `PING`.
- **INFO + SCAN (M13).** `INFO` server introspection (Redis-faithful text);
  `SCAN` cursor iteration over the typed keyspace.
- **TUI v2 (M14).** Multi-type value views, `SCAN` paging, AUTH prompt,
  `INFO`-driven status bar.
- **Protected mode + atomic ops (M15).** The server refuses a non-loopback bind
  without auth/TLS by default (`-protected-mode no` overrides); atomic
  `RENAME`/`RENAMENX`/`COPY` replace the racy client-side dance.
- **Observability (M16).** OpenTelemetry logs/metrics/traces over OTLP → the
  Grafana LGTM stack, off unless `-otel-endpoint` is set; telemetry never fails
  a command.

The v2 config surface adds `-requirepass`, `-tls-cert`/`-tls-key`,
`-protected-mode`, and `-otel-endpoint`/`-otel-protocol`/`-otel-service-name`/
`-otel-sampling`/`-otel-capture-keys` to the §5.7 flags.

## 6. Non-functional requirements

| Area | Requirement |
|---|---|
| Durability | Zero acknowledged-write loss on crash under `appendfsync=always` |
| Throughput | Record `redis-benchmark -t set,get -n 100000` in README; no target number |
| Concurrency | Correct under N concurrent clients; reader-heavy workloads scale, writer-heavy bottleneck is **documented**, not solved |
| Startup | AOF replay completes before listener accepts |
| Server deps | Go stdlib only |
| CLI deps | Stdlib only (`net`, `bufio`, `flag`, `os`); no readline lib in v1 |
| TUI deps | `charmbracelet/bubbletea`, `lipgloss`, `bubbles` (scoped to `cmd/toykv-tui/`) |
| Test deps | `redis/go-redis/v9` for integration tests only |
| Platform | Linux + macOS (amd64, arm64) |

## 7. Definition of done

- [ ] `redis-cli -p 6390` works for every command in §5.1.
- [ ] Crash-and-restart loses zero acknowledged writes under `appendfsync=always` (test in `feat/integration-tests`).
- [ ] `make bench` runs `redis-benchmark -p 6390 -t set,get -n 100000`; README records the number.
- [ ] Integration tests drive the server via both `redis-cli` and `go-redis/v9`.
- [ ] `toykv-cli -addr :6390 <cmd>` round-trips every command in §5.1; REPL and piped modes also work.
- [ ] `toykv-tui -addr :6390` renders live state and performs every mutating command in §5.1.
- [ ] ADRs in `docs/adr/`: wire-protocol subset, AOF format, single-mutex model, TUI-over-RESP. **No more.**
- [ ] README documents `appendfsync` tradeoff and the bind-to-localhost auth choice.

## 8. Risks / open questions

- **Scope creep — lists/sets/hashes.** Mitigation: explicit non-goal; defer to v2 decision.
- **`KEYS *` polling at 2 Hz** stresses the server under large keyspaces. TUI must use `SCAN` *if* the server grows it; v1 documents the limitation and caps polling.
- **Single-mutex contention** under write-heavy load. Accepted; ADR'd.
- **AOF replay correctness** is the highest-blast-radius bug surface — primary integration-test target.
