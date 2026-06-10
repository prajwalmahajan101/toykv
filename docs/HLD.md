# toykv — High-Level Design

> System-level architecture: components, boundaries, data flow, lifecycle, and the rationale that ties them together.
> Companion to [`PRD.md`](./PRD.md) (what & why) and [`LLD.md`](./LLD.md) (types & bytes).

| Field | Value |
|---|---|
| Status | Draft — pre-implementation |
| Audience | Implementer, reviewer, future-you |
| Last updated | 2026-06-11 |

---

## 1. System context

```
                ┌─────────────────────┐
                │      operator       │
                └──────────┬──────────┘
                           │
                           ▼ (RESP / TCP)
   ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐
   │ redis-cli│ │ toykv-cli│ │ toykv-tui│ │ go-redis/v9  │
   │ (3rd-pty)│ │ (this rp)│ │ (this rp)│ │ (test only)  │
   └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────┬───────┘
        │            │            │              │
        └────────────┼────────────┼──────────────┘
                     │            │
                     └─────┬──────┘
                           │
                            ▼ (TCP :6390)
                  ┌───────────────────┐
                  │   toykv server    │
                  │   (single binary) │
                  └─────────┬─────────┘
                            │
                            ▼ (POSIX FS)
                  ┌───────────────────┐
                  │  /var/lib/toykv   │
                  │  ├─ toykv.aof     │
                  │  └─ toykv.aof.tmp │  (during rewrite)
                  └───────────────────┘
```

Three properties define the boundary:
1. **RESP is the only protocol.** The CLI and TUI are just clients. There is no in-process API.
2. **The server is stdlib-only.** No transitive deps means no vendoring debate.
3. **One file is the truth.** The store is in memory; the AOF is the durability ledger.

## 2. Component inventory

| Component | Package | Purpose | Owns |
|---|---|---|---|
| Listener | `internal/server` | Accept loop, per-conn goroutine | `net.Listener`, conn fan-out |
| RESP codec | `internal/resp` | Parse + serialise RESP2 frames | Wire format |
| Command dispatcher | `internal/server` | Route parsed command → handler | Command registry |
| Store | `internal/store` | In-memory KV map + TTL | `sync.RWMutex`, `map[string]entry` |
| TTL sweeper | `internal/store` | 1 Hz expiry eviction | Background goroutine |
| AOF writer | `internal/aof` | Append-after-commit, fsync policy | `*os.File`, fsync ticker |
| AOF replayer | `internal/aof` | Startup replay into store | One-shot reader |
| Rewriter | `internal/aof` | `BGREWRITEAOF` snapshot + rename | Worker goroutine |
| Server orchestrator | `cmd/toykv` | Wire everything together | Lifecycle, signals |
| RESP client | `internal/client` | Shared RESP client over `net.Conn` | One conn per consumer (CLI/TUI) |
| CLI engine | `internal/cli` | One-shot, REPL, piped modes; pretty-printer | Stdin/stdout, exit status |
| CLI orchestrator | `cmd/toykv-cli` | Flag parsing, mode dispatch | Lifecycle |
| TUI app | `internal/tui` | Bubble Tea program | UI state, polling timer |
| TUI orchestrator | `cmd/toykv-tui` | Flag parsing, program start | Lifecycle |

## 3. Layered architecture

Server side:

```
┌─────────────────────────────────────────────────┐
│  cmd/toykv  (flags, lifecycle, signal handling) │
└────────────┬────────────────────────────────────┘
             ▼
┌─────────────────────────────────────────────────┐
│  internal/server  (listener, conn loop,         │
│                    dispatcher, command registry)│
└──────┬───────────────────┬──────────────────────┘
       ▼                   ▼
┌──────────────┐    ┌──────────────────────┐
│ internal/resp│    │ internal/store       │
│ (RESP codec) │    │ (map + RWMutex + TTL)│
└──────────────┘    └──────────┬───────────┘
                               ▼
                    ┌──────────────────────┐
                    │ internal/aof         │
                    │ (write + replay +    │
                    │  rewrite)            │
                    └──────────┬───────────┘
                               ▼
                    ┌──────────────────────┐
                    │ os.File / POSIX FS   │
                    └──────────────────────┘
```

Dependency rule: arrows point **down** only. `aof` knows nothing about RESP. `store` knows nothing about connections. `resp` knows nothing about commands. The dispatcher is the only thing that touches all three.

