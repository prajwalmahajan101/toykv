# 07 — M5 PR A merged (Store.Snapshot)

**Date:** 2026-06-12
**Merge:** PR #13 (`feat/m5-store-snapshot` → `main`), rebase fast-forward, 1 commit (`a53bf79`).
**Tag:** none yet — M5 tag (`m5`) lands at PR C close.

## Decision / surprise

The smallest PR of the M5 series and the one I was most tempted to skip and roll into PR B "for atomicity." Resisted, because the snapshot interface is the actual decoupling point between store and AOF — bundling it with the rewriter would have hidden the *contract*: "give me a stable, copied view of live state, and evict expired entries while you're under the lock anyway."

The interesting micro-decision: Snapshot takes a **write** lock, not a read lock. The rationale is that any read-lock walk would either (a) leak expired entries the rewriter would then write into the new AOF — defeating compaction's purpose — or (b) do a lock-upgrade dance inside the iteration, which the lazy-Get path already documents as the awkward part of the lazy-expiry design. Taking the write lock once, evicting inline, and returning a fully-materialised slice is the boring correct answer.

## Why it mattered

It pre-pays for the rewriter's "consistent snapshot" invariant. PR B's Rewriter doesn't need to think about expiry at all — `Snapshot()` already filtered. That keeps the rewriter's surface area honest: it serialises whatever the store hands it.

## Next

PR B: aof.Rewriter + Writer side-buffer mode + `.tmp` cleanup on open + ADR-0005.
