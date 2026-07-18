# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Pre-v1.0.0 milestones are tagged `mN` after each milestone PR merges
on `main` (see [`docs/ROADMAP.md`](./docs/ROADMAP.md)).

## [Unreleased]

## [2.0.0] — 2026-07-18

Second release. Closes M10–M17 of the roadmap: a deployable,
**safe-by-default**, **observable** single-node KV. The `2.0.0` major is
*earned*, not defaulted — everything in M10–M14 and M16 is additive (opt-in
RESP3, backward-compatible AOF v3 replay, off-by-default OpenTelemetry), so by
semver alone the arc is a `1.x`. **M15's protected mode** is the one deliberate
break: v1 served any bind with no auth; v2 refuses a non-loopback bind without
`-requirepass` or TLS unless `-protected-mode no` is passed. That single change
to the deployment contract is what makes the major honest.

### Added

- Atomic keyspace commands (M15, tag `m15`): `RENAME`, `RENAMENX`, and
  `COPY source dest [DB 0] [REPLACE]` — single store-mutex-guarded moves
  that replace the racy client-side `GET`+`SET`+`DEL`. TTL and value type
  (string/list/hash) travel with the key; the destination gets a fresh
  SCAN sequence. `RENAME`/`RENAMENX` return `-ERR no such key` on a
  missing source; `COPY` deep-copies list/hash payloads and accepts only
  DB index 0 (single-DB). Recorded verbatim in the AOF — **no format
  bump**, replayed deterministically under the v3 reader. See
  [ADR-0016](./docs/adr/0016-protected-mode-and-atomic-keyspace-ops.md).
- Authentication (M12, tag `m12`): `-requirepass <pass>` server flag and
  the `AUTH [username] password` command (single-user model; the only
  valid username is `default`, matching Redis). `HELLO 3 AUTH default
  <pass>` performs real verification — the protocol switch commits only
  after a successful AUTH. Password comparison is constant-time
  (`crypto/subtle`); error strings are Redis 7 byte-exact (`-WRONGPASS`,
  `-NOAUTH`, the "no password is set" hint included) — see
  [ADR-0013](./docs/adr/0013-auth-model-and-tls-termination.md).
- Command gating (M12): with `requirepass` set, an unauthenticated
  connection may run only `AUTH`, `HELLO`, and `PING`; everything else —
  including unknown commands — returns `-NOAUTH Authentication required.`
  before the dispatch table is consulted. Unauthenticated `PING` is a
  deliberate, documented deviation from Redis (ROADMAP §M12).
- TLS transport (M12): `-tls-cert` / `-tls-key` wrap the listener via
  stdlib `crypto/tls` (min TLS 1.2). The flags must be given as a pair;
  the server exits non-zero with a clear error otherwise. Composes with
  `-requirepass` for the deployable posture; `redis-cli --tls --cacert`
  round-trips.
- Value types: lists and hashes (M11, tag `m11`). The store entry is now
  a tagged union (string / list / hash); lists ride a growable
  ring-buffer deque (O(1) pushes at both ends).
- List commands: `LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LLEN`, `LRANGE`,
  `LINDEX`. Hash commands: `HSET`, `HGET`, `HDEL`, `HEXISTS`, `HKEYS`,
  `HVALS`, `HLEN`, `HGETALL` — `HGETALL` replies with a native RESP3
  map (`%`) to `HELLO 3` clients and a flat array on RESP2.
- `TYPE key` (`string` | `list` | `hash` | `none`) and Redis-exact
  `-WRONGTYPE` errors on cross-type operations. A list/hash emptied by
  its last pop/delete removes the key (Redis parity).
- AOF format v3: typed mutating records; replay accepts v1, v2, and v3
  files. Opening an older file upgrades its header version byte in place
  before any append, so pre-M11 binaries fail fast with a version error
  instead of dying mid-replay. `BGREWRITEAOF` snapshots each typed key as
  one canonical `RPUSH`/`HSET` record (+ `PEXPIREAT` when a TTL is set).