Client side (both CLI and TUI share the bottom two layers):

```
cmd/toykv-cli ──► internal/cli ──┐
                                 ├──► internal/client ──► internal/resp
cmd/toykv-tui ──► internal/tui ──┘
```

`internal/client` is the single RESP-client surface — `Dial`, `Do(argv...)`, `Close`. Both consumers reuse it; no parallel implementations.

## 4. Server lifecycle

```
start
  │
  ├─ parse flags
  ├─ ensure -dir exists
  ├─ open AOF file (O_APPEND|O_CREATE|O_RDWR)
  ├─ replay AOF into store        ◄─── BLOCKS HERE; no listener yet
  ├─ launch TTL sweeper goroutine
  ├─ launch fsync ticker (if everysec)
  ├─ launch BGREWRITEAOF worker  (idle, command-triggered)
  ├─ open listener
  └─ accept loop
       │
       └─ per conn:
            ├─ read RESP frame
            ├─ dispatch command
            ├─ if mutating: store.Apply() → aof.Append() → fsync per policy
            ├─ write RESP reply
            └─ repeat until conn close or shutdown signal

shutdown (SIGINT / SIGTERM)
  │
  ├─ close listener (no new conns)
  ├─ wait for in-flight handlers (bounded; ctx with deadline)
  ├─ flush + fsync AOF
  └─ close file, exit 0
```

Two non-obvious invariants:

1. **Acknowledge after durability.** For `appendfsync=always`, the reply frame is only written once `fsync` returns. This is the contract that makes "zero acknowledged-write loss" testable.
2. **Replay blocks accept.** A client that connects mid-replay must be refused at the TCP layer (no `Accept`), not at the command layer. Otherwise the dispatcher would have to encode a "starting up" state, which leaks into the wire protocol.

## 5. Command flow — `SET key value EX 60`

```
client ──RESP──► reader ──parsed cmd──► dispatcher ──Set(k,v,ttl)──► store
                                                                       │
                                                                       ▼
                                                              acquire write lock
                                                              insert entry
                                                              release lock
                                                                       │
                                                                       ▼
                                                              aof.Append(frame)
                                                                       │
                              fsync policy switch                      │
                              ┌──────────────────────────────────────┐ │
                              │ always: fsync now                    │◄┘
                              │ everysec: ticker fsyncs in background │
                              │ no: kernel decides                    │
                              └──────────────────────────────────────┘
                                                                       │
                                                                       ▼
                                                              writer ──RESP +OK──► client
```

`GET` is the same minus the AOF step and the lock degrades to read.

## 6. AOF lifecycle

```
        APPEND                           REWRITE
   ┌────────────────┐              ┌──────────────────────────┐
   │ aof.Append(f)  │              │ BGREWRITEAOF triggered   │
   │  ├ write       │              │  ├ snapshot store        │
   │  ├ fsync (?)   │              │  ├ write to .tmp         │
   │  └ return      │              │  ├ fsync .tmp            │
   └────────────────┘              │  ├ rename .tmp → .aof    │
                                   │  └ fsync parent dir      │
                                   └──────────────────────────┘
                                               │
                                               ▼
                                  Live appends during rewrite are
                                  buffered in memory, then replayed
                                  onto the new file before the rename.
```

Atomic-rename guarantees: at any crash point, the directory contains *exactly one* of `{old .aof, new .aof}` — never a half-written file under the canonical name.

## 7. Concurrency model

| Goroutine | Count | Owns | Communicates with |
|---|---|---|---|
| accept loop | 1 | listener | spawns conn goroutines |
| conn handler | N (1 per client) | one `net.Conn` | store (lock), aof (chan or direct) |
| TTL sweeper | 1 | tick loop | store (write lock, briefly) |
| fsync ticker | 0 or 1 | timer | aof.File |
| rewrite worker | 0 or 1 | snapshot buffer | store (snapshot copy), file |
| shutdown coordinator | 1 | context, sigchan | all of the above |

**The shared state is exactly one map + one file.** All synchronisation is through the store's `RWMutex` and the AOF's single-writer goroutine pattern (or a mutex; LLD decides).

Why single-mutex (vs sharded): predictable correctness for v1; documented bottleneck under writer-heavy load; sharding postponed unless a benchmark says otherwise. (ADR-0003 once code lands.)

