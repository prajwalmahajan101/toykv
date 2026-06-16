# 13 — M8 Integration tests (end-to-end protocol compat)

**Date:** 2026-06-16
**Branch:** `feat/integration-tests`
**ADR:** none — M8 is pure protocol compat against shipped artifacts, not a new architectural decision (the ADR ledger budget noted in [ADR README](../adr/README.md) is honoured).
**Trigger:** M7 closed with the TUI smoke test inlined into `cmd/toykv-tui/`. M8 is the first time the *shipped binaries* (not in-process `server.New`) get exercised: a real subprocess, a real third-party client (`go-redis/v9`), and the real `redis-cli` byte-compat sweep that the README has implicitly promised since M1.

## Decision / surprise

The expensive call was deciding **what M8 is not**. The previous-roadmap shape made M8 the catch-all for "anything risky we didn't get to upstream" — crash injection, concurrent stress, weird-edge fuzz. The current roadmap (`docs/ROADMAP.md:199`) moved every one of those tests *into* the milestone that owns the risk: M2 owns the concurrent stress, M3 owns the durability crash matrix, M4 owns the TTL race, M5 owns the rewrite-during-writes crash. Honouring that meant resisting the temptation to grow M8 every time a "what if…" question came up. M8 reads now as a three-layer protocol smoke and nothing more.

Three calls landed differently from the initial sketch:

1. **Subprocess builds, not subprocess re-exec.** The plan said "reuse the M3 `TestMain` re-exec trick". In practice `go build -o $tmp/toykv ./cmd/toykv` from `TestMain` is cleaner — the harness compiles each binary once per `go test` invocation and every test spawns a fresh process. No env-var dance, no shared mutable state, no `TOYKV_E2E_CHILD=1` flag. Cost: ~1.2s of one-time build per `go test` run. Worth it.

2. **No PTY for the REPL test.** Driving `toykv-cli`'s REPL would have required a PTY dep (`creack/pty`). Reading `cmd/toykv-cli/main.go:170` made it clear that `dispatchLine` is shared between REPL and piped — so testing the piped path covers the same code with the same assertions. The PTY-shaped test would have proved the *prompt rendering*, not the *protocol*, and M8's mandate is protocol compat. REPL stays covered by the existing `internal/cli` unit tests.

3. **`waitReady` needed proper RESP framing.** The first cut sent inline `PING\r\n` and got `-ERR Protocol error` back instantly. toykv's RESP reader is strict — no inline-command fallback — so the harness now sends a proper `*1\r\n$4\r\nPING\r\n`. This is a *good* discovery: it confirms the server rejects malformed framing rather than silently best-effort-ing it. If we ever add inline-command compatibility later, this is the line to flip.

The minor surprise was teatest's `WaitFor` semantics. The first SET test asserted that the rendered terminal contained `"bar"` (the value just written). But the TUI's value pane doesn't echo the inline reply — it shows the previous response. The test passes once `"foo"` (the new key) or `"OK"` (the SET reply) appears in the keys pane or status bar. The lesson: teatest assertions are observations of the *rendered* UI, not of the *model state*. Server-side `GET` is still the durable signal.

## Why it mattered

Three things this PR locks in:

1. **The shipped binaries are now under test on every CI run.** Until M8, every Go test exercised an *in-process* server. A regression that only showed up when the binary was built (e.g. a `go:embed` path, a `GOOS=darwin` linker quirk, a `-ldflags` mistake at release time) would have shipped silently. The harness in `test/e2e/harness.go` builds `cmd/toykv` and `cmd/toykv-cli` exactly the way `make build` does, then drives them. CI now fails on a release-shaped regression in unit-suite time.

2. **`go-redis/v9` is the real cross-implementation compat gate.** A toykv-CLI-against-toykv-server test only proves the two halves of toykv agree with each other. A go-redis-against-toykv-server test proves toykv agrees with an *independent* RESP2 implementation. The TTL suite caught two semantics I'd have left ambiguous otherwise: `TTL missing` must return -2 (not -1), and `EXPIRE missing` must return 0 (not raise). Both were already correct; the test makes them *contractual*.

