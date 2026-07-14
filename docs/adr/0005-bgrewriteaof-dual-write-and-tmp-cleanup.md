# ADR-0005: BGREWRITEAOF — dual-write side buffer + atomic-rename swap

- Status: Accepted (swap ordering corrected 2026-07-15 — see **Correction** below)
- Date: 2026-06-12
- Milestone: M5
- PR: (to be filled on merge — PR B of the M5 series)

> **Correction (M12, 2026-07-15).** The original design drained the side
> buffer onto the new file *after* the rename. That left a real (narrow)
> durability window: an acked write made during the rewrite lived only in
> the old inode (fsynced) and the in-memory side buffer, and the rename
> unlinked that old inode *before* the new file received those bytes — so
> a SIGKILL in the `[rename, drain]` window lost acked writes. The
> milestone-owned crash test caught this flakily (the window is small; it
> widens under `-race` on CI). Fixed by reordering: the side buffer is now
> folded into the `.tmp` and fsynced **before** the rename, with the whole
> capture→rename→fd-swap held under the Writer's append lock so no append
> can interleave. `DrainAndSwap` (post-rename) is replaced by
> `FinalizeRewrite` (pre-rename fold). The strategy (B, dual-write) is
> unchanged; only the swap ordering is corrected. The sections below are
> updated to match; the superseded reasoning is called out inline.

## Context

M5 introduces `BGREWRITEAOF` (LLD §4.4): the AOF is rewritten in place by snapshotting live state to a fresh file and atomically swapping it in. The rewrite shrinks the log after churn (`SET k v1; SET k v2; DEL other` collapses to `SET k v2`) and is the only mechanism v1 ships for bounded log growth.

Two correctness constraints make the design non-obvious:

1. **The M3 durability contract must hold throughout the rewrite.** Every acked `SET` must survive a SIGKILL at *any* point during the rewrite. We cannot stop appending mid-rewrite — clients are still writing.
2. **Exactly one canonical file at every instant.** A crash must never leave `toykv.aof` half-written. The roadmap risk table calls this out at "High" severity for M5.

Two reasonable strategies present themselves:

- **(A) Buffer-only.** During rewrite, redirect live appends to an in-memory (or separate-file) buffer; the canonical AOF stops growing until the swap. Simpler swap (no concurrent writer to the old file) but the canonical file is *stale* between BeginRewrite and Swap — a crash there loses everything the buffer captured, even though those writes were acked.
- **(B) Dual-write.** Live appends go to both the current canonical file *and* the side buffer. Canonical stays durable and consistent throughout; the buffer's job is just to carry the post-snapshot writes onto the new file after the rename.

## Decision

**Adopt strategy (B): dual-write side buffer + fold-into-tmp + atomic rename.**

Single-sentence: *the AOF Writer keeps appending to the canonical file unchanged, while mirroring the same RESP bytes into an in-memory side buffer that the Rewriter folds onto the fresh `.tmp` file — and fsyncs — before the atomic rename swaps it into place, so the file that becomes canonical is already complete.*

Concretely (see `internal/aof/writer.go` and `internal/aof/rewriter.go`):

```
Rewriter.Rewrite:
  1. Writer.BeginRewrite()              ─ side buffer armed
  2. cmds := snapshotCallback()         ─ takes brief Store.Lock
  3. Open dir/toykv.aof.tmp, write v2 header + cmds, fsync, close
                                          (concurrent Appends → old file + side buffer)
  4. Writer.FinalizeRewrite(tmp, canonical) ─ ALL under the Writer append lock:
       a. append side buffer onto .tmp, fsync .tmp   (fresh file now complete)
       b. rename(.tmp, toykv.aof)      ─ atomic on POSIX same-fs
       c. fsync(dir)                    ─ rename durability
       d. close old fd, open new file   ─ later appends target the new file
```

The lock in step 4 blocks Appends for the duration of the swap, so the side
buffer captured in (a) is final — nothing lands in the old inode after it.

Crash invariants at each step:

| Killed during | Canonical contents on disk | Restart behaviour |
|---|---|---|
| step 1 (after `BeginRewrite`) | unchanged old file (+ live appends) | normal replay — complete |
| step 3 (writing `.tmp`) | unchanged old file (+ live appends) | startup unlinks stale `.tmp`; replay — complete |
| step 4a (folding side buffer into `.tmp`) | unchanged old file (+ live appends) | startup unlinks stale `.tmp`; replay old file — complete |
| step 4b (before rename completes) | unchanged old file (+ live appends) | same — complete |
| step 4b (after rename completes) | new file (snapshot **+ side buffer**) | replay new file — complete |
| step 4c–4d | new file (snapshot + side buffer) | replay new file — complete |