## 8. CLI architecture

```
┌──────────────────────────────────────────┐
│  cmd/toykv-cli   (flags, mode dispatch)  │
└──────────────────┬───────────────────────┘
                   ▼
┌──────────────────────────────────────────┐
│  internal/cli                            │
│  ┌──────────────┐  ┌──────────────────┐  │
│  │ OneShot      │  │ REPL             │  │
│  │ (argv reply) │  │ (readline loop)  │  │
│  └──────┬───────┘  └────────┬─────────┘  │
│         │   ┌─── Piped ─────┘            │
│         │   │  (stdin → cmd → reply)     │
│         ▼   ▼                            │
│  Pretty / Raw formatter                  │
└──────────────────┬───────────────────────┘
                   ▼
┌──────────────────────────────────────────┐
│  internal/client (shared with TUI)       │
└──────────────────┬───────────────────────┘
                   ▼
              TCP :6390
```

**Mode dispatch (in `cmd/toykv-cli`):**

```
if isatty(stdin) && len(args) == 0  → REPL
if !isatty(stdin)                   → Piped (read stdin lines)
if len(args) > 0                    → OneShot
```

**Why a separate engine package and not just code in `cmd/`?** Tests. `internal/cli` can be exercised with a fake `io.Reader`/`io.Writer` and a fake `client.Doer` — no subprocess needed for unit tests. The `cmd/toykv-cli` shell stays a 30-line `main`.

**Pretty printer rules** (`internal/cli/format.go`):

| RESP reply | Pretty | Raw |
|---|---|---|
| `+OK` | `OK` | `OK` |
| `+<str>` | `<str>` | `<str>` |
| `-ERR <msg>` | `(error) ERR <msg>` (stderr) | `<msg>\n` (stderr) |
| `:n` | `(integer) n` | `n` |
| `$-1` (nil) | `(nil)` | *(empty, exit 0)* |
| `$len <bytes>` | `"<bytes>"` (Go-quoted) | raw bytes + `\n` |
| `*…` | indented numbered list | one item per line |

**Exit status:**

```
0 ← +OK / :n / $… / *… (success replies)
1 ← -ERR (server returned an error)
2 ← dial / parse / I/O failure (operator problem)
```

## 9. TUI architecture

```
┌─────────────────────────────────────────┐
│  cmd/toykv-tui  (flags, program.Run)    │
└──────────────────┬──────────────────────┘
                   ▼
┌─────────────────────────────────────────┐
│  internal/tui                           │
│  ┌─────────┐  ┌─────────┐  ┌──────────┐ │
│  │ Model   │  │ Update  │  │  View    │ │
│  └────┬────┘  └────┬────┘  └────┬─────┘ │
│       └───────────┬┴───────────┘       │
│                   ▼                     │
│  msgs: tickMsg, refreshMsg, errMsg,    │
│        editDoneMsg, replyMsg, ...      │
└──────────────────┬──────────────────────┘
                   ▼
┌─────────────────────────────────────────┐
│  internal/tui/client (RESP client)      │
│   - one persistent net.Conn             │
│   - request/reply (sync per call)       │
│   - reconnect-on-error                  │
└──────────────────┬──────────────────────┘
                   ▼
              TCP :6390
```

**Polling, not subscriptions.** Refresh ticks every `-refresh` interval issue:
1. `KEYS *` → list (or filtered glob if user typed `/`)
2. For up to N visible keys: `TTL <k>` and (for the focused one) `GET <k>`.

The TUI keeps the last reply in model state. Editing flows are modal: pressing `e` opens an inline editor, on submit fires `SET <k> <new>`, then forces a refresh.

**Why polling and not pub/sub?** v1 has no pub/sub. A `WATCH`-like keyspace notification system is out of scope. Polling is honest and exercises the same wire protocol any client would use — keeps the TUI useful as an integration-test surrogate.

**Why one connection?** Pipelining is unused; the latency of `KEYS` + a handful of `TTL`/`GET` calls per tick is dominated by the server's lock acquisition, not by TCP RTT. Multiple connections would only matter under heavy parallel refresh, which is not the v1 workload.

## 10. Data flow — TUI startup