- RESP3 wire upgrade, opt-in via `HELLO [protover [AUTH user pass]]`;
  per-connection protocol state defaults to RESP2 (M10, tag `m10`).
- RESP3 encoder set in `internal/resp`: `%` map, `~` set, `,` double,
  `#` boolean, `_` null, `=` verbatim, `>` push. A protocol-aware writer
  emits native RESP3 frames to a `HELLO 3` client and downgrades each to
  its RESP2 equivalent at a single point, so RESP2 clients (incl.
  `redis-cli` default) are unaffected — see
  [ADR-0011](./docs/adr/0011-resp3-negotiation-and-protocol-state.md).
- `INFO` and `SCAN` (M13, tag `m13`). `INFO` reports uptime, `dbsize`,
  `appendfsync` policy, AOF byte size, and replay stats as Redis-faithful
  `# Section\nkey:value` text — a verbatim string (`=`) on RESP3, a bulk
  string on RESP2 (never a map, preserving `go-redis .Info()` /
  `redis-cli info` parsing). `SCAN cursor [MATCH pattern] [COUNT n]`
  iterates the typed keyspace via an insertion-sequence cursor, returning
  every key present for the whole scan under concurrent mutation — see
  [ADR-0014](./docs/adr/0014-scan-cursor-and-info-wire-format.md).
- TUI v2 (M14, tag `m14`): multi-type value rendering (distinct string /
  list / hash views driven by `TYPE`), `SCAN`-backed paging in the keys
  pane (removes the v1 large-keyspace caveat), an AUTH prompt on connect
  when the server has `requirepass`, and an `INFO`-driven status bar
  (fsync policy, dbsize, uptime) — on the same `internal/client` package.
  See [ADR-0015](./docs/adr/0015-tui-v2-scan-paging-and-tls-deferral.md).
- Observability (M16, tag `m16`): OpenTelemetry logs, metrics, and traces
  over OTLP (gRPC | HTTP) to the Grafana **LGTM** stack. RED metrics per
  command, a connection→command→{store,aof} trace tree, and
  trace-correlated structured logs via an `slog` bridge. **Off by
  default** — with no `-otel-endpoint` the SDK no-op providers are
  installed and the hot path stays allocation-light, so behaviour and
  benchmarks match the pre-M16 binary (semver unchanged; M15 still earns
  the major). Config: `-otel-endpoint`, `-otel-protocol`,
  `-otel-service-name`, `-otel-sampling`, `-otel-capture-keys`; a
  `deploy/` `grafana/otel-lgtm` stack for local viewing. Telemetry export
  failures are logged and dropped — they never fail a command. See
  [ADR-0017](./docs/adr/0017-opentelemetry-signal-model-and-otlp-export.md).

### Changed

- **Protected mode (M15) — the deliberate breaking change that earns
  `2.0.0`.** The server now refuses to *start* when bound to a non-loopback
  address with neither `-requirepass` nor TLS configured, exiting non-zero
  with a message naming the fix. This flips v1's implicit "bind anywhere,
  serve anyone" contract. Override with `-protected-mode no` (logged);
  loopback binds and auth'd/TLS binds are unaffected. A bad flag value
  exits 2; the unsafe-bind refusal exits 1. See
  [ADR-0016](./docs/adr/0016-protected-mode-and-atomic-keyspace-ops.md).
- `GET` misses and failed `SET NX/XX` now reply with the RESP3 null (`_`)
  to a RESP3 client; RESP2 clients still receive `$-1`, byte-identical to
  v1.

### Fixed

