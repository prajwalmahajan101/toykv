# toykv — Roadmap

Milestone-ordered execution plan to v1.0.0. Each milestone ends at a tagged, demoable state. Branch off `main`; merge via PR; **no direct commits to `main`**.

```
v1  M0 Skeleton ─► M1 RESP echo ─► M2 Store core ─► M3 AOF + crash ─► M4 TTL ─►
    M5 Compaction ─► M6 CLI ─► M7 TUI ─► M8 Integration tests ─► M9 Bench + polish ─► v1.0.0

v2  M10 RESP3 ─► M11 Types (lists+hashes, AOF v3) ─► M12 AUTH/TLS ─►
    M13 INFO + SCAN ─► M14 TUI v2 ─► M15 Hardening (protected-mode + atomic ops) ─►
    M16 Bench + polish ─► v2.0.0   (committed — active plan)
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

## v2.0 — Useful (committed — the active plan post-v1)

Make toykv usable beyond a learning demo: authenticated, **safe-by-default**, typed, observable, single-node. Same execution discipline as v1 — branch off `main`, merge via PR, **no direct commits to `main`** — and the same governing principle: **the highest-blast-radius surface ships and is crash-tested earliest, and each milestone owns its own risk test.**

> **Earning the `2.0.0` tag (decided 2026-07-13).** Everything in M10–M14 is *additive* — RESP3 is opt-in, AOF v3 replays old formats — so by semver alone this work is a `1.x`, not a forced major. `2.0.0` is *earned*, not defaulted, by **M15's protected-mode change**: v1 served any bind with no auth; v2 refuses a non-loopback bind without auth unless explicitly overridden. That single deliberate break to the deployment contract is what makes the major honest. M15 also promotes **atomic `RENAME`/`RENAMENX`/`COPY`** out of the backlog — closing a real correctness gap (racy today via `GET`+`SET`+`DEL`) — so the release ships one breaking change and one correctness win, not a pile of additions hoping volume justifies the number.

> **Decision recorded 2026-07-13.** v1.0.0 shipped 2026-06-17; after ~4 weeks of real use the trajectory decision (previously deferred) is now made: **run the full M10–M16 arc — Option B (v1 → v2)**. See [Honest framing](#honest-framing--pick-one-trajectory).
>
> The honest caveats still stand, subordinate to that commitment: the backlog tracker (`project-todo/projects/toykv.md`) records that a *minimal* v2 is **AUTH + TLS only**, and that **v3 (Raft-distributed) is the real downstream dependency** — blocked on `ToyRaft` shipping as a vendorable library. So if scope tightens mid-cycle, v2 can be trimmed back to M12 (AUTH+TLS) without abandoning the release; and jumping to v3 stays a live option the moment `ToyRaft` is ready.

### Why this order — bottom-up + risk-first (continued)

Numbering continues the single v1 sequence (**M10–M16**); the release tag is `v2.0.0`.

| Risk | Severity | Owned by |
|---|---|---|
| Tagged-union store model + **AOF v3** replay of typed records (silent corruption of a list/hash on restart) | **Critical** | M11 — crash injection for typed round-trip |
| `WRONGTYPE` enforcement across every command | High | M11 — same |
| AOF backward-compat (must replay v1 / v2 / **v3** records) | High | M11 — same |
| Auth-state leak across connections / partial-auth command execution | High | M12 — concurrent auth stress |
| TLS handshake + connection drain | Medium | M12 — same |
| RESP3 downgrade — a RESP2 client's replies must stay byte-identical | Medium | M10 — dual-protocol compat sweep |
| SCAN cursor stability under concurrent mutation | Medium | M13 — cursor-guarantee stress |
| Unsafe default exposure — a non-loopback bind serving unauthenticated | **High** | M15 — protected-mode bind-refusal test |
| `RENAME` / `COPY` atomicity under concurrent mutation | Medium | M15 — concurrent-rename stress |
| `INFO` field correctness | Low | M13 |

Two ordering decisions deserve calling out:

1. **RESP3 (M10) before types (M11).** RESP3 is the wire foundation — additive, low store-blast-radius, opt-in via `HELLO 3`. Building the protocol-negotiation and type-tag plumbing first means types then exercise RESP3's map/set replies (`HGETALL` → map) as a real second use case — the same logic that put AOF before TTL in v1.
2. **Types (M11) before AUTH (M12).** Types are the highest-blast-radius surface in v2 (store model + AOF format bump), so they ship and get crash-tested early. AUTH/TLS is self-contained connection-layer work that doesn't depend on the type system and can slot in once the risky format change is proven.
3. **Hardening (M15) after AUTH (M12).** Protected mode gates on `requirepass` existing, so it can only land once M12 has shipped auth. It sits at M15 — after the feature milestones — because it is the *deliberate breaking change that earns the major*, and it reads most honestly as the last gate before the release milestone rather than buried inside M12. If v2 ever trims to the minimal AUTH+TLS cut, protected mode should be pulled forward into M12 — exposing auth without a safe default is the exact risk the minimal cut would otherwise ship.

---

### M10 — RESP3 wire upgrade
**Branch:** `feat/resp3` · **Depends on:** nothing new (v1 RESP2 codec) · **ADR:** RESP3 negotiation & per-connection protocol state
- `HELLO [proto [AUTH user pass]]` for protocol negotiation; per-connection protocol-version state.
- New RESP3 encoders in `internal/resp/`: `%` map, `~` set, `,` double, `#` boolean, `_` null, `=` verbatim string, `>` push-frame scaffold.
- Default stays RESP2; RESP3 is opt-in and never sent to a client that hasn't said `HELLO 3`.
- **Owned risk test:** dual-protocol compat sweep — every command in PRD §5.1 replies byte-identically to a RESP2 client, while a RESP3 client gets the richer frames. RESP2 clients (incl. `redis-cli` default) unaffected.
- **Exit:** `HELLO 3` upgrades a connection; `redis-cli -3 -p 6390 <cmd>` round-trips; RESP2 golden replies unchanged.

