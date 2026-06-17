# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Pre-v1.0.0 milestones are tagged `mN` after each milestone PR merges
on `main` (see [`docs/ROADMAP.md`](./docs/ROADMAP.md)).

## [Unreleased]

### Added

### Changed

### Fixed

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