- Telemetry disabled-path overhead (M17): with OpenTelemetry compiled in
  but off, the RED chokepoint rebuilt attribute sets and metric options
  per command, costing ~18–21% throughput vs the pre-M16 binary
  (attribute/option construction allocates even against no-op providers).
  Per-command instrument attributes are now memoized at construction —
  **no `if enabled` hot-path guard added** — restoring `SET` to parity and
  `GET` to within ~7% (disabled path 29 → 14 allocs/op). See the M17
  amendment in [ADR-0017](./docs/adr/0017-opentelemetry-signal-model-and-otlp-export.md).

### Security

- RESP codec pre-auth DoS bounds (M17): the frame decoder capped bulk-string
  size but not array element count or nesting depth, so a single tiny packet
  from any peer that could reach the port could drive an unbounded
  `make([]Value, n)` (memory-amplification OOM) or unbounded recursion
  (stack-exhaustion fatal panic) — **before** the auth gate. Added
  `MaxArrayLen` (1 048 576, matching Redis `proto-max-multibulk-len`) and
  `MaxDepth` (32), both rejected with `ErrTooLarge` before any allocation.
  Found by the v2.0.0 release-gate security review — see
  [`docs/SECURITY-REVIEW-v2.md`](./docs/SECURITY-REVIEW-v2.md).

## [v1.0.0] — 2026-06-17

First public release. Closes M0–M9 of the roadmap. See
[`docs/release-notes/v1.0.0.md`](./docs/release-notes/v1.0.0.md) for the
hand-curated highlights.

### Added — server, persistence, TTL (M1–M5)

- RESP2 wire protocol subset; `redis-cli` compatible (M1, tag `m1`).
- In-memory store with `sync.RWMutex` and strict `[]byte` values (M2, tag `m2`).
  Commands: `PING`, `ECHO`, `GET`, `SET key value [NX|XX]`, `DEL`, `EXISTS`,
  `INCR`, `DECR`, `KEYS pattern`, `FLUSHDB`, `DBSIZE`.
- AOF persistence with `appendfsync = always | everysec | no` policies; v1
  format with version byte; replay-blocks-`Accept` startup (M3, tag `m3`).
- Crash-injection test: SIGKILL mid-write, restart, verify every acked SET
  under `fsync=always` is present (M3).
- TTL with lazy + 1 Hz sweep eviction; `EXPIRE`, `PEXPIRE`, `PEXPIREAT`,
  `TTL`, `PTTL`, `PERSIST`; `SET ... EX seconds | PX ms`. AOF format bumps
  to v2 with absolute-PXAT encoding (M4, tag `m4`).
- `BGREWRITEAOF` compaction with side-buffer dual-write and atomic rename;
  crash-safe at every offset (M5, tag `m5`).

### Added — CLI, TUI, integration tests (M6–M8)

- `internal/client/` — shared RESP2 client over `net.Conn` consumed by both
  CLI and TUI.
- `toykv-cli` — one-shot, REPL, piped, and `-raw` modes; pretty-printer
  follows `redis-cli` conventions; exit-status mapping `0 / 1 / 2`.
- `toykv-tui` — Bubble Tea two-pane TUI; 2 Hz refresh via `tea.Cmd`;
  all PRD §5.5 keybindings (`j/k`, `/`, `n`, `e`, `d`, `t`, `i`, `D`, `F`,
  `r`, `:`, `q`).
- `test/e2e/` — subprocess harness compiles `cmd/toykv` and `cmd/toykv-cli`
  in `TestMain`, drives them via `go-redis/v9`, `toykv-cli`, and a
  `redis-cli` byte-compat sweep (skipped when `redis-cli` is off `PATH`).
- TUI smoke migrated to `teatest.NewTestModel` + `teatest.WaitFor`.
- CI splits `unit tests` and `e2e tests` steps; Linux runners install
  `redis-tools` to exercise the `redis-cli` sweep.

### Added — release polish, bench, chaos (M9)

- `docs/BENCHMARKS.md` — first recorded baseline across three fsync
  policies (`valkey-benchmark -n 100000` via Docker; methodology written
  up).