### M11 — Value types: lists + hashes (AOF v3)
**Branch:** `feat/types` · **Depends on:** M10 (typed replies ride RESP3 map/set frames) · **ADR:** tagged-union store model + AOF v3 format
*(The highest-blast-radius milestone in v2 — it owns the crash test.)*
- `internal/store` entry becomes a **tagged union** (string / list / hash) with a type byte.
- Lists: `LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LLEN`, `LRANGE`, `LINDEX`.
- Hashes: `HSET`, `HGET`, `HDEL`, `HEXISTS`, `HKEYS`, `HVALS`, `HLEN`.
- `TYPE key` — folded in here (practically required once keys are typed; the TUI and `SCAN` both lean on it).
- `WRONGTYPE` error on any command applied to the wrong value type.
- **AOF format bumps to v3** — encodes list/hash mutating records; replay accepts **v1, v2, and v3** records (version-byte plumbing, now on its second real exercise after TTL added v2).
- **Owned risk test:** **crash injection.** Subprocess test — SIGKILL mid-write of list/hash ops, restart, verify every acked typed mutation is present under `appendfsync=always`; partial-tail rejected with offset reported.
- **Exit:** every list/hash command round-trips via `redis-cli`; `WRONGTYPE` matches Redis; v3 AOF replay reconstructs typed values across crash-restart.

### M12 — AUTH + TLS
**Branch:** `feat/auth-tls` · **Depends on:** M10 (`HELLO … AUTH` form); otherwise self-contained connection-layer work · **ADR:** AUTH model + TLS termination
- `requirepass` flag + `AUTH password` (and the `HELLO 3 AUTH user pass` form from M10).
- TLS via stdlib `crypto/tls` behind `-tls-cert` / `-tls-key`; wraps the accept loop, drains cleanly.
- Command gating: an unauthenticated connection may run only `AUTH`, `HELLO`, and `PING`; everything else returns `-NOAUTH`.
- **Owned risk test:** concurrent auth stress — N connections racing `AUTH` + commands; no auth-state bleed across goroutines, no command executes before its connection authenticates.
- **Exit:** `redis-cli -a <pass>` authenticates and round-trips; wrong/absent password rejected; `redis-cli --tls --cert … --key …` completes a TLS handshake and round-trips.

