# toykv — Roadmap

Milestone-ordered execution plan to v1.0.0. Each milestone ends at a tagged, demoable state. Branch off `main`; merge via PR; **no direct commits to `main`**.

```
M0  Skeleton ──► M1  RESP echo ──► M2  Store core ──► M3  AOF + crash ──► M4  TTL ──►
M5  Compaction ──► M6  CLI ──► M7  TUI ──► M8  Integration tests ──► M9  Bench + polish ──► v1.0.0
```

## Why this order — bottom-up + risk-first

Milestones are ordered so that the **highest-blast-radius pieces ship — and are crash-tested — earliest**. The principle: each milestone owns the risk tests for the surface it introduces; M8 becomes pure protocol/end-to-end smoke instead of a catch-all for crash injection that should have lived upstream.

| Risk | Severity | Owned by |
|---|---|---|
| AOF replay correctness (silent acked-write loss on restart) | **Critical** | M3 — crash-injection tests live in M3, not M8 |
| Per-command fsync ordering (ack-after-durability invariant) | High | M3 — same |
| `BGREWRITEAOF` during concurrent writes | High | M5 — own rewrite-during-writes crash test |
| TTL lock-upgrade race under sweeper pressure | Medium | M4 — own concurrent stress test |
| Single-RWMutex contention | Medium (accepted) | M2 — own concurrent benchmark |
| Wire-protocol edge cases | Low | M1 ✅ |
| Server lifecycle drain | Low | M1 ✅ |
| `redis-cli` compat across the matrix | Low | M8 (right place — only after each piece is internally proven) |

Two ordering decisions deserve calling out:

1. **AOF (M3) before TTL (M4).** AOF is the higher-blast-radius surface; TTL state must round-trip through AOF anyway. Building AOF first means a small, focused v1 format; adding TTL forces the version-byte plumbing on a real second use case rather than as theoretical scaffolding. (Previous order had TTL at M3 and AOF at M4.)

2. **CLI (M6) before TUI (M7).** CLI exercises the shared `internal/client` package end-to-end with the simplest possible UI surface, so the TUI lands on proven plumbing.

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

## M2 — Store core + concurrent commands
**Branch:** `feat/store-core`
- `internal/store.Store` with `sync.RWMutex`; strict `[]byte` values per LLD §3.
- Commands: `GET`, `SET key value [NX|XX]`, `DEL`, `EXISTS`, `INCR`, `DECR`, `KEYS pattern`, `FLUSHDB`, `DBSIZE`.
- Glob matching for `KEYS` (stdlib `filepath.Match`).
- **Owned risk test:** concurrent stress — 100 goroutines × 1000 `INCR k` → final value exactly 100 000; race detector clean. Catches mutex-misuse bugs at the layer that introduces them, not at integration time.
- **Exit:** `redis-cli` round-trips every command; unit + concurrent stress green.

