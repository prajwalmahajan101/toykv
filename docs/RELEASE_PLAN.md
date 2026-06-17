# toykv — Release Plan

## Versioning

SemVer 2.0.0.
- **MAJOR** — breaking wire-protocol or AOF-format change.
- **MINOR** — new command, new TUI feature, additive AOF field.
- **PATCH** — bug fixes, perf, docs.

Pre-v1.0.0: every milestone gets a lightweight milestone tag `mN` (e.g. `m4` after M4 merges). Only `v1.0.0` and beyond use SemVer version tags.

## Branching

- `main` is always green and tagged-ready.
- Feature work on `feat/<slug>` branches.
- **No direct commits to `main`.** PR + green CI + 1 self-review pass.
- Conventional Commits enforced via commit-msg hook (`feat|fix|refactor|docs|test|chore`).
- Subject ≤72 chars, imperative, no trailing period.

## Release cuts

### v1.0.0 — exit criteria

1. ✅ PRD §7 Definition of Done fully checked.
2. ✅ ROADMAP M0..M9 merged and tagged (`m0`..`m8` per-milestone, `v1.0.0` on M9 merge).
3. ✅ CI green for ≥3 consecutive runs on `main` (see Actions tab; PR #20 + the M9 PR series are the immediate evidence).
4. Manual smoke pass — *executed pre-tag against the `feat/release-v1` HEAD*:
   - ✅ `redis-cli` against running server for every command in PRD §5.1 (CI: `test/e2e/rediscli_test.go` exercises the byte-compat sweep on Linux runners).
   - ✅ `toykv-cli` (one-shot, REPL, piped) against running server for every command (CI: `test/e2e/cli_test.go`).
   - ✅ `toykv-tui` against running server for every mutating command (CI: `cmd/toykv-tui/smoke_test.go` via teatest).
   - ✅ Crash test: server killed mid-write, restart, verify replay (CI: `internal/server/aof_crash_test.go`; release-confidence soak in `test/chaos/`).
5. ✅ README contains: install, quickstart, command reference (table), fsync tradeoff (with bench snapshot), security callout, TUI screenshot (`assets/tui.gif` regeneratable from `assets/tui.tape`, ASCII fallback inline), bench numbers.
6. ✅ ADRs present: 0003 (AOF format + fsync policy), 0004 (TTL PXAT encoding), 0005 (BGREWRITEAOF dual-write), 0008 (CLI stdlib + mode detection), 0009 (TUI Bubble Tea + injectable Doer), 0010 (release artefacts). Budget rationale recorded in `docs/adr/README.md`.

### Post-v1 patch releases

- `v1.0.x` — bugfix only.
- `v1.x.0` — additive commands, TUI improvements, RESP3 negotiation (if added without breaking RESP2 clients).
- `v2.0.0` — only if AOF format changes or wire-protocol breaks.

### v2.0 and v3.0

Full feature ladders live in [ROADMAP.md — v2.0](./ROADMAP.md#v20--useful-proposed-not-committed) and [v3.0](./ROADMAP.md#v30--distributed-the-tinyraft-payoff). Neither is committed; the default trajectory is **Option A (ship v1, stop)** until reviewed post-v1.

## Distribution

- Goreleaser → GitHub Releases, archives for `darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`.
- Each release ships **three binaries**: `toykv`, `toykv-cli`, `toykv-tui`.
- SHA256SUMS file alongside archives.
- No homebrew tap / apt repo for v1 — direct download only.

## Rollback / hotfix policy

- Tagged release breaking? `git revert` the offending commit on `main`, cut a `v1.0.(x+1)` patch.
- AOF format never broken in a patch. If a bug requires a new format, gate it behind a version byte and ship in next minor.

## Security disclosure

- No auth in v1 — bind to localhost by default, documented in README.
- Vulnerability reports: GitHub Security Advisories (private). 90-day disclosure window.

## Communication

- Release notes inline on GitHub Release (auto-generated from Conventional Commits, hand-edited highlights).
- Hashnode post written *after* `v1.0.0` ships, not before — avoids vapourware.
