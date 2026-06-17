# 14 — M9 Bench + polish + v1.0.0 release

**Date:** 2026-06-17
**Branch:** `feat/release-v1`
**ADR:** [0010 — v1 release artefacts](../adr/0010-release-artefacts-and-distribution.md)
**Trigger:** M0–M8 shipped; PR #20 merged and `main` green across all CI checks (lint, test matrix × 4, build, GitGuardian). The roadmap had exactly one ⬜ left: bench numbers, README polish, Goreleaser, tag. M9's job was to satisfy the v1.0.0 exit criteria already enumerated in `docs/RELEASE_PLAN.md:22-34`, not to invent new ones.

## Decision / surprise

Three calls landed differently from the initial plan.

1. **Chaos testing was added to M9 even though the ROADMAP didn't list it.** The plan got reviewed live; the question "are we not adding chaos testing??" surfaced before any code landed. The honest answer was "no, the ROADMAP places crash injection in M3/M4/M5 and a defence-in-depth in M8 — those exist already." The honest follow-up was that *composed* faults (kill + pause + rewrite + sustained writes overlapping) had no test. The owning-milestone discipline (each milestone owns one fault) means no single milestone owns the *composition*. So `test/chaos/` ships as the release-confidence soak — the only layer that intentionally overlaps faults, gated by `-short` so CI runs a 30-second smoke and `make chaos` runs the 5-minute long form. It's not in the ROADMAP; it lands in M9 because it's release-shaped, not because it was promised.

2. **One big README commit, not six.** The original plan had separate commits for install / commands / fsync / security / CLI / TUI sections. The user pushed back: "combine all the README.md commits." Reading the final diff, the call holds — every section is part of the same RELEASE_PLAN exit-criterion #5 line, splitting would be cosmetic. The criterion-to-commit relationship is one-to-one; the section-to-commit relationship being one-to-one would have been bikeshedding.

3. **Bench numbers were captured from a Dockerised valkey, not native `redis-benchmark`.** The host (Arch Linux) doesn't have `redis` in `extra/`; valkey is. Installing valkey natively needs sudo. The cleanest answer was `docker run --rm --network host valkey/valkey:8-alpine valkey-benchmark` — same tool, same protocol, zero local install footprint, and `docker rmi` cleaned up afterwards. The recorded methodology in `docs/BENCHMARKS.md` calls out the tool explicitly so the next person can reproduce it without guessing what was used.

The bench numbers themselves were the surprise. On the dev box (13th-gen i7-1355U + NVMe + warm page cache), `fsync=always` came out **fastest** — not slowest. Why: `fdatasync` to a warm-cache NVMe is well under 100 µs, cheaper than the per-request RESP+mutex overhead that dominates `everysec`/`no` once disk pressure isn't the bottleneck. The README and BENCHMARKS.md both call this out — the conventional `no > everysec > always` ordering will reassert on a slower disk; the numbers are recorded, not promoted as a target.

## Why it mattered

Four things M9 locks in:

1. **`v1.0.0` is real, not aspirational.** Every prior milestone was a step toward "ships." M9 is the step that *ships*. Goreleaser config is validated (`goreleaser check`) and snapshot-built successfully — produced four archives (`darwin/linux × amd64/arm64`), each carrying all three binaries (`toykv`, `toykv-cli`, `toykv-tui`) plus README/LICENSE/SECURITY/PRD/ROADMAP and a `SHA256SUMS` file. The `.github/workflows/release.yml` triggers on `v*` tag push, so a `git push origin v1.0.0` is the last manual step — and the workflow is independently testable via snapshot before that step ever happens.

2. **The bench surface is now reproducible by anyone with `docker` or `redis-tools`.** Before M9 the BENCHMARKS.md table was a placeholder. After M9 it has real numbers and a recorded methodology, but more importantly the `make bench` and `make bench-prep` targets accept `BENCH_HOST` / `BENCH_PORT` / `BENCH_N` / `BENCH_TESTS` overrides so a fresh contributor can vary the knob and report. The numbers in the table are a single host's snapshot; the bench *process* is the durable artefact.

3. **The chaos suite is the first time M3/M4/M5's faults overlap in one test run.** Each upstream milestone owns one fault and proves the invariant *for that fault in isolation*. `test/chaos/` runs them together — workers SET/GET/DEL/INCR while a kill-injector SIGKILLs every ~1.5 s, a pauser SIGSTOPs every 2 s, and a rewriter triggers `BGREWRITEAOF` every 3 s. The acked-write-survival test on the 8-second smoke run captures ~318 000 ops, ~25 000 errors (during restart windows — expected), and zero lost acked writes. The monotonic-INCR test asserts the counter never regresses across restarts. The torn-tail test asserts no `panic`/`fatal` appears in stderr across the whole soak. All three invariants pass — composition adds confidence; it doesn't surface a new bug. (Which is the *desired* outcome: M3/M4/M5 already owned the bugs.)

4. **The README is now a release-shaped landing page, not a contributor's quick-start.** The Install section assumes `curl + tar`, not `make build`. The commands table replaces the one-line PRD §5.1 dump with a proper reference (command / arity / reply / notes). The fsync tradeoff section embeds the bench snapshot with the "this hardware inverted the conventional ordering — here's why" caveat instead of hand-waving. The security callout is now a `⚠` blockquote above the Install section instead of buried under "What it isn't." Three concrete signals a first-time visitor cares about: "is this safe?", "how do I get it?", "what does it do?" — answered before the layout diagram.