## M3 — AOF persistence + crash injection
**Branch:** `feat/aof`
*(Was M4. Moved up: AOF replay is the highest-blast-radius surface in v1, and TTL records depend on the AOF format anyway.)*
- AOF v1 format (LLD §4.1): 8-byte header + RESP-encoded records of mutating commands. Version byte present from day one; v1 records cover only `SET k v` / `DEL k` (no TTL yet — that's M4).
- `appendfsync` policy: `always` (default) | `everysec` | `no`.
- Append-after-commit: handler completes store mutation, *then* AOF append, *then* fsync per policy, *then* reply.
- Startup replay (server blocks `Accept` until replay completes).
- **Owned risk test:** **crash injection.** Subprocess test — SIGKILLs server mid-write, restarts, verifies every acked SET is present under `appendfsync=always`. This is the durability contract, proven where the code lands.
- **Exit:** crash-restart preserves every acknowledged write under `always`; replay rejects partial-tail with offset reported.

## M4 — TTL (on top of AOF v2)
**Branch:** `feat/ttl`
*(Was M3. Now lands after AOF so the format bump is the first real exercise of the version-byte design.)*
- Entry gains optional expiry timestamp (LLD §3.1).
- Commands: `SET ... EX seconds`, `SET ... PX ms`, `EXPIRE`, `TTL`, `PERSIST`.
- Lazy check on every read/write; 1 Hz background sweeper using Redis's "expire random sample" algorithm (LLD §3.3).
- **AOF format bumps to v2** — adds expiry encoding; replay accepts both v1 and v2 records (version-byte plumbing). This is the test of whether the version field actually works as designed.
- **Owned risk test:** concurrent stress — N goroutines `SET k v EX 1` while the sweeper runs; verify no spurious `(nil)` returns to an unexpired key (the lock-upgrade race window in LLD §3.2).
- **Exit:** `EXPIRE k 1 && sleep 2 && GET k` → `(nil)`; sweeper evicts under no traffic; v2 AOF replay round-trips TTLs across crash-restart.

## M5 — Compaction (`BGREWRITEAOF`)
**Branch:** `feat/bgrewriteaof`
- `BGREWRITEAOF` command (LLD §4.4).
- Snapshot current state to `.aof.tmp`, capture live appends in a side buffer during the snapshot, append the side buffer, `fsync`, atomic `rename` over canonical path, `fsync` parent dir.
- **Owned risk test:** crash during rewrite. SIGKILL mid-rewrite → restart → exactly one of `{old .aof, new .aof}` is present and replay yields a consistent state. No half-written file under the canonical name at any crash point.
- **Exit:** rewrite shrinks AOF after heavy churn; no data loss across rewrite + restart under crash injection.

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

## M8 — Integration tests (end-to-end protocol compat)
**Branch:** `feat/integration-tests` *(must land — don't repeat toymq's dangling-branch mistake)*
*(Note: crash-injection and concurrent stress live in M3/M4/M5 — the milestones that own those risks. M8's job is end-to-end protocol compat, not unit-test catch-up.)*
- Spin the shipped binary in a subprocess, exercise via `go-redis/v9`.
- Subprocess tests for `toykv-cli` (one-shot, REPL, piped) and `redis-cli` (skipped if not on PATH; CI installs `redis-tools`).
- TUI smoke test via `teatest`.
- Optional: light cross-milestone crash test as defence-in-depth (the real crash matrix is already proven in M3/M5).
- **Exit:** CI green across all layers; `redis-cli -p 6390 <cmd>` byte-compat for every command in PRD §5.1.

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

| Milestone | Title | Status | PR | Tag |
|---|---|---|---|---|
| M0 | Skeleton | ✅ | [#1](https://github.com/prajwalmahajan101/toykv/pull/1) | `m0` |
| M1 | RESP codec + PING/ECHO | ✅ | [#3](https://github.com/prajwalmahajan101/toykv/pull/3) | `m1` |
| M2 | Store core + concurrent commands | ⬜ | — | — |
| M3 | AOF persistence + crash injection | ⬜ | — | — |
| M4 | TTL (on top of AOF v2) | ⬜ | — | — |
| M5 | Compaction (`BGREWRITEAOF`) | ⬜ | — | — |
| M6 | CLI | ⬜ | — | — |
| M7 | TUI | ⬜ | — | — |
| M8 | Integration tests (protocol compat) | ⬜ | — | — |
| M9 | Bench + polish + v1.0.0 | ⬜ | — | `v1.0.0` |

## Changes from the previous roadmap

- **M3 ↔ M4 swap:** AOF now lands before TTL (was: TTL before AOF). Rationale: AOF is the highest-risk surface; TTL state needs to persist anyway; building AOF first lets the version-byte design get exercised on a real second use case when TTL adds expiry encoding.
- **Risk tests moved upstream:** each milestone owns its own crash-injection / concurrent-stress test. M3 owns the durability crash test. M4 owns the TTL race test. M5 owns the rewrite-during-writes crash test. M8 becomes pure end-to-end protocol compat instead of the catch-all for everything risky.
- **M2 explicitly owns a concurrent stress test** (was: just unit tests).
