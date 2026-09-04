# toykv — Roadmap

Milestone-ordered execution plan to v1.0.0. Each milestone ends at a tagged, demoable state. Branch off `main`; merge via PR; **no direct commits to `main`**.

```
v1  M0 Skeleton ─► M1 RESP echo ─► M2 Store core ─► M3 AOF + crash ─► M4 TTL ─►
    M5 Compaction ─► M6 CLI ─► M7 TUI ─► M8 Integration tests ─► M9 Bench + polish ─► v1.0.0

v2  M10 RESP3 ─► M11 Types (lists+hashes, AOF v3) ─► M12 AUTH/TLS ─►
    M13 INFO + SCAN ─► M14 TUI v2 ─► M15 Hardening (protected-mode + atomic ops) ─►
    M16 Observability (OpenTelemetry → LGTM) ─► M17 Bench + polish ─► v2.0.0   ✅ shipped 2026-07-22

v3  M18 Raft embed ─► M19 Cluster + election ─► M20 Client routing ─►
    M21 WAIT + INFO repl ─► M22 TUI v3 (cluster) ─► M23 Bench + dogfood + polish ─► v3.0.0   (committed — ToyRaft dogfood gate)
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

> **Earning the `2.0.0` tag (decided 2026-07-13).** Everything in M10–M14 and M16's observability is *additive* — RESP3 is opt-in, AOF v3 replays old formats, OpenTelemetry is off unless an endpoint is configured — so by semver alone this work is a `1.x`, not a forced major. `2.0.0` is *earned*, not defaulted, by **M15's protected-mode change**: v1 served any bind with no auth; v2 refuses a non-loopback bind without auth unless explicitly overridden. That single deliberate break to the deployment contract is what makes the major honest. M15 also promotes **atomic `RENAME`/`RENAMENX`/`COPY`** out of the backlog — closing a real correctness gap (racy today via `GET`+`SET`+`DEL`) — so the release ships one breaking change and one correctness win, not a pile of additions hoping volume justifies the number.

> **Decision recorded 2026-07-13.** v1.0.0 shipped 2026-06-17; after ~4 weeks of real use the trajectory decision (previously deferred) is now made: **run the full M10–M17 arc — Option B (v1 → v2)**. See [Honest framing](#honest-framing--pick-one-trajectory). *(Arc extended 2026-07-16: an observability milestone (M16) was inserted before the release; the release renumbered M16 → M17.)*
>
> The honest caveats still stand, subordinate to that commitment: the backlog tracker (`project-todo/projects/toykv.md`) records that a *minimal* v2 is **AUTH + TLS only**, and that **v3 (Raft-distributed) is the real downstream dependency** — blocked on `ToyRaft` shipping as a vendorable library. So if scope tightens mid-cycle, v2 can be trimmed back to M12 (AUTH+TLS) without abandoning the release; and jumping to v3 stays a live option the moment `ToyRaft` is ready.

### Why this order — bottom-up + risk-first (continued)

Numbering continues the single v1 sequence (**M10–M17**); the release tag is `v2.0.0`.

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
| Instrumentation overhead + durability impact — spans/metrics must not slow the hot path or perturb the AOF ordering | Medium | M16 — off-by-default + no-regression / durability-unaffected test |
| Telemetry export failure must never fail a command (exporter down ≠ client error) | Medium | M16 — same |
| `INFO` field correctness | Low | M13 |

Four ordering decisions deserve calling out:

1. **RESP3 (M10) before types (M11).** RESP3 is the wire foundation — additive, low store-blast-radius, opt-in via `HELLO 3`. Building the protocol-negotiation and type-tag plumbing first means types then exercise RESP3's map/set replies (`HGETALL` → map) as a real second use case — the same logic that put AOF before TTL in v1.
2. **Types (M11) before AUTH (M12).** Types are the highest-blast-radius surface in v2 (store model + AOF format bump), so they ship and get crash-tested early. AUTH/TLS is self-contained connection-layer work that doesn't depend on the type system and can slot in once the risky format change is proven.
3. **Hardening (M15) after AUTH (M12).** Protected mode gates on `requirepass` existing, so it can only land once M12 has shipped auth. It sits at M15 — after the feature milestones — because it is the *deliberate breaking change that earns the major*, and it reads most honestly as the last gate before the release milestone rather than buried inside M12. If v2 ever trims to the minimal AUTH+TLS cut, protected mode should be pulled forward into M12 — exposing auth without a safe default is the exact risk the minimal cut would otherwise ship.
4. **Observability (M16) last, just before the release.** OpenTelemetry instruments the *whole* command / connection / persistence surface, so it lands after every surface it measures exists — instrumenting a moving target (M11 types, M12 auth spans, M15 rename ops) before those commands are final would mean re-instrumenting. It is fully additive (off unless an endpoint is configured), so it does **not** move the semver needle — M15 still earns the major. It sits before the M17 release rather than in the v2.x backlog because "deployable, safe-by-default, **observable** single node" is the stated v2 goal, and shipping the release without the three signals would leave the "observable" adjective unbacked.

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
**Branch:** `feat/info-scan` · **Depends on:** M11 (`SCAN` iterates the typed keyspace; `TYPE` per key) + M10 (`INFO` served over the RESP3-aware writer) · **ADR:** [0014](./adr/0014-scan-cursor-and-info-wire-format.md) — SCAN insertion-sequence cursor + INFO wire format (the earlier "none new" projection was revised: the seq-cursor is a real store-model contract, and the INFO wire-form correction pairs with it)
- `INFO` — uptime, `dbsize`, `appendfsync` policy, AOF byte size, replay stats; served as the Redis-faithful `# Section\nkey:value` text — a **verbatim string** (`=`) on RESP3, a **bulk string** on RESP2. (Corrected 2026-07-16: the earlier draft said "RESP3 map." Real Redis never maps `INFO`, and a map breaks `go-redis .Info()` / `redis-cli info` parsing — the byte-compat the project has valued since M8.)
- `SCAN cursor [MATCH pattern] [COUNT n]` — cursor-based iteration over the (now typed) keyspace; replaces `KEYS *` for large keyspaces.
- **Owned risk test:** cursor-guarantee stress — a full `SCAN` loop under concurrent writes returns every key that was present for the entire scan (Redis's SCAN guarantee), with no crash on a stale cursor.
- **Exit:** `INFO` fields match live server state; a `SCAN` loop enumerates the full keyspace; integration tests introspect server state via `INFO`.

### M14 — TUI v2
**Branch:** `feat/tui-v2` · **Depends on:** M11 (`TYPE` views), M12 (AUTH prompt), M13 (`SCAN` paging + `INFO` status bar) · **ADR:** [0015](./adr/0015-tui-v2-scan-paging-and-tls-deferral.md) — TUI v2 SCAN-paging boundary + TLS-client deferral (the "none new" projection was revised: M14 mostly consumes M11–M13, but the paging carries no consistency contract of its own and TLS-client dialing is a deliberate deferral — both real boundaries, same call as M13's ADR-0014)
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

### M16 — Observability: OpenTelemetry (logs, metrics, traces) → LGTM
**Branch:** `feat/observability` · **Depends on:** M10–M15 (instruments the full command / connection / persistence surface, so it lands once every command is final) · **ADR:** OpenTelemetry signal model + OTLP export + LGTM backend
*(Fully additive — off unless an OTLP endpoint is configured — so it does not touch the semver story; M15 still earns the major. This is the milestone that makes the v2 goal-word "observable" actually true.)*
> **Complete signal inventory:** [`docs/M16-observability.md`](./M16-observability.md) — every metric, log event, and trace span, each anchored to its emitting call site. The bullets below are the summary; the inventory is the implementation checklist and the coverage-check table.
- **The three signals via the OpenTelemetry SDK.** One `go.opentelemetry.io/otel` wiring — `TracerProvider` / `MeterProvider` / `LoggerProvider` — exporting **OTLP** (gRPC + HTTP) to an OpenTelemetry Collector that fans out to the Grafana **LGTM** stack: **L**oki (logs), **G**rafana (dashboards), **T**empo (traces), **M**imir (metrics). Off by default: no `-otel-endpoint` ⇒ no-op providers and zero hot-path cost.
- **Logs → Loki.** An `slog.Handler` that also emits OTLP log records, so structured logs carry the active trace/span ID for correlation. Console `slog` stays the default when OTel is off; log shape is unchanged.
- **Metrics → Mimir (RED per command).** Rate / errors / duration: a command-latency histogram and a call/error counter labelled by command, plus gauges for `connected_clients`, `dbsize`, AOF byte size, and `aof_rewrite_in_progress` — the same sources `INFO` (M13) already reads, now exported too. This **folds the v2.x-backlog "Prometheus `/metrics`" item** into OTLP (a Prometheus scrape exporter stays an option).
- **Traces → Tempo.** A span per command dispatch (attributes: command, arity, proto, authenticated, reply kind), a parent span per connection lifecycle, and child spans for the store op and the AOF append/fsync — so a slow `everysec`/`always` fsync shows up as span latency. Context threads through `dispatch` alongside the existing `connState`.
- **Config + local stack.** `-otel-endpoint`, `-otel-protocol` (grpc|http), `-otel-service-name`, and a sampling flag; a `deploy/` compose (or the single `grafana/otel-lgtm` image) for local viewing. Exporter failures are logged and dropped — **telemetry never fails a command**.
- **Owned risk test:** (1) **no-op when disabled** — with no endpoint the providers are no-ops and a benchmark shows no measurable regression vs the pre-M16 binary; (2) **durability unaffected** — the M3/M11 crash-durability suite passes unchanged with instrumentation compiled in (span wrapping must not reorder mutate → append → fsync → reply); (3) **signal correctness** — against an in-memory/stdout exporter, one command yields exactly one dispatch span with the expected attributes, an error reply increments the error counter, and a log record carries the active trace ID; (4) **exporter-down resilience** — a dead OTLP endpoint never turns a successful command into a client error.
- **Exit:** with `-otel-endpoint` set, traces/metrics/logs land in a local LGTM stack and correlate by trace ID in Grafana; with it unset, behaviour and benchmarks match the pre-M16 binary; the durability suite is green with instrumentation in place.

### M17 — Bench + polish + v2.0.0
**Branch:** `feat/release-v2` · **Depends on:** M10–M16 all merged
- Re-run `make bench` with typed workloads; README records the new numbers.
- README + [SECURITY](./SECURITY.md) update — auth/TLS + protected mode lift the localhost-only ceiling; document the new "deployable, safe-by-default, **observable**" posture, the protected-mode override, and the OpenTelemetry/LGTM setup.
- PRD / HLD / LLD deltas for types, RESP3, auth, protected mode, and observability.
- ADR reconciliation: the **eight v2 ADR topics** land in **seven files** — `0012` bundles the tagged-union store model *and* the AOF v3 format, so the topic list (RESP3 negotiation, tagged-union store model, AOF v3 format, AUTH/TLS, SCAN cursor + INFO wire format, TUI v2 SCAN-paging boundary + TLS-client deferral, protected-mode default + atomic keyspace ops, OpenTelemetry signal model + OTLP export) maps to ADRs `0011`–`0017`. Each is written **after** its owning milestone merges (M10/M11/M12/M13/M14/M15/M16) per [`docs/adr/README.md`](./adr/README.md); M17 only verifies all seven files have landed and the index budget note is current — it does not batch-write them at release time. (M17 also *amends* ADR-0017 to record the release-gate observability parity fix — see below.) (Two were added mid-cycle against a "no new ADR" projection: M13's ADR-0014 — the seq-cursor is a real store-model contract — and M14's ADR-0015 — the paging carries no consistency contract of its own and TLS-client dialing is a deliberate deferral.)
- **Release-hardening gate (must all pass before the tag):**
  - ✅ Flaky `TestAOF_CrashInjection_DuringRewrite/late-kill` — verified **not** flaky (`-race -count=3`, no `-short`); the whole test only `t.Skip`s under `-short` (subprocess fork), which is intentional, not a quarantine.
  - ✅ `.golangci.yml` — already carries `version: "2"` and runs in `make lint` + CI (closed before M17).
  - ✅ **Security review** of the M12 auth/TLS + M15 protected-mode surface ([`docs/SECURITY-REVIEW-v2.md`](./SECURITY-REVIEW-v2.md)): that surface is clean, but the review surfaced **two BLOCKING pre-auth DoS vectors in the RESP codec** (unbounded array count + unbounded nesting depth) — both **fixed** (`MaxArrayLen`/`MaxDepth`) with regression tests before the tag.
  - ✅ **Explicit v1→v2 AOF upgrade test** — real v1/v2 files replay in-place under the v3 reader (`internal/aof/upgrade_test.go`).
  - ✅ **Observability no-regression check** — the parity A/B against the pre-M16 binary caught a real **~20% disabled-path regression**; **fixed** by memoizing per-command instrument attributes (ADR-0017 M17 amendment) with **no** hot-path guard, restoring SET to parity / GET within ~7%; crash-durability suite green with instrumentation compiled in.
  - Confirm the `2.0.0` call is earned (protected mode is the breaking change) and record it in the release notes.
- Goreleaser reused from v1; tag `v2.0.0`.

### v2.x backlog (not in committed scope)

Deferred from the committed M10–M17 cut so v2 stays a focused "usable single-node" release, not a Redis re-implementation:

| Theme | Item | Note |
|---|---|---|
| Observability | Native Prometheus `/metrics` scrape endpoint behind `-metrics-addr` | RED metrics now ship via M16's OpenTelemetry → Mimir (OTLP push); a pull-based Prometheus scrape endpoint remains optional/deferred |
| Persistence | RDB snapshots alongside AOF (opt-in, `-rdb-interval`) | Faster cold starts on large datasets |
| Reliability | `-aof-truncate` flag to repair partial tails | Operationally important once auth lifts the deployment ceiling |
| Content | Hashnode post: *"Three persistence policies, one append-only file"* | Owed since v1 — write after v2 ships, not before |
| Integration | `prajwal-resilience-kit` Redis-adapter test target | First external consumer; validates AUTH + commands |

**Breaking risk:** two, both deliberate and documented. (1) AOF format bump to **v3** to encode list/hash records → version-gated, replays v1, v2, and v3 records (M11). (2) **Protected mode** (M15) refuses a non-loopback bind without auth — a behavioural break to v1's deployment contract, overridable via `-protected-mode no`. This is the change that earns the `2.0.0` major; RESP3 is wire-only and additive and never forces it. **Observability (M16) adds no breaking risk** — it is off unless an OTLP endpoint is configured.

**Cut criteria:** RESP3 + lists + hashes + AUTH + TLS + `INFO` + `SCAN` + **protected mode** + **atomic `RENAME`/`RENAMENX`/`COPY`** + **OpenTelemetry (logs/metrics/traces)** shipped, each feature area with a corresponding ADR, and the M17 release-hardening gate (flaky-test fix, lint config, security review, v1→v2 AOF upgrade test, observability no-regression check) all green.

## v3.0 — Distributed (the ToyRaft payoff · committed)

This is where toykv earns the original "Raft state-machine demo" framing. It embeds [`github.com/prajwalmahajan101/toyraft`](https://github.com/prajwalmahajan101/toyraft) (`v1.0.0-rc.1`) as a vendored consensus library. Standalone mode stays byte-identical to v2; `-replicate` opts into a Raft-backed cluster. Same execution discipline as v1/v2 — branch off `main`, merge via PR, **no direct commits to `main`** — and the same governing principle: **the highest-blast-radius surface ships and is crash/linearizability-tested earliest, and each milestone owns its own risk test.**

> **Goal:** a replicated, leader-based, **single-writer** toykv cluster. v3.0 is **replication only** — sets, sorted sets, pub/sub, and keyspace notifications are deferred to the [v3.x backlog](#v3x-backlog-not-in-committed-v30-scope). The narrowing keeps the release focused on the actual ToyRaft payoff.

> **Mutual unblock (the dogfood gate).** ToyRaft is at `v1.0.0-rc.1` with a **frozen** public API (`pkg/raft.Node`, `StateMachine`, `Storage`, `Transport`); its own roadmap gates the `v1.0.0` tag on a real consumer embedding it. toykv's running cluster **is** that gate. So M18–M23 and ToyRaft `v1.0.0` unlock each other — and M23 ships a **migration / dogfooding report** (`docs/TOYRAFT-MIGRATION-REPORT.md`, written incrementally across M18–M22) back to ToyRaft (bugs, API friction, docs gaps, feature requests) as first-class output, not an afterthought.

> **Decisions locked (2026-09-04).**
> 1. **Scope — replication only.** Types/pub-sub/events → v3.x.
> 2. **Reads — leader by default; `READONLY` opt-in for stale replica reads.** No linearizable-read guarantee (ToyRaft `v1` has no ReadIndex — documented).
> 3. **Write routing — client-driven redirect.** A follower returns a leader-hint error; the client/CLI/TUI retry against the leader.
> 4. **Compaction — ship on `rc.1` with an unbounded Raft log.** `StateMachine.Snapshot/Restore` are *structured* now (store serialization built + tested) but stubbed `ErrSnapshotUnsupported` per the `v1` contract, so real compaction lands free when ToyRaft `v2` ships snapshots. Deferred to v3.x.

### The integration seam

toykv today mutates at a single `dispatch()` chokepoint: **mutate store → append AOF → reply**. Replicated, a mutating command becomes **`raft.Propose(envelope)` → ToyRaft replicates → `StateMachine.Apply` mutates store + appends AOF → reply**. AOF becomes each node's *local* applied-state durability; the **Raft log is the replication source of truth**. The existing `replayApply` pattern (dispatch reused for AOF replay) means the seam is small and single-point.

### Why this order — bottom-up + risk-first (continued)

Numbering continues the single sequence (**M18–M23**); the release tag is `v3.0.0`.

| Risk | Severity | Owned by |
|---|---|---|
| `StateMachine.Apply` determinism / apply-exactly-once (silent replica divergence) | **Critical** | M18 — determinism + apply-once test |
| mutate→propose→apply→AOF ordering & crash durability through the new path | **Critical** | M18 — durability suite with the SM in the loop |
| Acked-write loss across leader failover / partition heal | **Critical** | M19 — kill-leader + partition linearizability test |
| Split-brain / double-leader | High | M19 — same |
| A write reaching a follower silently dropped | High | M20 — redirect test |
| Stale/torn reads violating the chosen read model | Medium | M20 — read-model test |
| `WAIT` over-reporting acks (a durability lie) | Medium | M21 — ack-truth test vs a slow/partitioned follower |
| Raft peer transport is unauthenticated (ToyRaft threat model: trusted network only) | High (documented) | M23 — security note + bind guard |

### M18 — Raft embedding + single-node replicated path *(the state-machine seam)*
**Branch:** `feat/raft-embed` · **Depends on:** nothing new · **ADR:** 0018 — Raft embedding, command envelope & StateMachine seam
*(The foundational, highest-blast-radius milestone — it owns the determinism + durability tests.)*
- Add the `toyraft v1.0.0-rc.1` dependency.
- Deterministic **command envelope** (encode `argv` → `[]byte`, version byte) — only *mutating* commands are replicated.
- **Command classification table:** mutating (→ `Propose`), read (local/leader), local-admin never replicated (`HELLO`, `AUTH`, `PING`, `INFO`, `BGREWRITEAOF`).
- Implement the toykv `raft.StateMachine`: `Apply` decodes the envelope → applies to the store → appends AOF (reuses today's handler logic). `Snapshot`/`Restore` return `ErrSnapshotUnsupported` per the `v1` contract, but the store-serialization they will call is built and unit-tested now (forward-compat for ToyRaft `v2`).
- Run a **single-node cluster** (`Peers=[self]`, self trivially leader): every mutating command flows `Propose → Apply`. Standalone (non-`-replicate`) path untouched.
- **Owned risk test:** (1) determinism — the same command stream applied twice yields byte-identical store state; (2) apply-once & ordering — single-node propose→apply round-trips every mutating command in strict index order; (3) crash durability preserved through the new path (the M3/M11 suite green with the SM in the loop).
- **Exit:** single-node `-replicate` serves every mutating command via `Propose→Apply`; store state matches standalone; durability suite green.

### M19 — Multi-node replication + leader election *(the distributed core)*
**Branch:** `feat/cluster` · **Depends on:** M18 · **ADR:** 0019 — cluster mode, transport & raft-log storage layout
- `-replicate -peers <id@host:raftport,…> -raft-addr -raft-dir`. Wire ToyRaft `pkg/transport/http` (peer port distinct from the client port) + `pkg/storage/file` for the Raft log.
- N-node cluster (odd N ≥ 3); role tracking; `LeaderHint()` surfaced. Standalone mode unchanged.
- **Owned risk test:** 3-node cluster — a leader-acked write appears on all followers; **kill the leader → new leader elected → no acked write lost**; partition heal reconciles divergent tails. Plus a **linearizability harness** on `SET`/`GET`/`INCR` (ToyRaft's Porcupine harness or a go-redis-driven recorder), run under `-race`.
- **Exit:** 3-node cluster replicates all writes; survives leader kill + partition with zero acked-write loss; linearizability harness green.

### M20 — Client routing: write redirect + read model
**Branch:** `feat/cluster-routing` · **Depends on:** M19 · **ADR:** 0020 — write redirection & cluster read consistency
- Follower write → leader-hint redirect error (`-MOVED`-style / `-ERR NOTLEADER host:port`). `internal/client` (CLI **and** TUI) auto-retry against the hint with bounded backoff.
- Reads: leader by default (follower reads redirect); **`READONLY`** opt-in lets a follower serve stale local reads (documented non-linearizable).
- **Owned risk test:** a client hitting a random node completes every write via redirect and reads consistently from the leader; `READONLY` reads return follower-local state; redirect storms during an election converge under bounded retry.
- **Exit:** any-node client completes all writes; leader/replica read model behaves as specified; CLI/TUI follow redirects transparently.

### M21 — `WAIT` + INFO replication + cluster observability
**Branch:** `feat/wait-info-repl` · **Depends on:** M19 (reads `Status().MatchIndex`) · **ADR:** 0021 — replication acknowledgement & telemetry model
- `WAIT numreplicas timeout` — block until N replicas ack the write's index (driven by leader `MatchIndex`); truthful, never over-reports.
- `INFO replication` section — role, leader addr, connected replicas, per-replica lag, commit/apply/log offsets.
- OTel (M16 surface) extended: spans for propose→commit→apply, replication-lag & role gauges → the existing LGTM stack.
- **Owned risk test:** `WAIT N t` returns only once ≥N replicas truly hold the index (verified against a partitioned/slow follower it does **not** over-count); `INFO replication` fields match live cluster state.
- **Exit:** `WAIT` matches Redis semantics; `INFO replication` accurate; raft signals visible in Grafana.

### M22 — TUI v3: cluster view
**Branch:** `feat/tui-v3` · **Depends on:** M20, M21 · **ADR:** *(none expected — consumes M20/M21; revisit only if a real contract emerges, per the M13/M14 precedent)*
- Cluster pane: replicas, current leader, per-node role, lag, log offset (fed by `INFO replication`); AUTH + redirect-aware connect.
- **Owned risk test:** `teatest` cluster-view smoke against a running 3-node cluster; a leader change is reflected in the view.
- **Exit:** the TUI renders live cluster topology and follows leadership changes; all v2 keybindings still pass.

### M23 — Bench + dogfood report + polish + v3.0.0
**Branch:** `feat/release-v3` · **Depends on:** M18–M22 all merged
- Re-run `make bench` in cluster mode (replication cost vs standalone); README records the numbers.
- **Migration / dogfooding report (`docs/TOYRAFT-MIGRATION-REPORT.md`) — a release deliverable.** The report is **authored incrementally as the integration happens** — each of M18–M22 appends its findings (confirmed bugs with repros, API friction, docs gaps, feature requests) as they surface against running code — and is finalized here at M23, then delivered to ToyRaft as its `v1.0.0` dogfood-gate feedback. This is the reciprocal half of the mutual unblock. *(Not pre-written: findings are recorded only once observed in integration.)*
- **Security note & bind guard:** the ToyRaft peer transport is unauthenticated/plaintext (ToyRaft threat model = trusted network). Document it; extend protected-mode to refuse an untrusted-network raft bind without an explicit override. Update [`docs/SECURITY.md`](./SECURITY.md).
- Docs: PRD/HLD/LLD deltas for replication; README cluster quickstart; a `deploy/` compose for a local 3-node cluster.
- ADR reconciliation: 0018–0021 land after their owning milestones (M18/M19/M20/M21) per [`docs/adr/README.md`](./adr/README.md); M23 verifies all four files exist and the index note is current.
- **Coordinate ToyRaft `v1.0.0`:** bump the dependency `rc.1 → v1.0.0` once ToyRaft tags it off this integration.
- **Release-hardening gate (all must pass before the tag):** linearizability harness green on `-race`; leader-kill / partition-heal suite green; standalone-mode benchmarks unchanged vs v2; crash-durability suite green with the SM in the loop; migration report delivered.
- Goreleaser reused from v1/v2; tag `v3.0.0`.

### v3.x backlog (not in committed v3.0 scope)

Deferred from the committed M18–M23 cut so v3.0 stays a focused "replicated single-writer cluster," not a distributed-Redis re-implementation. These ride the v3 architecture and unblock during the v3 line as small upstream features land.

| Theme | Item | Blocker / note |
|---|---|---|
| Compaction | Real Raft-log compaction via `StateMachine.Snapshot/Restore` | Unblocks on **ToyRaft `v2`** snapshots; the wiring is already structured in M18 (store serialization built) |
| Reads | Linearizable reads (ReadIndex / lease reads) | Unblocks on **ToyRaft** ReadIndex; until then `READONLY` reads are explicitly stale |
| Membership | Dynamic add/remove node without a full-cluster restart | Unblocks on **ToyRaft** joint-consensus config changes |
| Types | **Sets**: `SADD`, `SREM`, `SMEMBERS`, `SISMEMBER`, `SINTER`, `SUNION`, `SDIFF` | Ride the replicated `Apply` path; AOF may bump to v4 |
| Types | **Sorted sets**: `ZADD`, `ZRANGE`, `ZRANGEBYSCORE`, `ZRANK`, `ZSCORE` | Same |
| Pub/Sub | `SUBSCRIBE`, `UNSUBSCRIBE`, `PUBLISH`, `PSUBSCRIBE` on the RESP3 push frames (v2 M10) | Cluster-wide fan-out is its own design question |
| Events | `__keyspace@0__:k` notifications for `expired`/`del`/`set` | Depends on pub/sub |
| Wire | `CLUSTER NODES` (minimal — **not** Redis Cluster's slot model) | Convenience introspection once topology is dynamic |

**Breaking risk in v3.0:** minimal. Replication is opt-in (`-replicate`); standalone mode is byte-identical to v2. The only behavioural addition to the deployment contract is the M23 raft-bind guard, overridable. AOF format is untouched by v3.0 (typed-record bumps only arrive with the deferred set/sorted-set work).

**Out of scope even at v3:** Redis Cluster slot/sharding model, Sentinel, Lua / `MULTI` / `EXEC`. (Spec rejects these explicitly.)

**Cut criteria:** 3-node cluster passes the linearizability harness on `SET`/`GET`/`INCR` under `-race`; leader-kill + partition-heal lose no acked write; `WAIT` / `INFO replication` / redirect / `READONLY` all shipped with ADRs; standalone benchmarks unchanged; the migration report is delivered and **ToyRaft is tagged `v1.0.0`**.

## v4.0 — deferred (not committed · gated on the mission growing past "learning artifact")

v4 is **deliberately undefined**. The source spec is emphatic that the danger is scope creep — *"that's how you end up half-building Redis instead of finishing tinykv."* v3.0 delivers the original "Raft state-machine demo" thesis; anything past a replicated single-writer node is a **new mission**, not a continuation. This section collects the genuinely-major deferrals so they are tracked, not forgotten — each with the blocker that would have to clear first. **Nothing here is committed**, and several items stay rejected unless toykv's purpose changes from a learning artifact to a system meant to be run at scale.

| Theme | Item | Gate / blocker |
|---|---|---|
| **Horizontal scale-out** | Multi-Raft-group sharding — key-space partitioned across independent Raft groups (a request routes to the group that owns the key) | Only if the mission changes to real horizontal scale. The single-group **Redis Cluster slot model stays rejected** — this would be a deliberately different, simpler design |
| **Elastic membership** | Online cluster resize (grow/shrink) as an operator workflow, not a restart | ToyRaft joint-consensus membership (also a v3.x item at the command level; v4 is the *operational* story around it) |
| **Read scaling** | Learner / non-voting read-replica nodes; cross-region followers | ToyRaft learner-role support + ReadIndex |
| **Peer security** | mTLS + auth on the Raft peer transport (lift the trusted-network-only ceiling) | ToyRaft transport security (currently trusted-network-only by design) |
| **Backup / DR** | Point-in-time snapshot export + restore-to-new-cluster; log shipping to object storage | ToyRaft `v2` snapshots (v3.x compaction first) |
| **Client ecosystem** | A first-class toykv Go client library (beyond `internal/client`); optional other-language SDKs | Only worthwhile once external consumers exist |
| **Transactions** | `MULTI` / `EXEC` / `WATCH` | **Spec-rejected** — listed for completeness only; revisit only on an explicit mission change |
| **Scripting** | Lua / server-side scripting | **Spec-rejected** — same |
| **Failover ops** | Graceful leadership transfer for planned maintenance; rolling-restart orchestration | ToyRaft leadership-transfer (post-`v1`) |

**Honest note:** the realistic terminal state for toykv is **v3.0** — a working, replicated, observable, safe-by-default single-writer KV that proves the ToyRaft integration. v4 exists in this document so that if the project's purpose ever changes, the deferrals and their upstream gates are already mapped. Treat "ship v3.0, stop" as the default, exactly as "ship v1, stop" was for the v1 line.

## Honest framing — pick one trajectory

The source spec is emphatic about scope creep: *"that's how you end up half-building Redis instead of finishing tinykv."* Three honest paths were on the table — recorded here so the reasoning behind the choice stays legible:

| Option | Trajectory | When this is right |
|---|---|---|
| **A — ship v1, stop** | v2 and v3 stay aspirational; tracked here as backlog only | Spec-faithful. Project ships as the long-weekend artefact it was meant to be |
| **B — v1 → v2** | Run the M10–M17 arc (RESP3, types, AUTH/TLS, INFO/SCAN, TUI v2, hardening, observability); stop at "complete usable single-node KV" | Realistic if v1 sees real (personal/test) usage and the gaps annoy. Minimal viable v2 is AUTH+TLS + protected mode (M12 + the M15 protected-mode bullet) — the rest is optional even within v2 |
| **C — v1 → v3 (skip or trim v2)** | Jump to the Raft-distributed payoff — the actual downstream dependency | Only once `ToyRaft` ships as a vendorable library. v3 is blocked on `ToyRaft` v1.0-rc1; v2 can be skipped or trimmed to AUTH+TLS if scope is tight, since downstream work needs v3 (multi-node), not v2 |

**Decided 2026-07-13: Option B** — v1.0.0 has seen ~4 weeks of real use since the 2026-06-17 tag, and the gaps that matter (no auth, string-only values, `KEYS *`-only iteration) are worth closing. The full M10–M17 arc is now the committed active plan. The asymmetry the tracker records still holds: **v2 is polish; v3 is the real downstream dependency** — so Option C ("trim v2 to AUTH+TLS, jump to v3") stays live the moment `ToyRaft` ships as a vendorable library, and v2 can fall back to M12-only if scope tightens without abandoning the release.

**Updated 2026-09-04: Option C is now active — v2 → v3.** v2.0.0 shipped 2026-07-22, and ToyRaft has reached `v1.0.0-rc.1` with a frozen public API — the exact precondition Option C named. The committed [v3.0 arc (M18–M23)](#v30--distributed-the-toyraft-payoff--committed) is now the active plan. The trajectory is the full A → B → C sequence, not a jump: v1 shipped, v2 shipped, v3 is the real downstream payoff that motivated the whole project. v3.0 is scoped to **replication only** (the ToyRaft thesis); everything else is v3.x/v4 deferral.

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
| M13 | INFO + SCAN | ✅ | [#31](https://github.com/prajwalmahajan101/toykv/pull/31) | `m13` |
| M14 | TUI v2 | ✅ | [#32](https://github.com/prajwalmahajan101/toykv/pull/32) | `m14` |
| M15 | Hardening: protected mode + atomic keyspace ops | ✅ | [#33](https://github.com/prajwalmahajan101/toykv/pull/33) | `m15` |
| M16 | Observability: OpenTelemetry (logs/metrics/traces) → LGTM | ✅ | [#35](https://github.com/prajwalmahajan101/toykv/pull/35) | `m16` |
| M17 | Bench + polish + v2.0.0 | ✅ | [#36](https://github.com/prajwalmahajan101/toykv/pull/36) | `v2.0.0` |
| M18 | Raft embedding + single-node replicated path | 📋 Planned | — | `m18` |
| M19 | Multi-node replication + leader election | 📋 Planned | — | `m19` |
| M20 | Client routing: write redirect + read model | 📋 Planned | — | `m20` |
| M21 | `WAIT` + INFO replication + cluster observability | 📋 Planned | — | `m21` |
| M22 | TUI v3: cluster view | 📋 Planned | — | `m22` |
| M23 | Bench + dogfood report + polish + v3.0.0 | 📋 Planned | — | `v3.0.0` |

## Changes from the previous roadmap

- **M3 ↔ M4 swap:** AOF now lands before TTL (was: TTL before AOF). Rationale: AOF is the highest-risk surface; TTL state needs to persist anyway; building AOF first lets the version-byte design get exercised on a real second use case when TTL adds expiry encoding.
- **Risk tests moved upstream:** each milestone owns its own crash-injection / concurrent-stress test. M3 owns the durability crash test. M4 owns the TTL race test. M5 owns the rewrite-during-writes crash test. M8 becomes pure end-to-end protocol compat instead of the catch-all for everything risky.
- **M2 explicitly owns a concurrent stress test** (was: just unit tests).

**v2 additions (M10–M17):**

- **RESP3 pulled forward v3.0 → v2.0.** RESP3 was originally a v3 wire item; it moves up to M10 as the v2 wire foundation so the type work (M11) exercises its map/set replies as a real second use case. v3 pub/sub then reuses the push frames rather than owning the protocol break.
- **Pub/sub stays v3.** Only the RESP3 transport moves to v2; `SUBSCRIBE`/`PUBLISH` and keyspace notifications remain v3.
- **`TYPE` folded into the types milestone (M11)** rather than the loose wire-completeness list — it's practically required once keys are typed (TUI + `SCAN` depend on it).
- **Single AOF bump this cycle (v3), owned by M11** — mirrors v1's discipline of one focused format change per milestone that needs it.
- **v2 is now committed (decided 2026-07-13).** The earlier draft framed the whole M10–M15 arc as "proposed, optional — not committed." After ~4 weeks of v1.0.0 in real use, the trajectory decision is made: **Option B (v1 → v2), full arc.** The section header, banner, status table, and [Honest framing](#honest-framing--pick-one-trajectory) all reflect the commitment. The honest caveats are preserved but subordinated: minimal v2 = AUTH+TLS (M12) remains the fallback if scope tightens, and v3 (Raft, blocked on `ToyRaft`) is still the real downstream dependency.
- **Per-milestone dependency + ADR ownership added.** Each of M10–M17 now names what it depends on and which ADR it owns (RESP3 negotiation → M10, tagged-union store + AOF v3 → M11, AUTH/TLS → M12, SCAN cursor + INFO wire format → M13, protected-mode + atomic keyspace ops → M15, OpenTelemetry signal model → M16), so ADRs are written after their owning milestone merges rather than batched at release (M17).
- **New M15 "Hardening" milestone; release renumbered M15 → M16.** Added to *earn* the `2.0.0` tag rather than default to it. Everything in M10–M14 is additive (opt-in RESP3, backward-compatible AOF v3), so by semver alone the arc is a `1.x`. M15 introduces the one deliberate breaking change — **protected mode** (refuse a non-loopback bind without auth) — and promotes **atomic `RENAME`/`RENAMENX`/`COPY`** out of the v2.x backlog to close a real correctness gap (racy today). One breaking change + one correctness win, not feature-count padding. The v2 ADR set grows from four to five, the cut criteria add protected mode + atomic renames, and M16 gains an explicit release-hardening gate (flaky-test fix, `.golangci.yml` schema fix, security review of the auth/TLS/protected-mode surface, v1→v2 AOF upgrade test).
- **New M16 "Observability" milestone; release renumbered M16 → M17 (added 2026-07-16).** OpenTelemetry for the three signals — logs → **L**oki, metrics → **M**imir, traces → **T**empo, viewed in **G**rafana (the **LGTM** stack) — exported over OTLP. Fully additive and off unless an endpoint is configured, so it does **not** change the semver story (M15 still earns the major); it exists to make the v2 goal-word "observable" true rather than aspirational. It lands after every command surface is final (so instrumentation isn't chasing a moving target) and before the release. The v2 ADR set grows from six to seven (OpenTelemetry signal model + OTLP export, owned by M16), the cut criteria add the three signals, the release gate adds an OTel-off no-regression + durability-with-instrumentation check, and the v2.x-backlog "Prometheus `/metrics`" item is folded into M16's OTLP metrics (native scrape endpoint stays optional).

**v3 additions (M18–M23, planned 2026-09-04):**

- **v3 scoped to replication only.** The earlier v3 sketch bundled replication *and* sets/sorted-sets *and* pub/sub *and* keyspace events into one table. Split: v3.0 = replication (the ToyRaft thesis); types/pub-sub/events → [v3.x backlog](#v3x-backlog-not-in-committed-v30-scope). Keeps the release a focused proof of the ToyRaft integration rather than another feature pile.
- **`tinyraft` → `toyraft` (`v1.0.0-rc.1`).** The library exists and its public API (`pkg/raft.Node`, `StateMachine`, `Storage`, `Transport`) is frozen. The old roadmap's "only attempt if `tinyraft` is real" precondition is met.
- **Mutual dogfood gate made explicit.** ToyRaft gates its own `v1.0.0` on a real consumer embedding it; toykv's cluster is that consumer. M23 ships a **[migration / dogfooding report](./TOYRAFT-MIGRATION-REPORT.md)** back to ToyRaft as a first-class release deliverable, and bumps the dependency `rc.1 → v1.0.0`.
- **Four architecture decisions locked (2026-09-04):** replication-only scope; leader reads + `READONLY` opt-in stale replica reads (no ReadIndex in ToyRaft `v1`); client-driven write redirect; ship on `rc.1` with an unbounded Raft log (compaction structured now, deferred to v3.x pending ToyRaft `v2` snapshots).
- **Per-milestone dependency + ADR ownership** continues the v2 pattern: Raft-embed/StateMachine seam → M18 (ADR-0018), cluster/transport/storage → M19 (ADR-0019), write-redirect + read model → M20 (ADR-0020), `WAIT` + replication telemetry → M21 (ADR-0021). M22 (TUI v3) consumes M20/M21 with no new ADR expected.
- **New v4.0 deferral section added.** Genuinely-major post-v3 ambitions (multi-Raft-group sharding, elastic membership, learner reads, peer mTLS, backup/DR, client SDKs) are tracked with their upstream gates — explicitly **not committed**, with the spec-rejected items (`MULTI`/`EXEC`, Lua) called out. "Ship v3.0, stop" is the default terminal state.
