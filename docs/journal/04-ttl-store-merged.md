# 04 — M4 PR A merged (store TTL + sweeper)

**Date:** 2026-06-11
**Merge:** PR #10 (`feat/ttl-store` → `main`), rebase, 4 commits.

## Decision / surprise

The plan predicted "PR A is a self-contained refactor that's much easier to review on its own." Confirmed — the diff is +518/-36 inside `internal/store/` only, no callers needed updating. The store can grow TTL semantics without the server learning what TTL is yet. That's a quiet win for the M2 design call to keep `entry` a struct from day one.

The interesting moment was the **race test catching itself**.

## Why it mattered

`TestTTL_LockUpgradeRace_NoSpuriousMiss` failed on its first stress run (4 violations in 468k reads). The bug was in the *test*, not the code: I captured `t0 := time.Now()` **before** `Get`, but Get's internal time check happens later — sometimes much later if the scheduler preempts. So a Get that correctly returned nil (because the entry's TTL had elapsed by the time Get checked) would be flagged as "spurious nil for unexpired key."

The fix forced me to think through Go's memory model explicitly: capture `tAfter` **after** Get (upper-bounds Get's internal time check) and `atomic.Load` lastExpireAt **before** Get (synchronizes-with writer's `atomic.Store`, guarantees Get's RLock sees the committed entry). Then `tAfter < loadedExpireAt` is a real violation — no false positives possible.

Worth the journal because the *first* design of the test felt obviously right and was obviously wrong. The race detector would never have caught this — it's a logical timing race, not a data race. The fact that the test was sensitive to its own timing assumptions was only visible under `-count=20` stress. Single-run passes hid the issue.

## Code / measurement

- Commits (rebased onto main): `d898441` `840b3a7` `046b437` `90a308f`.
- Race test under `-race -count=20`: ~38s total, all passes clean.
- CI matrix: 5 checks (lint + linux/mac × go 1.25/1.26), all green.
- Diff: `+518 -36` across 4 files in `internal/store/`.

## Blog-worthy?

Yes — strongly. Two threads worth pulling:

1. **"Your concurrency test has its own concurrency bugs."** The store code under test was correct; the test's assertion logic was wrong. The right shape only emerged from drawing the memory-model arrows on paper: which happens-before edge guarantees what the reader can conclude about the writer's last commit.
2. **"Lazy expiry + lock upgrade is the price of `sync.RWMutex` not supporting upgrade-in-place."** In a language with upgradeable read locks this would be one line. In Go it's release-and-reacquire-and-double-check. Worth showing the diff cost.

## What's next

- PR B (TTL commands on the wire) — already in flight as I write this. The plan called the bundled-asymmetry trade-off correctly: TTLs will work in memory, won't survive restart, for exactly one PR cycle.
- PR C (AOF v2 + crash test + ADR-0004 + tag `m4`).