### M13 — INFO + SCAN
**Branch:** `feat/info-scan` · **Depends on:** M11 (`SCAN` iterates the typed keyspace; `TYPE` per key) + M10 (`INFO` as RESP3 map) · **ADR:** none new — reuses RESP3 (M10) and store (M11) decisions
- `INFO` — uptime, `dbsize`, `appendfsync` policy, AOF byte size, replay stats; served as a RESP3 map when the client is on RESP3, a bulk string on RESP2.
- `SCAN cursor [MATCH pattern] [COUNT n]` — cursor-based iteration over the (now typed) keyspace; replaces `KEYS *` for large keyspaces.
- **Owned risk test:** cursor-guarantee stress — a full `SCAN` loop under concurrent writes returns every key that was present for the entire scan (Redis's SCAN guarantee), with no crash on a stale cursor.
- **Exit:** `INFO` fields match live server state; a `SCAN` loop enumerates the full keyspace; integration tests introspect server state via `INFO`.

### M14 — TUI v2
**Branch:** `feat/tui-v2` · **Depends on:** M11 (`TYPE` views), M12 (AUTH prompt), M13 (`SCAN` paging + `INFO` status bar) · **ADR:** none new — consumes M11–M13 surfaces
- Multi-type rendering in the value pane: distinct string / list / hash views driven by `TYPE`.
- `SCAN`-backed paging in the keys pane — removes the v1 large-keyspace caveat.
- AUTH prompt on connect when the server has `requirepass`; INFO-driven status bar (fsync policy, dbsize, uptime).
- Lands on proven plumbing — same `internal/client` package, extended for the new commands (v1's CLI-before-TUI logic).
- **Owned risk test:** `teatest` smoke per type view + a paging scenario against a running server.
- **Exit:** the TUI renders every value type and pages a large keyspace; all v1 keybindings still pass.

### M15 — Hardening: protected mode + atomic keyspace ops (the earned 2.0.0)
**Branch:** `feat/hardening` · **Depends on:** M12 (protected mode gates on `requirepass`; auth must exist) · **ADR:** protected-mode default + atomic keyspace ops
*(This milestone is what turns `2.0.0` from an epoch bump into a semver-earned major: one deliberate breaking change + one correctness fix.)*
- **Protected mode — the deliberate breaking change.** By default the server refuses to *start* when bound to a non-loopback address with neither `requirepass` nor TLS configured; it exits non-zero with a clear message pointing at the fix. Opt out with `-protected-mode no` (or `-protected-mode off`). This flips v1's implicit "binds anywhere, serves anyone" contract — the change that makes the major honest. Loopback binds and auth'd/TLS binds are unaffected.
- **Atomic `RENAME` / `RENAMENX` / `COPY`** — promoted from the v2.x backlog into committed scope. A single store-mutex-guarded operation, not the racy `GET`+`SET`+`DEL` dance. `RENAME` overwrites the destination; `RENAMENX` fails with `:0` if the destination exists; `COPY src dst [REPLACE]`. TTL and value type (string/list/hash from M11) travel with the key. **No AOF format bump** — these are new mutating commands recorded verbatim like `DEL`, replayed deterministically; the v3 format from M11 is untouched.
- **Owned risk test:** (1) **protected-mode bind refusal** — a server started on a non-loopback addr with no auth exits non-zero with the documented error; the same bind with `requirepass`, TLS, a loopback addr, or `-protected-mode no` starts cleanly. (2) **concurrent-rename atomicity** — N goroutines `RENAME` the same source key; exactly one succeeds, no torn intermediate state, race detector clean, and the surviving key keeps its TTL and type.
- **Exit:** non-loopback + no-auth bind refuses to start (override works and is logged); `RENAME`/`RENAMENX`/`COPY` match Redis semantics incl. TTL preservation; crash-restart replays renames from the AOF with no format change.

### M16 — Bench + polish + v2.0.0
**Branch:** `feat/release-v2` · **Depends on:** M10–M15 all merged
- Re-run `make bench` with typed workloads; README records the new numbers.
- README + [SECURITY](./SECURITY.md) update — auth/TLS + protected mode lift the localhost-only ceiling; document the new "deployable, safe-by-default" posture and the protected-mode override.
- PRD / HLD / LLD deltas for types, RESP3, auth, and protected mode.
- ADR reconciliation: the **five** v2 ADRs (RESP3 negotiation, tagged-union store model, AOF v3 format, AUTH/TLS, protected-mode default + atomic keyspace ops) are each written **after** their owning milestone merges (M10/M11/M12/M15) per [`docs/adr/README.md`](./adr/README.md); M16 only verifies all five have landed and the index is current — it does not batch-write them at release time.
- **Release-hardening gate (must all pass before the tag):**
  - Fix or deliberately quarantine the flaky `TestAOF_CrashInjection_DuringRewrite/late-kill` — no known-flaky crash-durability test at release.
  - Fix `.golangci.yml` (add the `version:` schema key) so `make lint` runs in CI again — a release must not ship with a non-functional lint gate.
  - **Security review** of the M12 auth/TLS + M15 protected-mode surface: timing-safe password compare, no command execution before auth, TLS cert/key handling and drain, protected-mode bypass audit.
  - **Explicit v1→v2 AOF upgrade test** — load a real v1/v2 AOF file written by a v1 binary and verify in-place replay under the v3 reader.
  - Confirm the `2.0.0` call is earned (protected mode is the breaking change) and record it in the release notes.
- Goreleaser reused from v1; tag `v2.0.0`.

### v2.x backlog (not in committed scope)

Deferred from the committed M10–M16 cut so v2 stays a focused "usable single-node" release, not a Redis re-implementation:

| Theme | Item | Note |
|---|---|---|
| Observability | Prometheus `/metrics` endpoint behind `-metrics-addr` | Real-world deploys need RED metrics; not required for "usable single-node" |
| Persistence | RDB snapshots alongside AOF (opt-in, `-rdb-interval`) | Faster cold starts on large datasets |
| Reliability | `-aof-truncate` flag to repair partial tails | Operationally important once auth lifts the deployment ceiling |
| Content | Hashnode post: *"Three persistence policies, one append-only file"* | Owed since v1 — write after v2 ships, not before |
| Integration | `prajwal-resilience-kit` Redis-adapter test target | First external consumer; validates AUTH + commands |

**Breaking risk:** two, both deliberate and documented. (1) AOF format bump to **v3** to encode list/hash records → version-gated, replays v1, v2, and v3 records (M11). (2) **Protected mode** (M15) refuses a non-loopback bind without auth — a behavioural break to v1's deployment contract, overridable via `-protected-mode no`. This is the change that earns the `2.0.0` major; RESP3 is wire-only and additive and never forces it.

**Cut criteria:** RESP3 + lists + hashes + AUTH + TLS + `INFO` + `SCAN` + **protected mode** + **atomic `RENAME`/`RENAMENX`/`COPY`** shipped, each feature area with a corresponding ADR, and the M16 release-hardening gate (flaky-test fix, lint config, security review, v1→v2 AOF upgrade test) all green.

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
| **Pub/Sub** | `SUBSCRIBE`, `UNSUBSCRIBE`, `PUBLISH`, `PSUBSCRIBE` (built on the RESP3 push frames delivered in v2 M10) |
| Events | `__keyspace@0__:k` notifications for `expired`/`del`/`set` |
| Storage | Optional sharded store (only if benchmarks justify it) |

**Breaking risk:** AOF format may bump again for new types (sets / sorted sets). The RESP3 wire bump already landed in v2 (M10) — additive, RESP2 clients still work via opt-out — so v3 pub/sub reuses its push frames rather than introducing a new protocol break.

**Out of scope even at v3:** Redis Cluster slot/sharding model, Sentinel, Lua / `MULTI` / `EXEC`. (Spec rejects these explicitly.)

**Cut criteria:** 3-node cluster passes a Jepsen-style linearizability harness on `SET`/`GET`/`INCR`; `tinyraft` is independently released.

## Honest framing — pick one trajectory

The source spec is emphatic about scope creep: *"that's how you end up half-building Redis instead of finishing tinykv."* Three honest paths were on the table — recorded here so the reasoning behind the choice stays legible:

| Option | Trajectory | When this is right |
|---|---|---|
| **A — ship v1, stop** | v2 and v3 stay aspirational; tracked here as backlog only | Spec-faithful. Project ships as the long-weekend artefact it was meant to be |
| **B — v1 → v2** | Run the M10–M16 arc (RESP3, types, AUTH/TLS, INFO/SCAN, TUI v2, hardening); stop at "complete usable single-node KV" | Realistic if v1 sees real (personal/test) usage and the gaps annoy. Minimal viable v2 is AUTH+TLS + protected mode (M12 + the M15 protected-mode bullet) — the rest is optional even within v2 |
| **C — v1 → v3 (skip or trim v2)** | Jump to the Raft-distributed payoff — the actual downstream dependency | Only once `ToyRaft` ships as a vendorable library. v3 is blocked on `ToyRaft` v1.0-rc1; v2 can be skipped or trimmed to AUTH+TLS if scope is tight, since downstream work needs v3 (multi-node), not v2 |

**Decided 2026-07-13: Option B** — v1.0.0 has seen ~4 weeks of real use since the 2026-06-17 tag, and the gaps that matter (no auth, string-only values, `KEYS *`-only iteration) are worth closing. The full M10–M16 arc is now the committed active plan. The asymmetry the tracker records still holds: **v2 is polish; v3 is the real downstream dependency** — so Option C ("trim v2 to AUTH+TLS, jump to v3") stays live the moment `ToyRaft` ships as a vendorable library, and v2 can fall back to M12-only if scope tightens without abandoning the release.

## Status tracking

| Milestone | Title | Status | PR | Tag |
|---|---|---|---|---|
| M0 | Skeleton | ✅ | [#1](https://github.com/prajwalmahajan101/toykv/pull/1) | `m0` |
| M1 | RESP codec + PING/ECHO | ✅ | [#3](https://github.com/prajwalmahajan101/toykv/pull/3) | `m1` |
| M2 | Store core + concurrent commands | ✅ | [#6](https://github.com/prajwalmahajan101/toykv/pull/6) | `m2` |
| M3 | AOF persistence + crash injection | ✅ | [#8](https://github.com/prajwalmahajan101/toykv/pull/8) | `m3` |
| M4 | TTL (on top of AOF v2) | ✅ | [#10](https://github.com/prajwalmahajan101/toykv/pull/10) [#11](https://github.com/prajwalmahajan101/toykv/pull/11) [#12](https://github.com/prajwalmahajan101/toykv/pull/12) | `m4` |
| M5 | Compaction (`BGREWRITEAOF`) | ✅ | [#13](https://github.com/prajwalmahajan101/toykv/pull/13) [#14](https://github.com/prajwalmahajan101/toykv/pull/14) [#15](https://github.com/prajwalmahajan101/toykv/pull/15) | `m5` |
| M6 | CLI | ✅ | [#15](https://github.com/prajwalmahajan101/toykv/pull/15) [#16](https://github.com/prajwalmahajan101/toykv/pull/16) | `m6` |
| M7 | TUI | ✅ | [#17](https://github.com/prajwalmahajan101/toykv/pull/17) [#19](https://github.com/prajwalmahajan101/toykv/pull/19) | `m7` |
| M8 | Integration tests (protocol compat) | ✅ | _see `feat/integration-tests` PR series_ | `m8` |
| M9 | Bench + polish + v1.0.0 | ✅ | _see `feat/release-v1` PR_ | `v1.0.0` |
| M10 | RESP3 wire upgrade | ✅ | [#26](https://github.com/prajwalmahajan101/toykv/pull/26) | `m10` |
| M11 | Value types: lists + hashes (AOF v3) | ✅ | [#28](https://github.com/prajwalmahajan101/toykv/pull/28) | `m11` |
| M12 | AUTH + TLS | ✅ | [#30](https://github.com/prajwalmahajan101/toykv/pull/30) | `m12` |
| M13 | INFO + SCAN | ⏳ Planned (committed) | — | `m13` |
| M14 | TUI v2 | ⏳ Planned (committed) | — | `m14` |
| M15 | Hardening: protected mode + atomic keyspace ops | ⏳ Planned (committed) | — | `m15` |
| M16 | Bench + polish + v2.0.0 | ⏳ Planned (committed) | — | `v2.0.0` |

## Changes from the previous roadmap

- **M3 ↔ M4 swap:** AOF now lands before TTL (was: TTL before AOF). Rationale: AOF is the highest-risk surface; TTL state needs to persist anyway; building AOF first lets the version-byte design get exercised on a real second use case when TTL adds expiry encoding.
- **Risk tests moved upstream:** each milestone owns its own crash-injection / concurrent-stress test. M3 owns the durability crash test. M4 owns the TTL race test. M5 owns the rewrite-during-writes crash test. M8 becomes pure end-to-end protocol compat instead of the catch-all for everything risky.
- **M2 explicitly owns a concurrent stress test** (was: just unit tests).

**v2 additions (M10–M16):**

- **RESP3 pulled forward v3.0 → v2.0.** RESP3 was originally a v3 wire item; it moves up to M10 as the v2 wire foundation so the type work (M11) exercises its map/set replies as a real second use case. v3 pub/sub then reuses the push frames rather than owning the protocol break.
- **Pub/sub stays v3.** Only the RESP3 transport moves to v2; `SUBSCRIBE`/`PUBLISH` and keyspace notifications remain v3.
- **`TYPE` folded into the types milestone (M11)** rather than the loose wire-completeness list — it's practically required once keys are typed (TUI + `SCAN` depend on it).
- **Single AOF bump this cycle (v3), owned by M11** — mirrors v1's discipline of one focused format change per milestone that needs it.
- **v2 is now committed (decided 2026-07-13).** The earlier draft framed the whole M10–M15 arc as "proposed, optional — not committed." After ~4 weeks of v1.0.0 in real use, the trajectory decision is made: **Option B (v1 → v2), full arc.** The section header, banner, status table, and [Honest framing](#honest-framing--pick-one-trajectory) all reflect the commitment. The honest caveats are preserved but subordinated: minimal v2 = AUTH+TLS (M12) remains the fallback if scope tightens, and v3 (Raft, blocked on `ToyRaft`) is still the real downstream dependency.
- **Per-milestone dependency + ADR ownership added.** Each of M10–M16 now names what it depends on and which ADR it owns (RESP3 negotiation → M10, tagged-union store + AOF v3 → M11, AUTH/TLS → M12, protected-mode + atomic keyspace ops → M15), so ADRs are written after their owning milestone merges rather than batched at release (M16).
- **New M15 "Hardening" milestone; release renumbered M15 → M16.** Added to *earn* the `2.0.0` tag rather than default to it. Everything in M10–M14 is additive (opt-in RESP3, backward-compatible AOF v3), so by semver alone the arc is a `1.x`. M15 introduces the one deliberate breaking change — **protected mode** (refuse a non-loopback bind without auth) — and promotes **atomic `RENAME`/`RENAMENX`/`COPY`** out of the v2.x backlog to close a real correctness gap (racy today). One breaking change + one correctness win, not feature-count padding. The v2 ADR set grows from four to five, the cut criteria add protected mode + atomic renames, and M16 gains an explicit release-hardening gate (flaky-test fix, `.golangci.yml` schema fix, security review of the auth/TLS/protected-mode surface, v1→v2 AOF upgrade test).