3. **The `redis-cli` byte-compat sweep is now executable, not aspirational.** The README has said "redis-cli works against it" since M1. Until this PR there was no CI gate enforcing it. `test/e2e/rediscli_test.go` skips locally (most machines don't have `redis-cli` on PATH) and runs on Ubuntu CI via `apt install redis-tools`. The next time someone tweaks RESP framing in `internal/resp`, real `redis-cli` regression is one CI run away.

## Code / measurement

- `test/e2e/harness.go` — 260 lines (BuildBinaries, StartServer, RunCLI, waitReady, freePort).
- `test/e2e/main_test.go` — 25 lines (TestMain + harness smoke).
- `test/e2e/protocol_strings_test.go` — 130 lines (PING/ECHO/SET/GET/DEL/EXISTS/INCR/DECR/KEYS/DBSIZE/FLUSHDB).
- `test/e2e/protocol_ttl_test.go` — 120 lines (EXPIRE/PEXPIRE/PEXPIREAT/TTL/PTTL/PERSIST + SET EX/PX).
- `test/e2e/cli_test.go` — 65 lines (one-shot, piped, raw, server-error exit codes).
- `test/e2e/rediscli_test.go` — 110 lines (PRD §5.1 byte-compat sweep, t.Skip if no redis-cli).
- `test/e2e/rewrite_restart_test.go` — 85 lines (50-key mixed workload, BGREWRITEAOF, restart, verify).
- `cmd/toykv-tui/smoke_test.go` — refactored to `teatest.NewTestModel` + `teatest.WaitFor` (now 175 lines, was 184).
- `.github/workflows/ci.yml` — split `test` step into `unit tests` and `e2e tests`; ubuntu installs `redis-tools` via apt.
- New deps: `github.com/redis/go-redis/v9 v9.20.1`, `github.com/charmbracelet/x/exp/teatest`.

Local timing: `go test -race -timeout 10m ./test/e2e/...` finishes in ~2.2s including the one-time `go build` of two binaries. Full suite (`go test -race ./...`) finishes under 4s.

## Score

- Zero new ADRs. The "no more ADRs" line from journal 12 holds; M8 is not a new architectural decision, just the first exercise of the existing artifacts.
- Zero changes to `internal/store`, `internal/aof`, `internal/server`, `internal/resp`, `internal/client`. Every protocol-compat finding routed through existing code. The TTL `-2 / -1` sentinels, the `EXPIRE missing → 0` semantics, and the `BGREWRITEAOF → restart` round-trip were all already correct — M8 made them tested, not implemented.
- Two new top-level test layers (`test/e2e/` and the teatest-driven TUI smoke) cost ~800 LOC. The marginal CI minute is well under one; the marginal local-`go test` second is two. For the durable confidence that "the binary we ship is the binary CI ran", the line/second budget is small.
- The PR-split discipline held: PR A (deps + harness + go-redis), PR B (CLI + redis-cli + rewrite-restart), PR C (teatest + CI), PR D (this journal + ROADMAP). Each PR is independently CI-green and revertible.

## What's next

- **Immediate:** open the four PRs in order, merge, tag `m8`.
- **M9 (bench + polish + v1.0.0):**
  - `make bench` → `redis-benchmark -p 6390 -t set,get -n 100000`; record numbers in `docs/BENCHMARKS.md` (file already exists, currently empty).
  - README polish — revise the ADR-budget paragraph (now 6 ADRs not 4), add a TUI screenshot/GIF, document the new e2e test layer alongside unit + crash-injection.
  - Goreleaser config for darwin/linux × amd64/arm64 × three binaries.
  - Tag `v1.0.0`. Honest release notes — what's in, what's deliberately out (no auth, no TLS, no cluster).
- **Open question for M9:** keep `test/e2e/` at the repo root, or move it to `internal/e2e/` once we extract `toykv-go` as the SDK? Probably keep — the harness depends on the shipped binaries, which is a property of the *application*, not the SDK. The SDK extraction post-v1.0 should leave `test/e2e/` where it is and let `toykv-go` grow its own integration suite.

## Blog-worthy?

One strong thread:

- **"Three layers of integration testing for a CLI server, and why each one catches a different bug"** — a clean walkthrough of the harness (`exec.Cmd` + free-port probe + RESP-framed PING), the third-party-client compat layer (`go-redis/v9` as the *independent* implementation), and the byte-compat sweep (`redis-cli` proves the README claim). Lands a reusable pattern that applies to any RESP2-shaped service or, more broadly, any CLI server with a public wire protocol. The "build the binary in `TestMain`" trick is one paragraph that I'd have appreciated reading three milestones ago.
