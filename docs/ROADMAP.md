# toykv — Roadmap

Milestone-ordered execution plan to v1.0.0. Each milestone ends at a tagged, demoable state. Branch off `main`; merge via PR; **no direct commits to `main`**.

```
M0  Skeleton ──► M1  RESP echo ──► M2  In-mem KV ──► M3  TTL ──► M4  AOF ──►
M5  Compaction ──► M6  CLI ──► M7  TUI ──► M8  Integration tests ──► M9  Bench + polish ──► v1.0.0
```

CLI lands before TUI on purpose: it exercises the shared `internal/client` package end-to-end with the simplest possible UI surface, so the TUI starts on proven plumbing.

---

## M0 — Repo skeleton
**Branch:** `feat/skeleton`
- `go mod init github.com/prajwalmahajan101/toykv` (Go 1.26, matching toymq).
- Layout: `cmd/toykv/`, `cmd/toykv-cli/`, `cmd/toykv-tui/`, `internal/store/`, `internal/resp/`, `internal/aof/`, `internal/server/`, `internal/client/`, `internal/tui/`, `docs/`, `Makefile`.
- `Makefile` targets: `build`, `test`, `lint`, `bench`, `run`, `cli`, `tui`.
- `.golangci.yml` minimal.
- CI: GitHub Actions — `go build ./... && go test ./...`.
- **Exit:** `make build` produces all three binaries; each prints `--help`.

## M1 — RESP codec + TCP server
**Branch:** `feat/resp-codec`
- RESP2 reader/writer in `internal/resp/`.
- TCP accept loop, per-connection goroutine.
- Implement `PING`, `ECHO` only.
- **Exit:** `redis-cli -p 6390 ping` → `PONG`.

## M2 — In-memory store + core commands
**Branch:** `feat/store-core`
- `internal/store.Store` with `sync.RWMutex`.
- Commands: `GET`, `SET key value [NX|XX]`, `DEL`, `EXISTS`, `INCR`, `DECR`, `KEYS pattern`, `FLUSHDB`, `DBSIZE`.
- Glob matching for `KEYS` (stdlib `filepath.Match`).
- **Exit:** `redis-cli` round-trips every command above; unit tests on store + commands.

## M3 — TTL
**Branch:** `feat/ttl`
- Entry gains optional expiry timestamp.
- `SET ... EX seconds`, `EXPIRE`, `TTL`.
- Lazy check on every read/write.
- 1 Hz background sweeper.
- **Exit:** `EXPIRE k 1 && sleep 2 && GET k` → `(nil)`; sweeper evicts under no traffic.

## M4 — AOF persistence
**Branch:** `feat/aof`
- Append-after-commit pipeline.
- `appendfsync` policy: `always` | `everysec` | `no`.
- Startup replay (server blocks accept until replay done).
- **Exit:** crash-restart preserves every acknowledged write under `always`.

## M5 — Compaction
**Branch:** `feat/bgrewriteaof`
- `BGREWRITEAOF` command.
- Snapshot to `.tmp`, atomic rename.
- **Exit:** rewrite shrinks AOF after heavy churn; no data loss across rewrite + restart.

## M6 — CLI
**Branch:** `feat/cli`
- `internal/client/` — shared RESP client over `net.Conn` (consumed by CLI **and** TUI).
- `cmd/toykv-cli/` — one-shot, REPL, and piped-stdin modes (PRD §5.6).
- Pretty-print replies (`+OK`, `(nil)`, `(integer) 42`, `"value"`); `-raw` for script use.
- Exit-status mapping (`0` ok, `1` `-ERR`, `2` conn/parse failure).
- **Exit:** `toykv-cli -addr :6390 <cmd>` round-trips every command in PRD §5.1; REPL works; `echo cmd \| toykv-cli` works.

## M7 — TUI
**Branch:** `feat/tui`
- `cmd/toykv-tui/` — Bubble Tea program built on the **same** `internal/client` package from M6.
- Two-pane layout, key/value rendering, status bar, all keybindings from PRD §5.5.
- Raw-command prompt (`:`).
- **Exit:** TUI performs every mutating command from PRD §5.1 against a running server.

## M8 — Integration tests
**Branch:** `feat/integration-tests` *(must land — don't repeat toymq's dangling-branch mistake)*
- Spin server in test, exercise it via `go-redis/v9`.
- Subprocess tests for both `toykv-cli` and `redis-cli` (`redis-cli` skipped if not on PATH).
- Crash-injection test (SIGKILL during writes → restart → verify replay).
- TUI smoke test via teatest.
- **Exit:** CI green on all layers.

## M9 — Bench + polish + v1.0.0
**Branch:** `feat/release-v1`
- `make bench` → `redis-benchmark -p 6390 -t set,get -n 100000`; README records result.
- README: install, run, commands, fsync tradeoff, security note, CLI examples, TUI screenshot/GIF.
- Goreleaser config for darwin/linux × amd64/arm64 producing **three binaries**.
- Tag `v1.0.0`.

---

## v2.0 — Useful (proposed, not committed)

Make toykv usable beyond a learning demo. Each feature is self-contained — no single item doubles project scope. Cut a v2 only after v1 sees real use.

