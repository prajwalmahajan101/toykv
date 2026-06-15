# 09 — M5 PR C merged (BGREWRITEAOF wired + crash test) — M5 closed

**Date:** 2026-06-12
**Merge:** PR #15 (`feat/m5-bgrewriteaof-cmd` → `main`), rebase fast-forward, 1 commit (`eb4835d`).
**Tag:** `m5` at `eb4835d`.

## Decision / surprise

The pleasantly boring closing PR of the M5 series. After PR B's bytes-buffer-empty bug, I was braced for the dispatch/single-flight wiring to misbehave — instead all four functional tests passed first try, and the crash matrix passed on the first run too. 10 iterations clean. That's the dual-write design paying off: because the canonical file is always durable, the only thing the crash test needed to verify was "exactly one file at canonical, no `.tmp`, every ack survives" — invariants that the design *makes structurally true* rather than the test having to fish them out of a narrow timing window.

The one non-trivial decision in this PR was extracting `renderCanonicalSet`. cmdSet had its canonical-form append inline (3 lines: "SET" prefix, optional PXAT). The snapshot bridge needed the exact same encoding. I almost copy-pasted the 3 lines because they're tiny. Decided to extract instead because the *contract* is what matters — "byte-identical record shape between live SET and rewritten SET" is an ADR-0004/0005 invariant, not an implementation detail. Pulling it into one function means any future change to the canonical form lands in both paths automatically. Three lines isn't an abstraction; a *shared contract* is.

## Why it mattered

M5 closes the v1 durability story. Every milestone from M3 onward has owned its own crash test, and the on-disk format has now survived TTL bump and atomic-rewrite swap without changing record shape. The version byte plumbing from M3 paid for itself in M4; the dual-write design from M5 paid for itself in the crash test landing first-try. The discipline of "ship the simplest invariant the *design* can enforce, not the *test* can verify" continues to pay off.

The other thing M5 closes: the `redis-cli BGREWRITEAOF` smoke check. With the binary built fresh, the rewrite round-trips on a real Redis client. That's the unglamorous validation that M9's `redis-benchmark` bench will not be tripping on a missing command at the wire level.

## Code / measurement

- Server: 268 new lines (handler + 4 functional tests + 1 crash test).
- AOF: 0 changes — PR B's surface stayed sufficient.
- Store: 0 changes — PR A's `Snapshot()` stayed sufficient.
- Roadmap: M5 ✅, 6/10 milestones done.

## Score

- 3 PRs shipped, all rebase-merged to main, all CI green.
- 1 bug found during development (resp.Writer scratch buffer needed Flush), caught by the unit test I wrote before the integration test. Right order.
- ADR-0005 written before code (drafted in the plan), refined after code. The "dual-write" name and the crash-invariant table both survived contact with the implementation.

## Next

M6 — CLI. Branch `feat/cli`. New surface: `internal/client/` shared RESP client, `cmd/toykv-cli/` one-shot/REPL/piped. No new persistence work; the M3/M4/M5 durability layer is now feature-complete for v1.