## Code / measurement

- `Makefile` — added `bench-prep`, `chaos`, `chaos-smoke` targets; parametrised `bench` via `BENCH_HOST/BENCH_PORT/BENCH_N/BENCH_TESTS`.
- `docs/BENCHMARKS.md` — real numbers for three fsync policies, methodology updated to call out `valkey-benchmark via docker` as the canonical tool.
- `test/chaos/harness.go` (~280 LoC) — `Server` (restart-capable subprocess), `RawClient` (minimal RESP2 over `net.Conn`), `Workload` (N-goroutine mixed driver), persistent stderr buffer across restarts.
- `test/chaos/main_test.go` (~50 LoC) — `TestMain` builds `cmd/toykv` once; `TestHarnessSmoke` is the always-on sanity check.
- `test/chaos/invariants_test.go` (~310 LoC) — three soak tests (`TestAckedWriteSurvival`, `TestMonotonicINCR`, `TestNoTornTailUnderRewrite`) with a shared `chaosConfig` that scales between `-short` and full form.
- `.github/workflows/ci.yml` — new `chaos-smoke` job (ubuntu, 3 min budget); `build` now `needs: [lint, test, chaos-smoke]`.
- `.github/workflows/release.yml` — new file; goreleaser-action@v6 on `v*` tag push.
- `.goreleaser.yaml` — three `builds:`, four platforms, all-in-one archive, `SHA256SUMS`, GitHub auto-changelog filtered to feat/fix/refactor.
- `assets/tui.tape` + `assets/README.md` — vhs script (regen path) and explanation.
- `README.md` — full polish sweep covering all six RELEASE_PLAN exit-criterion items.
- `docs/adr/0010-release-artefacts-and-distribution.md` — new ADR. Records: goreleaser-only build, no homebrew/apt/Docker for v1, four-archive shape, `SHA256SUMS` next to archives.
- `docs/adr/README.md` — index updated; budget rationale revised (M9 takes one ADR; M8 took zero; counter is now 7 written).
- `docs/TESTING.md` — new "Crash matrix" section: surface | risk | owning milestone | test file | invariant proven. Frames chaos as the composition layer.
- `docs/ROADMAP.md` — M9 row → ✅.
- `docs/RELEASE_PLAN.md` — exit-criteria list ticked with evidence pointers (test files, CI jobs).

Local timing:
- `make chaos-smoke` — ~25s (three invariant tests at 8s each, plus the always-on smoke).
- `goreleaser release --snapshot --clean` — ~44s for 12 binary builds (3 × 4) + 4 archives + SHA256SUMS.
- Bench (one fsync policy) — ~3s for 200 000 ops.

## Score

- One new ADR (0010). The release-artefact policy genuinely closes off alternatives (homebrew, Docker, hand-rolled make release) — those rejections need a written home so v2 doesn't relitigate them in passing.
- `test/chaos/` is the only new top-level test package. ~700 LoC. Reuses `test/e2e/`'s "build in `TestMain`" pattern but with its own harness because the Server needs restart semantics e2e doesn't.
- Zero changes to `internal/`. Every M9 artefact is build/test/docs/release plumbing. The shipped binary surface is byte-identical to the M8 tag (modulo the goreleaser ldflags injecting `main.version`/`main.commit`/`main.date`, which were unset before).
- Commit count: 13 atomic commits on `feat/release-v1`. Each conventional-formatted, each independently green under `make ci`, each one a single concern.
- The "combine all README commits" decision held — 1 commit for the README sweep, not 6. The plan's instinct to over-split deserved the correction.

## What's next

- **Immediate:** open PR for `feat/release-v1`; let CI ride through `lint` + `test` matrix + `chaos-smoke` + `build`. Merge into `main`. Then `git tag -a v1.0.0 -m "toykv v1.0.0"` and `git push origin v1.0.0`. The release.yml workflow takes it from there; release notes get hand-edited with the content from `docs/release-notes/v1.0.0.md` once the GitHub Release exists.
- **Open question to revisit after v1.0.0 ships:** the trajectory decision in `docs/ROADMAP.md:171-179`. Default is **Option A (ship v1, stop)**. The honest call is to leave it as Option A until v1.0.0 sees actual use; v2 themes (INFO/metrics, SCAN, AUTH/TLS, lists/hashes) stay backlog-only.
- **Generated artefact follow-ups (post-tag):** `assets/tui.gif` and `assets/tui.png` need a real recording — `vhs assets/tui.tape` after vhs is installed. README falls back to the ASCII rendering until then.

## Blog-worthy?

Two threads worth picking:

1. **"Releasing a learning-project Go binary: goreleaser, snapshot mode, and what we *didn't* ship."** The interesting half is the negative space — no Homebrew tap, no Docker image, no Windows, no apt PPA. Each "no" is recorded in ADR-0010 with a real reason rooted in the v1 security/auth limits. A clean piece on "release scope is also scope" against a backdrop of how easy goreleaser makes the maximalist version of that scope.

2. **"Five layers of crash testing, and what the composition layer actually catches."** The crash matrix table from `docs/TESTING.md` is the lede: every individual fault is owned by the milestone that introduces the risk; chaos is the only layer that overlaps them. The story is *why* that ordering pays for itself — owning-milestone crash tests reproduce on a known seed in seconds (good for dev loop); the chaos soak only proves the cumulative invariant under load (good for release confidence). Two different jobs, two different layers; conflating them was the mistake the original M8 plan made before the roadmap was reshuffled in journal 13.
