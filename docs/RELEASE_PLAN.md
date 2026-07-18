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

### v2.0.0 — exit criteria (M17 release-hardening gate)

The `2.0.0` major is **earned by M15's protected-mode break** (the server refuses a
non-loopback bind without auth/TLS), not defaulted — RESP3 and the AOF v3 bump are additive.
The gate below must be green with **fresh evidence run at release time**, not the roadmap's
word (the roadmap records intended, not verified, state).

Evidence captured 2026-07-18 on `feat/release-v2` (base `9904413`, `go1.26.3`, linux/amd64):

1. ✅ `go vet ./...` clean; `make lint` (golangci-lint v2 config) → **0 issues**.
2. ✅ `go test -race ./...` — full suite green (every package `ok`).
3. ✅ Flaky-test gate: `TestAOF_CrashInjection_DuringRewrite` (incl. `late-kill`) run
   `-race -count=3` fresh, no `-short` → green in 2.19s. Not flaky; the whole test `t.Skip`s
   only under `-short` (subprocess fork), which is intentional, not a quarantine.
4. ✅ Lint-config gate: `.golangci.yml` carries `version: "2"` and runs in `make lint` + `ci:`
   (the earlier "missing schema key" gate item was already closed before M17).
5. ✅ v1→v2→v3 AOF upgrade: `TestOpen_UpgradesOldHeaderInPlace{/v1,/v2}`,
   `TestOpen_UpgradedFileReplaysOldAndNewRecords`, `TestReplay_AcceptsAllSupportedVersions`
   → all PASS (`-race -count=1`). Real old-format files replay in-place under the v3 reader.
6. ✅ Observability no-regression: the disabled path installs SDK **no-op**
   Tracer/Meter/Logger providers (`internal/telemetry/telemetry.go`), so there is no `if
   enabled` hot-path branch; `BenchmarkObserveCommand_Disabled` records the disabled
   command-path cost as the parity guard. `TestExporterDown_NeverFailsCommand` (dead OTLP
   endpoint never fails a command) and `TestDurability_WithInstrumentation` (mutate→append→
   fsync→reply order preserved with tracing compiled in) both PASS; the `test/chaos` crash
   matrix passes `-race` with instrumentation present.
7. ✅ Security review of the M12 auth/TLS + M15 protected-mode surface — see
   [`SECURITY-REVIEW-v2.md`](./SECURITY-REVIEW-v2.md); zero unwaived blocking findings.
8. ✅ Typed bench (`set,get,lpush,rpush,hset`) re-run and recorded in
   [`BENCHMARKS.md`](./BENCHMARKS.md).
9. ✅ ADRs 0011–0017 present (the eight v2 topics across seven files — 0012 bundles the
   tagged-union store model and the AOF v3 format); index budget note current.
10. ✅ `serverVersion == "2.0.0"`; `CHANGELOG.md` `[2.0.0]` section complete incl. the M16
    observability entry and the earned-major note.

**Post-merge tag (run by the maintainer after the `feat/release-v2` PR merges to `main`):**

```
git tag -a v2.0.0 -m "toykv v2.0.0 — deployable, safe-by-default, observable single-node KV"
git push origin v2.0.0        # triggers goreleaser → 3 binaries × 4 platforms + SHA256SUMS
```

### v2.x and v3.0

The full v2 feature ladder (M10–M17) is **committed and shipping** — see
[ROADMAP.md — v2.0](./ROADMAP.md#v20--useful-committed--the-active-plan-post-v1); the
trajectory decision recorded 2026-07-13 is **Option B (v1 → v2)**. The v2.x backlog (RDB
snapshots, native Prometheus scrape, `-aof-truncate`) and the [v3.0 distributed
ladder](./ROADMAP.md#v30--distributed-the-tinyraft-payoff) remain out of committed scope; v3
is the real downstream dependency, blocked on `ToyRaft` shipping as a vendorable library.

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
