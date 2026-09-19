# 36 — v3.0.0 docs sync: README, roadmap scorecard, and GitHub metadata

**Date:** 2026-09-19
**Context:** PR #53 rebase-merged to `main` (`1e6f65a`..`e05b504`), base `776b67f`, on
`docs/v3-readme-metadata`. Pure documentation follow-up to the `v3.0.0` release cut — no code,
no behaviour change. Closes the gap where the top-of-README status still read the v2 story after
the cluster work had already shipped.

## Decision / surprise

**The README described a product two majors behind its own body.** The status line still said
`M0–M17 shipped — v2.0.0` and the "What it isn't" block still claimed *no auth, no TLS,
single-node* — every one of those already false by the time v3 tagged. The cluster/replication
prose (M18–M23) had been added incrementally lower down, but nobody re-read the header. Fixed:

- Status → `M0–M23 shipped — v3.0.0`, with the "standalone stays byte-identical to v2,
  replication opt-in behind `-replicate`" framing that keeps the no-forced-migration promise front
  and centre.
- Tagline gains *replicated* and the embedded-ToyRaft cluster line.
- "What it isn't" swaps three stale disclaimers for honest v3 limits: correctness demo not a
  throughput target; follower reads non-linearizable (ToyRaft v1 has no ReadIndex). The truthful
  limit is more useful than the obsolete one.
- Install version pin `v1.0.0 → v3.0.0`.

**Roadmap got a Redis-parity scorecard.** One consolidated note pointing at where each known gap
vs real Redis is tracked (pipelining + sharded lock → two new Performance rows; RDB → Persistence
row; sorted sets / pub-sub → v3.x backlog). Cheaper to answer "how does it measure up" once, in
writing, than in every future conversation.

## Why it mattered

Documentation drift is the quiet failure mode of an incrementally-shipped roadmap: the body keeps
pace commit-by-commit, the header doesn't, and the first thing a new reader sees is the most stale.
A docs-only PR is the right blast radius for the fix — no CI risk, rebase-merges clean.

## Code / measurement

Two atomic commits, docs-only:

```
1e6f65a docs(readme): sync status, tagline, and limits for v3.0.0 cluster release
e05b504 docs(roadmap): add Redis-parity scorecard and pipelining/sharded-lock gap rows
```

GitHub repo metadata refreshed out-of-band via `gh repo edit`: description rewritten for v3
(RESP2/RESP3, AOF, TTL, AUTH+TLS, OTel, Raft cluster, CLI+TUI), and 15 topics added (was empty) —
`key-value-store`, `raft`, `consensus`, `distributed-systems`, `replication`, `go`, `redis`,
`resp-protocol`, `aof`, `opentelemetry`, `tui`, `cli`, …

## Follow-ups

- The two new Performance roadmap rows (pipelined `redis-benchmark -P`, sharded store mutex) are
  the next honest throughput levers — deferred, not committed.