- `Makefile` — `bench` parametrised via `BENCH_HOST` / `BENCH_PORT` /
  `BENCH_N` / `BENCH_TESTS`; new `bench-prep`, `chaos`, `chaos-smoke`
  targets.
- `test/chaos/` — composition soak. Restart-capable subprocess, minimal
  raw RESP2 client, mixed-workload driver. Three invariants: acked-write
  survival, monotonic INCR across restarts, no torn-tail / panic across
  rewrites + pauses + kills. Long form gated behind `//go:build chaos`;
  CI runs the 30-second smoke variant.
- `docs/TESTING.md` — Crash matrix section: `surface | risk | owning
  milestone | test file | invariant proven`. Frames the chaos suite as
  the composition layer (every other test owns one fault).
- `README.md` — release-shaped landing page. Install (release archive
  curl + tar), commands reference table, fsync tradeoff with embedded
  bench snapshot, security/limitations `⚠` callout, CLI modes + exit
  codes, ASCII TUI rendering as fallback for `assets/tui.gif`
  (regeneratable via `assets/tui.tape`).
- `.goreleaser.yaml` — three `builds:`, four platforms
  (`{darwin,linux} × {amd64,arm64}`), all-in-one archive carrying every
  binary + LICENSE + key docs, `SHA256SUMS` alongside.
- `.github/workflows/release.yml` — `goreleaser-action@v6` on `v*` tag
  push.
- ADRs added: `0003` AOF format + fsync policy, `0004` TTL canonical
  PXAT encoding, `0005` BGREWRITEAOF dual-write + atomic-rename swap,
  `0008` CLI stdlib + mode detection, `0009` TUI Bubble Tea + injectable
  `Doer`, `0010` v1 release artefacts policy.
- Per-PR phase journals: `docs/journal/00..14`.
- Hand-curated release notes: `docs/release-notes/v1.0.0.md`.

### Security

- v1 ships with **no auth, no TLS, no IP allowlist**. Default port
  `:6390`; bind to `127.0.0.1`. Full threat model in
  [`docs/SECURITY.md`](./docs/SECURITY.md). Documented limit, not a
  defect.

### Out of scope (deferred — see ROADMAP v2 / v3)

- Lists, hashes, sets, sorted sets, pub/sub.
- `AUTH`, TLS, `INFO`, `SCAN`, RDB snapshots.
- Replication, clustering, `tinyraft` integration.
- Windows binaries, Homebrew tap, Docker image, apt PPA.

## [v0.0.0] — 2026-06-11

### Added

- Repo scaffold: Go module `github.com/prajwalmahajan101/toykv`, Go 1.26.
- Package skeletons under `internal/{resp,store,aof,server,client,cli,tui}`.
- Three binary entrypoints: `cmd/toykv`, `cmd/toykv-cli`, `cmd/toykv-tui`
  (all M0 placeholders printing `--help`).
- `Makefile` with `build`, `run`, `cli`, `tui`, `fmt`, `vet`, `lint`,
  `test`, `bench`, `ci`, `hooks`, `clean` targets.
- `.golangci.yml` (10 linters; security + correctness blocking; style
  enforced in CI).
- `.githooks/pre-commit` (gofmt + go vet on staged files; install via
  `make hooks`).
- GitHub Actions CI: lint + test matrix (ubuntu+macos × Go 1.25/1.26) +
  build.
- Documentation set under `docs/`: PRD, ROADMAP, HLD, LLD, TESTING,
  BENCHMARKS, RELEASE_PLAN, SECURITY, CONTRIBUTING, ADR index.
- MIT licence.

[Unreleased]: https://github.com/prajwalmahajan101/toykv/compare/v1.0.0...HEAD
[v1.0.0]: https://github.com/prajwalmahajan101/toykv/releases/tag/v1.0.0
[v0.0.0]: https://github.com/prajwalmahajan101/toykv/releases/tag/v0.0.0
