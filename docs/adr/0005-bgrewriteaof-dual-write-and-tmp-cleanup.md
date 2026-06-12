# ADR-0005: BGREWRITEAOF — dual-write side buffer + atomic-rename swap

- Status: Accepted
- Date: 2026-06-12
- Milestone: M5
- PR: (to be filled on merge — PR B of the M5 series)

## Context

M5 introduces `BGREWRITEAOF` (LLD §4.4): the AOF is rewritten in place by snapshotting live state to a fresh file and atomically swapping it in. The rewrite shrinks the log after churn (`SET k v1; SET k v2; DEL other` collapses to `SET k v2`) and is the only mechanism v1 ships for bounded log growth.

Two correctness constraints make the design non-obvious:

1. **The M3 durability contract must hold throughout the rewrite.** Every acked `SET` must survive a SIGKILL at *any* point during the rewrite. We cannot stop appending mid-rewrite — clients are still writing.
2. **Exactly one canonical file at every instant.** A crash must never leave `toykv.aof` half-written. The roadmap risk table calls this out at "High" severity for M5.

Two reasonable strategies present themselves:

- **(A) Buffer-only.** During rewrite, redirect live appends to an in-memory (or separate-file) buffer; the canonical AOF stops growing until the swap. Simpler swap (no concurrent writer to the old file) but the canonical file is *stale* between BeginRewrite and Swap — a crash there loses everything the buffer captured, even though those writes were acked.
- **(B) Dual-write.** Live appends go to both the current canonical file *and* the side buffer. Canonical stays durable and consistent throughout; the buffer's job is just to carry the post-snapshot writes onto the new file after the rename.

## Decision

**Adopt strategy (B): dual-write side buffer + atomic rename + post-rename drain.**

Single-sentence: *the AOF Writer keeps appending to the canonical file unchanged, while mirroring the same RESP bytes into an in-memory side buffer that the Rewriter drains onto the new file after the atomic rename swaps it into place.*

Concretely (see `internal/aof/writer.go` and `internal/aof/rewriter.go`):

```
Rewriter.Rewrite:
  1. Writer.BeginRewrite()              ─ side buffer armed
  2. cmds := snapshotCallback()         ─ takes brief Store.Lock
  3. Open dir/toykv.aof.tmp, write v2 header + cmds, fsync, close
                                          (concurrent Appends → old file + side buffer)
  4. rename(toykv.aof.tmp, toykv.aof)   ─ atomic on POSIX same-fs
  5. fsync(dir)                          ─ rename durability
  6. Writer.DrainAndSwap(canonical)     ─ close old fd, open new file,
                                          replay side buffer onto it, fsync
```

Crash invariants at each step:

| Killed during | Canonical contents on disk | Side buffer | Restart behaviour |
|---|---|---|---|
| step 1 (after `BeginRewrite`) | unchanged old file | gone (in-memory) | normal v1 replay |
| step 3 (writing `.tmp`) | unchanged old file | gone | startup unlinks stale `.tmp`; normal replay |
| step 4 (before rename completes) | unchanged old file | gone | same |
| step 4 (after rename completes) | new file (snapshot only) | gone | replay new file — but any post-snapshot writes that were also in the side buffer never reached disk |
| step 5–6 | new file (snapshot only) | gone | same |

The "post-rename, pre-DrainAndSwap" window is the only one where acked-but-not-replayable writes can exist. We close it by holding `appendIfLive` synchronous with the AOF append under `FsyncAlways` *for the old file* (already true) — a kill during that window loses the same writes a kill of the unmodified M3 code would lose, no worse. The dual-write design specifically does **not** make this window worse than the M3 baseline.

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

**Neutral.**
- The Writer grows three new methods (`BeginRewrite`, `DrainAndSwap`, `AbortRewrite`) and one new field (`sideBuf *bytes.Buffer`). The mutex is the existing `w.mu` — no new locks.
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
