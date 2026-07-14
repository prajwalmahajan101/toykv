# 20 — AOF rewrite durability fix (the "flaky" crash test was a real bug)

**Date:** 2026-07-15
**Branch:** `feat/auth-tls` (M12) → **[PR #30](https://github.com/prajwalmahajan101/toykv/pull/30)**. Commits on `main` after merge: `901806d` (fix: fold side buffer into tmp before rename), `6ba5017` (docs: ADR-0005 correction). Landed on the M12 branch because that's where CI surfaced it.
**Trigger:** M12's CI went red on `test (macos-latest / go 1.26.x)` with `TestAOF_CrashInjection_DuringRewrite/mid-kill`: "2/82 acked SETs lost across rewrite+SIGKILL", keys `live000`/`live001`. The roadmap's M16 gate already listed this test as "flaky — fix or quarantine." Asked to fix it; investigation showed it was **not a flaky test — it was a real, narrow durability bug** the test had been flakily catching since M5.

## Decision / surprise

1. **The failure signature was the whole diagnosis.** The lost keys were `live000` and `live001` — the *first two* live writes issued during the rewrite, never random ones. A test-timing flake loses arbitrary keys; losing specifically the earliest live writes points at an ordering bug between snapshot capture, the side buffer, and the rename. Read the failure, not just the "FAIL."

2. **Root cause: rename-before-drain unlinked acked data.** M5's dual-write rewrite (ADR-0005) appends each live write during a rewrite to *both* the old canonical file (fsynced under `FsyncAlways` → acked) and an in-memory side buffer. But `Rewrite` renamed the fresh file (snapshot only) onto the canonical path **first**, then `DrainAndSwap` wrote the side buffer onto the new file afterwards. The rename unlinks the old inode — so in the `[rename, drain]` window an acked write existed *only* in the now-unreachable old inode plus the in-memory buffer. A SIGKILL there lost it. `-race` on the CI runners widened the window enough to catch it every so often; locally it passed 15/15 (no -race) and 0/20 (with -race) — it never reproduced on my machine at all.

3. **The ADR had rationalized the bug.** ADR-0005 explicitly documented the "post-rename, pre-DrainAndSwap" window and argued it was "no worse than the M3 baseline." That was wrong: the M3 single-file path never unlinked live data mid-write. A crash invariant table that lists a data-loss row and calls it acceptable is a design smell — the durability-first ethos of this project (every acked write survives SIGKILL under `always`) has no "acceptable loss" row.

4. **The LLD had it right the whole time.** LLD §4.4 step 3 says "append the side buffer to `.tmp`, then fsync" *before* step 4 "rename". The implementation shipped the opposite order and the ADR then explained the divergence rather than catching it. The fix makes code match the spec it always had — a reminder that an ADR written *after* buggy code can launder the bug into "decided behaviour."

5. **Fix: fold-before-rename, whole swap under the append lock.** `DrainAndSwap` (post-rename) is replaced by `FinalizeRewrite` (pre-rename fold). It holds the Writer's append mutex across the entire capture→rename→fd-swap: (a) append the side buffer onto `.tmp` and fsync it, so the fresh file is complete *before* it becomes canonical; (b) rename; (c) fsync dir; (d) swap the fd off the old inode. Because the lock is held throughout, no append can interleave and strand data. Now the canonical path resolves to a *complete* file at every instant — old file (has every acked append) pre-rename, new file (snapshot + side buffer, fsynced pre-rename) post-rename. No unlinked-only window exists.

## Why it mattered

- **Iron law: no fix without a reproduced root cause.** I couldn't reproduce the SIGKILL flake locally, so I proved the mechanism a different way — a deterministic in-process regression test. `TestRewriter_CrashAfterRename_NewFileComplete` installs a test-only `afterRenameHook`, makes one acked append land in the side buffer during a paused snapshot, and at the rename instant replays the canonical file exactly as a restart would. On the old ordering it recovers only `[SET snap S]` and fails with "acked append 'live=L' absent … a crash in the [rename, drain] window would lose it"; on the fixed ordering it passes. Deterministic, no subprocess, and it fails loudly if anyone reintroduces the rename-before-drain order.
- **The scientific loop beat the un-reproducible flake.** Rather than chase a 1-in-many SIGKILL timing on CI, model the exact instant a crash would expose (post-rename) and assert the on-disk invariant there. Turns a probabilistic crash test into a deterministic ordering assertion.

## Code / measurement

- `TestRewriter_CrashAfterRename_NewFileComplete`: FAIL on `860802f` (pre-fix), PASS on `901806d` (post-fix).
- Previously-flaky `TestAOF_CrashInjection_DuringRewrite`: **0/30** failures under `-race` after the fix (was intermittently failing on CI).
- `make ci` green: golangci-lint 0 issues; `go test -race -timeout 5m ./...` — all packages pass.
- API delta: Writer gains `FinalizeRewrite(tmpPath, canonicalPath)` + private `swapTo`, drops `DrainAndSwap`; `sideBuf` and `w.mu` unchanged. `Rewrite` shrinks — rename/dir-fsync/fd-swap all moved into the locked `FinalizeRewrite`.

## Follow-ups

- **M16 gate item can be struck.** The roadmap's "fix or quarantine the flaky `TestAOF_CrashInjection_DuringRewrite`" release-hardening item is now *fixed*, not quarantined — update the M16 checklist at release time.
- **`FinalizeRewrite` holds the append lock across a few fsyncs.** Bounded by side-buffer size; sub-millisecond for v1 working sets and the correct price for closing the window (Redis blocks at the same swap point). If a real workload ever shows a latency blip during rewrite, the side buffer is the thing to spill to disk (already flagged in ADR-0005 as post-v1).
- **Audit the other crash-invariant tables** (M3 AOF, M11 typed crash) for any row that documents acceptable loss — the same "the ADR rationalized it" trap could hide elsewhere. None found on a first read, but worth a deliberate pass in the M16 security/durability review.

## Blog-worthy?

Strong yes, and it's the honest kind: *"the flaky test was telling the truth."* A crash test that fails 1-in-many on CI is easy to dismiss as flaky infra and quarantine — the roadmap even pre-authorized quarantining it. Reading the failure signature (always the *earliest* live writes) turned "flaky test" into "real durability bug," and the fix was a four-line reordering that the LLD had specified all along but the code and a post-hoc ADR had drifted from. Pairs with a sharp lesson: **an ADR written after the code can launder a bug into a decision** — the crash-invariant table literally had a data-loss row marked acceptable.