At **every** step the canonical path resolves to a *complete* file: pre-rename it is the old file, which received every acked append (fsynced under `FsyncAlways`); post-rename it is the new file, which received the side buffer and was fsynced *before* the rename. There is no window where an acked write exists only in an unlinked inode.

> **Superseded reasoning (original M5 design).** The first cut renamed *before* draining the side buffer (`DrainAndSwap`), and this ADR argued the resulting `[rename, drain]` window was "no worse than the M3 baseline." That was wrong: the rename unlinks the old inode, so acked writes that lived only there plus the in-memory buffer were lost on a crash in that window — a regression the M3 single-file path never had. The reorder above (fold-before-rename, whole swap under the append lock) closes it; the deterministic guard is `TestRewriter_CrashAfterRename_NewFileComplete`.

`.tmp` is **never canonical**. `aof.Open` unconditionally `os.Remove`s any `dir/toykv.aof.tmp` on startup — a leftover `.tmp` is always garbage from a previous crashed rewrite.

## Consequences

**Positive.**
- Canonical file remains continuously valid. The hardest invariant ("never a half-written `toykv.aof`") falls out of the design rather than being defended by careful crash-test choreography.
- Append fast path is unchanged when no rewrite is in flight — `sideBuf == nil` short-circuits.
- Recovery is trivial: replay the canonical file. There is no rewrite-state to reconstruct on startup.
- The rewriter is fully decoupled from the dispatcher and the store — its only inputs are the side-buffer-aware Writer and a snapshot callback. PR A's `Store.Snapshot()` is what makes the callback honest.

**Negative.**
- Memory overhead during rewrite = bytes of live appends from start-of-rewrite to drain. For toykv's single-RWMutex single-process design this is bounded by realistic client throughput × snapshot duration (sub-second for v1 working-set sizes). For larger datasets the side buffer would need to spill to disk; flagged as M5 follow-up work, not v1 scope.
- Dual-write means each Append during a rewrite encodes RESP into a scratch buffer then copies into both `bw` and `sideBuf`. Allocation per append is one `bytes.Buffer`; measured cost is in the microseconds — acceptable for the duration of a rewrite.
- The pre-swap fsync on the old file's mid-rewrite appends is wasted I/O — those bytes are about to be discarded with the unlinked inode. Accepted as the cost of "canonical-stays-valid-throughout."
- `FinalizeRewrite` holds the append lock across the fold+rename+fd-swap, so client appends block for that span (reopen `.tmp`, write side buffer, fsync, rename, fsync dir, reopen canonical). Bounded by side-buffer size and a few fsyncs — sub-millisecond for v1 working sets, and the correct price for closing the durability window. Redis blocks briefly at the same swap point for the same reason.

**Neutral.**
- The Writer grows three rewrite methods (`BeginRewrite`, `FinalizeRewrite`, `AbortRewrite`) plus a private `swapTo` helper and one new field (`sideBuf *bytes.Buffer`). The mutex is the existing `w.mu` — no new locks.
- Snapshot semantics match `Keys` / `Get` at the snapshot instant: expired entries are evicted under the store's write lock as the snapshot materialises, so the rewrite cannot resurrect them.

## Alternatives considered

- **Buffer-only (strategy A above).** Rejected because the canonical file goes stale for the duration of the rewrite, breaking M3's per-command durability contract. The simplification isn't worth a regression.
- **File-backed side buffer (`.aof.rewrite-buf`).** Equivalent crash story to the in-memory buffer (it is also unrenamed-into-canonical). Adds another file-state machine without v1 needing it. Reserve for the day a real workload demands it.
- **Stop accepting writes during rewrite.** Trivial correctness; unacceptable availability. Real Redis doesn't do this for the same reason.
- **Copy-on-write fork (Redis's approach).** Requires fork-safe data structures and POSIX `fork()` semantics this Go binary doesn't get for free. Out of scope for v1.

## References

- HLD.md §4 (AOF)
- LLD.md §4.4 (Rewriter)
- ADR-0003 (AOF format and fsync policy) — establishes "on-disk format = wire format" and the durability contract this ADR must preserve
- ADR-0004 (TTL canonical PXAT) — establishes that the snapshot can render every entry as a single `SET k v [PXAT ms]` line
- Related ADRs: [[0003]], [[0004]]
