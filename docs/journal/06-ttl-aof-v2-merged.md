# 06 — M4 PR C merged (AOF v2 + canonical PXAT) — M4 closed

**Date:** 2026-06-12
**Merge:** PR #12 (`feat/ttl-aof-v2` → `main`), rebase, 5 commits.
**Tag:** `m4` at `837d7bb`.

## Decision / surprise

The M3 journal made a specific prediction:

> The version field actually works as designed, the v1→v2 diff in `format.go` is **one accepted-versions list and one constant**. Prediction in the M3 journal: small. We'll find out.

That landed exactly right. The complete `format.go` diff for the version bump is:

- One new constant (`Version2 = 0x02`).
- One new constant alias (`CurrentVersion = Version2`) so `writeHeader` reads naturally.
- One package-level slice (`supportedVersions = []byte{Version1, Version2}`).
- One swap in `writeHeader` (Version1 → CurrentVersion).
- One swap in `readHeader` (single-byte check → loop over supportedVersions).

That's 5 small surface-area changes. No record-level reshape. No replayer changes — the canonical PXAT/PEXPIREAT records are RESP arrays of well-known commands, so they go through `s.dispatch` like everything else. **The format paid for itself**, which is the kind of design win I want to be able to point at when arguing for upstream investment in versioning that everyone says "we don't need yet."

The other notable moment: **`TestPRB_AOFAppendsAreV1Shape` getting deleted as designed**. PR B shipped a known asymmetry (TTLs in memory, not on disk) and pinned the regression with a test. PR C closes the asymmetry and the same diff deletes the pinning test, replacing it with the positive contract (`TestAOFRoundTrip_PreservesTTLs`). That test deletion is the cleanest possible signal that the asymmetry is gone and the team is allowed to forget about it.

## Why it mattered

The "ship a documented asymmetry, pin it with a test, delete the test next PR" discipline is what kept the M4 series from leaking partial state into long-term confusion. If PR B had shipped TTLs-in-memory-without-restart silently, every future debugging session would have a "wait, do TTLs survive restart?" cycle. Naming the regression made it concrete; killing the test killed the doubt.

## Code / measurement

- Commits: `295dfcc` `ba1bc81` `2ff796c` `12f9933` `837d7bb`.
- Tag `m4` at `837d7bb`.
- Crash test `TestAOF_CrashInjection_TTL` under `-count=10`: ~4.5s wall clock, clean.
- AOF v1 backwards compat: `TestAOF_V1FileReplaysOnV2Binary` (hand-crafted v1 file, replayed on M4 binary).
- CI: 6/6 green (lint + linux/mac × go 1.25/1.26 + build).

### The PXAT vs PX rejection — worth a sentence

The "obvious" encoding was `SET k v PX <ms>` because it mirrors the wire form. The reason it's wrong is the most quotable thing in ADR-0004: a `SET k v PX 5000` written 10 minutes ago, replayed under that rule, would silently extend the key's life by the entire downtime. Absolute deadlines (PXAT) anchor to wall-clock. The trap is exactly the kind that's hard to spot in a code review and impossible to spot in a benchmark — which is why the ADR exists.

## Blog-worthy?

Three threads worth a post each:

1. **"The version field that pays for itself."** Pre-write the gate (M3), prove it works at the first bump (M4), get backwards compat for free. The narrative arc is built into the journal entries — `02-aof.md` predicted it, `06-ttl-aof-v2-merged.md` (this entry) records the landing.
2. **"Ship the regression test, delete it next PR."** The discipline. Includes the diff: the deleted test, the replacement positive test, the ADR that documents the discipline.
3. **"Absolute deadlines are the only sane TTL persistence."** Pulls out ADR-0004's rejected-alternatives section. Particularly good for a Redis-comparison post — Redis got this right early and most people don't realise *why* the AOF stores PEXPIREAT and not PEXPIRE.

## What's next

- M4 done. 4 milestones remaining for v1.0.0: **M5 (compaction via BGREWRITEAOF), M6 (CLI), M7 (TUI), M8 (integration tests), M9 (bench + polish + v1.0.0 tag)**.
- M5 is the next risk-heavy milestone: rewrite-during-writes is the kind of bug that doesn't show up in single-threaded tests. Predicted shape: three sub-PRs again, with the BGREWRITEAOF crash test as the milestone-owned risk. Will the version byte help here too? Not directly — compaction emits the same v2 format. But the *replay* path will need to handle "rewrite finished, switch files, but the old file still has writes appended after the snapshot point" — that's the lift.
- The crash-test infrastructure (`startChild` + `runChildServer`) is now used by both M3 and M4 tests; M5 makes three. Probably worth promoting from "one-off test helper" to a named pattern when M5 lands.