```
toykv-tui -addr 127.0.0.1:6390
   │
   ├─ dial TCP
   ├─ PING → expect PONG (handshake / liveness)
   ├─ KEYS * → seed key list
   ├─ for each key: TTL k
   ├─ render initial view
   └─ start refresh ticker (default 500ms)
        │
        └─ tick:
             ├─ KEYS * (filtered if /pattern active)
             ├─ TTL for visible keys
             ├─ GET for focused key
             ├─ diff against prior model
             └─ emit msg to Bubble Tea Update
```

If the dial or `PING` fails, the TUI renders an error screen with the address and a retry hint (`r`). It does **not** auto-reconnect silently — silent reconnects mask "wrong address" mistakes.

## 11. Error model

| Class | Where it surfaces | Wire mapping |
|---|---|---|
| Wire parse error | RESP codec | Drop connection (`-ERR Protocol error`, then close) |
| Unknown command | Dispatcher | `-ERR unknown command 'XYZ'` |
| Wrong arity | Dispatcher | `-ERR wrong number of arguments for 'CMD'` |
| Type error (`INCR` on non-int) | Store | `-ERR value is not an integer or out of range` |
| TTL error (`EXPIRE` non-existent key) | Store | `:0` reply (Redis-compatible — not an error) |
| AOF write error | AOF writer | Server logs + exits non-zero (durability lost = no graceful continue) |
| AOF replay error | AOF replayer | Server refuses to start, prints offending offset |
| FS error during rewrite | Rewriter | `-ERR background rewrite failed`, log details |

**No silent fallbacks.** A failed `fsync` under `appendfsync=always` is a process-exit event, not a logged warning. This is the durability contract — we'd rather restart than lie.

## 12. Cross-cutting concerns

### 12.1 Observability

v1 uses `log/slog` with a JSON handler. Log levels:
- `INFO` — start, shutdown, replay summary, rewrite start/end.
- `WARN` — slow fsync, slow handler.
- `ERROR` — wire parse failure, AOF write failure (followed by exit).
- `DEBUG` — per-command (off by default).

No metrics endpoint in v1. If a Prometheus exporter is needed later, it goes behind a flag and lives in `internal/metrics` (post-v1 decision).

### 12.2 Configuration

Single source: CLI flags. No config file in v1.
- `tinykv` accepts: `-addr`, `-dir`, `-appendfsync`, `-log-level`.
- `tinykv-tui` accepts: `-addr`, `-refresh`, `-log-level`.

Env vars are **not** consulted. (One source of truth for config.)

### 12.3 Security

v1 binds to localhost by default. Documented in [`SECURITY.md`](./SECURITY.md). The TUI accepts any TCP target so it can be used in test environments; the server still defaults to loopback.

### 12.4 Time

`time.Now()` is read in exactly three places: TTL set, TTL check (lazy + sweeper), and AOF rewrite snapshot timestamp. All other code is time-agnostic. Tests inject a `nowFunc` so TTL is deterministic.

## 13. Decisions deferred to LLD

- Exact RESP grammar (LLD §2).
- AOF byte layout and version byte (LLD §4).
- Store `entry` struct layout (LLD §3).
- TUI message types and state machine (LLD §6).
- Command-handler signature (LLD §5).

## 14. Decisions deferred to ADRs (recorded after code lands)

| # | Topic |
|---|---|
| 0001 | RESP2 subset & error-reply shape |
| 0002 | AOF format & version byte |
| 0003 | Single-mutex over the store |
| 0004 | TUI over RESP (no in-process coupling) |

## 15. Rejected alternatives

| Alternative | Why rejected |
|---|---|
| In-process TUI/CLI sharing the store | Forces import-graph entanglement; clients couldn't double as wire-protocol integration tests |
| Skip the CLI, "just use redis-cli" | Loses portfolio completeness; `redis-cli` won't ship with the release; CLI also validates `internal/client` before TUI consumes it |
| Single binary with `tinykv --tui` subcommand | Bloats the server build with charm deps; breaks the "server is stdlib-only" rule |
| RDB snapshots in v1 | Two persistence mechanisms double the failure surface for a learning project |
| Sharded store from day one | Premature; spec is explicit about single-mutex documented bottleneck |
| Pub/sub for TUI live updates | Bloats v1 beyond a long weekend; polling is sufficient and honest |
| Custom binary wire format | Defeats the "redis-cli works" requirement |
| Config file (YAML/TOML) | Flags are sufficient; one source of truth |