| Theme | Feature | Rationale |
|---|---|---|
| Observability | `INFO` command (uptime, dbsize, fsync policy, AOF bytes, replay stats) | Richer TUI status bar; integration tests can introspect server state |
| Observability | Prometheus `/metrics` endpoint behind `-metrics-addr` flag | Real-world deploys need RED metrics |
| Wire | `SCAN cursor [MATCH] [COUNT]` | Replaces `KEYS *` in TUI; survives large keyspaces |
| Wire | `RENAME`, `RENAMENX`, `COPY` | Atomic edits — currently racy via `GET`+`SET`+`DEL` |
| Wire | `TYPE`, `PERSIST`, `PTTL`, `PEXPIRE`, `PEXPIREAT` | Redis-compat completeness on existing key model |
| **Types** | **Lists**: `LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LLEN`, `LRANGE`, `LINDEX` | Smallest type extension; unlocks job-queue use case |
| **Types** | **Hashes**: `HSET`, `HGET`, `HDEL`, `HEXISTS`, `HKEYS`, `HVALS`, `HLEN` | Second-most-asked type after strings |
| TUI | Multi-type rendering (string / list / hash views) | Follows the type additions |
| TUI | `SCAN`-backed paging | Removes v1 large-keyspace caveat |
| **Security** | `AUTH password` + `requirepass` flag | Lifts the localhost-only ceiling |
| **Security** | TLS via `crypto/tls` (`-tls-cert`/`-tls-key`) | Same |
| Persistence | RDB snapshots alongside AOF (opt-in, `-rdb-interval`) | Faster cold starts on large datasets |
| Reliability | `-aof-truncate` flag to repair partial tails | Operationally important once auth lifts the deployment ceiling |
| Content | Hashnode post: *"Three persistence policies, one append-only file"* | Owed since v1 — write after v2 ships, not before |
| Integration | `prajwal-resilience-kit` Redis-adapter test target | First external consumer; validates AUTH + commands |

**Breaking risk:** AOF format bump to v2 to encode list/hash records → version-gated, replays both v1 and v2 records.

**Cut criteria:** AUTH + lists + hashes + `INFO` + `SCAN` shipped, with corresponding ADRs.

## v3.0 — Distributed (the `tinyraft` payoff)

This is where toykv earns the original "Raft state-machine demo" framing. Only attempt if `tinyraft` is real and needs a state machine.

| Theme | Feature |
|---|---|
| **Replication** | Embed `tinyraft`; `toykv --replicate -peers <list>` for 3-node clusters |
| Replication | Leader/follower roles; leader writes go through Raft log; followers replay |
| Replication | AOF becomes a snapshot device; Raft log is the durability source of truth |
| Consistency | `WAIT numreplicas timeout` |
| Wire | `INFO replication` (role, leader addr, lag) |
| Wire | `CLUSTER NODES` (minimal — **not** Redis Cluster's slot model) |
| TUI | Cluster view: replicas, lag, current leader, log offset |
| **Types** | **Sorted sets**: `ZADD`, `ZRANGE`, `ZRANGEBYSCORE`, `ZRANK`, `ZSCORE` |
| **Types** | **Sets**: `SADD`, `SREM`, `SMEMBERS`, `SISMEMBER`, `SINTER`, `SUNION`, `SDIFF` |
| **Pub/Sub** | `SUBSCRIBE`, `UNSUBSCRIBE`, `PUBLISH`, `PSUBSCRIBE` |
| Wire | RESP3 (`HELLO 3` + push frames for pub/sub & keyspace notifications) |
| Events | `__keyspace@0__:k` notifications for `expired`/`del`/`set` |
| Storage | Optional sharded store (only if benchmarks justify it) |

**Breaking risk:** wire bumps to RESP3 (additive — RESP2 clients still work via opt-out). AOF format may bump again for new types.

**Out of scope even at v3:** Redis Cluster slot/sharding model, Sentinel, Lua / `MULTI` / `EXEC`. (Spec rejects these explicitly.)

**Cut criteria:** 3-node cluster passes a Jepsen-style linearizability harness on `SET`/`GET`/`INCR`; `tinyraft` is independently released.

## Honest framing — pick one trajectory

The source spec is emphatic about scope creep: *"that's how you end up half-building Redis instead of finishing tinykv."* Three honest paths forward — **no commitment yet, recorded so future-you remembers the choice is live**:

| Option | Trajectory | When this is right |
|---|---|---|
| **A — ship v1, stop** | v2 and v3 stay aspirational; tracked here as backlog only | Spec-faithful. Project ships as the long-weekend artefact it was meant to be |
| **B — v1 → v2** | Make it usable single-node; stop at "complete KV with auth + types" | Realistic if v1 sees real (personal/test) usage and the gaps annoy |
| **C — v1 → v2 → v3** | Accept the Redis-clone trajectory | Only if `tinyraft` is happening and needs a state machine |

Default unless explicitly chosen: **Option A**. Decision is reviewed after v1 ships, not before.

## Status tracking

| Milestone | Status | PR | Tag |
|---|---|---|---|
| M0 | ✅ | [#1](https://github.com/prajwalmahajan101/toykv/pull/1) | `v0.0.0` |
| M1 | ⬜ | — | — |
| M2 | ⬜ | — | — |
| M3 | ⬜ | — | — |
| M4 | ⬜ | — | — |
| M5 | ⬜ | — | — |
| M6 | ⬜ | — | — |
| M7 | ⬜ | — | — |
| M8 | ⬜ | — | — |
| M9 | ⬜ | — | v1.0.0 |
