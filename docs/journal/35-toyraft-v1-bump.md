# 35 — ToyRaft v1.0.0 bump, and the benchmark that looked like a regression

**Date:** 2026-09-19
**Context:** PR #51 rebase-merged to `main` (`b9c69ac`..`32bb9e2`), base `316c875`, on
`feat/toyraft-v1`. Closes M23's first external gate: ToyRaft is now tagged `v1.0.0` upstream,
so the dependency moves `v1.0.0-rc.2 → v1.0.0`. Also adds an in-process throughput bench. All
8 CI checks green (build, lint, chaos-smoke, test ubuntu/macos × go 1.25/1.26, GitGuardian).
Only the `v3.0.0` tag remains.

## Decision / surprise

**The bump's one breaking change was silent until it wasn't.** `raft.Node.Propose` now returns
a *fourth* value — the `StateMachine.Apply` result — so an embedder can recover an Apply-computed
value without an out-of-band registry. toykv already had that registry (`sm.TakeResult(idx)`), so
the fix was one line: `idx, _, _, err := n.raft.Propose(...)`. Kept the registry, ignored the new
return. Minimal diff beats a "clean" refactor that touches the reply-capture contract.

**Both dogfood asks resolved upstream — and we changed nothing for them.** v1.0.0 added
`raft.Node.NodeID()` (FRICTION-06) and a nil-defaulting `inproc.HubConfig.Clock` (FRICTION-07).
But there's no `ReplicaMatchIndex()` accessor, so the `MatchIndex` self-filter stays; and the
failover suite keeps `chaosTransport` deliberately — it wraps the *real HTTP* transport, so the
tests exercise the on-wire path a deployed cluster uses, not an in-process substitute. Resolved
upstream ≠ code churn downstream.

## Why it mattered

**The benchmark scared us, then taught us.** First replicated run: **p50 ~53 ms**, ~1 k msg/s
vs ~120 k standalone. Looked like toykv was 40× slower than the ToyMQ sibling (p50 1.3 ms) on
the *same* ToyRaft v1.0.0. Chased it:

- **Disk?** Put `RaftDir` on tmpfs (`/dev/shm`) — no change (53 → 53 ms). Not fsync-bound.
- **Heartbeat?** Dropped `-heartbeat-interval` 300 ms → 50 ms (ToyRaft default; its driver tick
  period *equals* the heartbeat) — barely moved (53 → 46 ms). So flush-on-Propose works; not
  tick-gated.
- **Concurrency.** That was it. Both brokers commit through **one serial Raft log**, so
  `latency ≈ queue-depth × commit-time`. My bench used `c=50`; ToyMQ used `--producers 4`. At
  ToyMQ's exact load (`c=4`, `n=2000`, 256 B), toykv replicated collapses to **p50 2.32 ms,
  1550 msg/s** — same order as ToyMQ's 2485 / 1.3 ms. No anomaly.

**Two numbers that read as contradictions but aren't:**
- ToyMQ's famous "**~100×**" is *not* replicated-vs-standalone — it's ToyRaft `rc.3 → v1.0.0`
  flush-on-`Propose` (FRICTION-08) removing a ~150 ms heartbeat-tick commit floor
  (`~150 ms → ~1.3 ms`). toykv inherits that same win on the bump.
- Replicated is **still** slower than standalone (123 k → 1 k), and always will be — Raft buys
  fault tolerance (no acked-write loss, leader failover, quorum durability), never speed. Every
  consensus system pays one round-trip per write. ToyMQ's own table shows the same (26 k → 2.5 k).

**Bench design outcome:** report replicated *throughput* from the `c=50` run and replicated
*latency* from the `c=4` run — reading p50 off the high-concurrency table overstates per-op cost
~20×. `BENCHMARKS.md` now carries both tables + the ToyMQ cross-reference, so the next reader
doesn't re-run the same three-hour investigation.

## Code / measurement

```
# default (c=50, n=20000, 64B) — throughput view
standalone        123496 msg/s  p50 205µs   p95 1.25ms  p99 3.36ms
replicated 3-node   1002 msg/s  p50 50.09ms p95 75.19ms p99 91.49ms

# ToyMQ-matched (c=4, n=2000, 256B) — latency view
standalone         41887 msg/s  p50 77µs    p95 166µs   p99 399µs
replicated 3-node   1550 msg/s  p50 2.32ms  p95 3.86ms  p99 4.66ms
```

`test/bench/throughput_test.go`, build-tagged `bench` (excluded from `go test ./...`), run via
`make bench-throughput`. Numbers vary run-to-run on a thermally-throttling mobile i7.

## Blog-worthy?

Strong material: **"the benchmark that looked like a regression."** The debugging arc —
disk → heartbeat → concurrency — is a clean worked example of *reading a benchmark wrong*.
And the "replication should get faster, right?" misconception is worth a whole section:
Raft is a fault-tolerance tax, not a speed-up, and the "100×" everyone quotes is a
different denominator (an upstream fix un-breaking a tick floor), not consensus beating memory.

## What's next

- **Cut `v3.0.0`** (post-merge): CHANGELOG + version bump + annotated tag + goreleaser +
  GitHub release via `code_assist:release`. The last remaining M23 gate.
- Then the v3.x backlog opens: compaction on ToyRaft `v2` snapshots, linearizable reads on
  ReadIndex, sets/sorted-sets/pub-sub on the replicated `Apply` path.
