# 03 — M3 merged (AOF persistence + crash injection)

**Date:** 2026-06-11
**Merge:** M3 series (5abc43d → 420bcbd), squash-landed on `main`, tagged `m3`.

## Decision / surprise

M3 shipped clean — the design doc in `02-aof.md` predicted the shape, and the merge added zero new ideas. The interesting part of the merge wasn't the code; it was that **the ROADMAP status flip (`docs: mark M3 done`) was the last commit**, not the first. The pattern from M1/M2 holds: ship code → ship ADR → flip status table.

## Why it mattered

Three things, briefly:

1. **Commit shape held.** Five commits, one logical change each: codec → wiring → flags → ADR → roadmap. No "WIP" or "fixup" noise. The `my_code:git_commit` pattern is paying off — diffs are reviewable a month from now without spelunking.
2. **ADR-0003 landed in the same series.** Format byte + fsync policy — exactly the two decisions a future reader will ask "why?" about. Captured before the rationale evaporated.
3. **The crash test is now the contract.** `TestAOF_CrashInjection_Always` is the proof that `+OK` means durable under `appendfsync=always`. Every subsequent milestone that mutates state has to keep it green. That's the invariant — not the code.

## Code / measurement

- Commits in series: `5abc43d` `d1167b1` `a7289ab` `9c28bbb` `420bcbd`.
- Crash test wall time: ~1.4s locally, ran `-count=10` clean before merge.
- Replay baseline (3 records, ~85B): 427µs. Track through M4/M5 — TTL adds a token, compaction halves the file.
- Tag: `m3`. v1.0.0 is now 4/10 milestones done.

## Blog-worthy?

Yes: "the on-disk format is the wire format" still holds after merge. Worth a sentence in the M3 retrospective post: nothing in the PR review surfaced an "actually, what about…" — meaning the corners (NX canonicalisation, replay-with-aof-nil, fsync-before-reply) were caught in design, not review. That's the kind of milestone you want to be able to point at when arguing for design docs before code.

## What's next

- **M4 — TTL on top of AOF v2.** Bump the version byte. First real test of "the format is versioned for a reason." Prediction: diff is small, the test matrix grows (TTL expiry races during replay).
- Carry the crash test forward — extend it to cover `SET … PX` once TTL lands.
