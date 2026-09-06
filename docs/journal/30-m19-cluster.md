# 30 — M19: the distributed core, and the friction that came back twice

**Date:** 2026-09-06
**Context:** M19 complete on `feat/cluster` — the distributed core, shipped as three
risk-ordered slices (PR pending; committed atomically off this branch). M19.1 wired a
real odd-N cluster over ToyRaft `pkg/transport/http` + `pkg/storage/file`; M19.2 proved
failover + partition correctness under injected network faults; M19.3 proved the register
is linearizable under concurrent load. Standalone and single-node paths stay byte-identical
to M18. Depends on ToyRaft `v1.0.0-rc.2`.

## Decision / surprise

**The same friction — `internal/clock`-typed, required, un-constructible `Clock` — bit
three times, on an escalating path.** M18 flagged it as *medium* on `inproc.Hub` (worked
around with a no-op transport, so it never hurt). M19.1 hit the **same class** on the
load-bearing `pkg/transport/http` — a *high*, because a real cluster cannot exist without a
transport. That one got fixed upstream: ToyRaft `rc.2` defaults a nil `http.Config.Clock`
to the real clock (parity with `raft.Config`). Then M19.2 went *back* to `inproc.Hub` for
its `Partition`/`Heal` chaos knobs — the natural home for failover testing, the exact
surface ToyRaft's own `figure8_test.go` uses — and hit the **identical wall a third time**:
`inproc` never got the nil-Clock fix. A friction you file but don't fix returns on the
critical path.

The turn: instead of waiting on an `rc.3`, M19.2 wrapped the *real* HTTP transport. A
~40-line `chaosTransport` implements `raft.Transport`, delegates to `http`, and drops
messages crossing a mutable partition cut — outbound in `Send`, inbound in the registered
`step` callback — toggled by a shared `partitionState`. It tests partitions over the
**production** transport, which is *better* than an in-process hub, race-clean, with zero
production change (the harness builds `*cluster.Node` directly, in-package).

## Why it mattered

- **A test failure that was the library being correct.** The first `TestLeaderKillNoAckedWriteLoss`
  run failed: "majority missing acked writes." Not a bug — ToyRaft's REPL-06 / Figure-8 guard
  (`commit.go`): a new leader **never** commits prior-term entries by replica count; the acked
  prefix flushes only once a *current-term* entry commits above it. So the 20 acked INCRs sat
  replicated-but-uncommitted until the new leader wrote once in its own term. Fix was in the
  *test*, not the code: drive one post-failover write, then assert the whole prefix survived.
  Reading `commit.go` before "fixing" saved a wild goose chase.
- **Split-brain is "no two leaders at the same *term*," not "≤1 leader."** An isolated old
  leader keeps `Role==Leader` at its stale term until it sees a higher one — momentarily two
  nodes report Leader. That's legal. The guard asserts uniqueness *per term*; a cluster-wide
  "only one Leader right now" scan would false-positive on every partition.
- **`-race` is why M19.3 is in-process, not go-redis-driven.** The roadmap offered "Porcupine
  *or* a go-redis recorder." A subprocess/go-redis server runs the server code *outside* the
  race detector — so the exit bar ("green under `-race`") quietly wouldn't cover consensus at
  all. Driving `cluster.Node` in one process keeps the whole propose→commit→apply path under
  `-race`. Fidelity lost (no RESP/dispatch) is already covered by the M19.1 go-redis e2e.
- **Every harness carries a non-vacuous check.** M19.2 inverts an assertion (orphan write
  *survives*) → must fail. M19.3 corrupts INCR's recorded output (`+1`) → Porcupine returns
  `Illegal`. A green linearizability test that can't go red is just an expensive `return true`.

## Code or measurement

- Chaos transport + failover suite: `internal/cluster/failover_test.go` — `chaosTransport`
  (Send/Register drop by cut), `newHubCluster`, `TestLeaderKillNoAckedWriteLoss` /
  `TestPartitionHealReconciliation` / `TestNoSplitBrainDoubleLeader`. All under `-race`.
- Linearizability: `internal/cluster/linearizability_test.go` — single-integer-register
  `porcupine.Model`, 4 clients × 40 ops on the leader (writes via `Node.Propose`, reads from
  the leader's applied store), `CheckOperations` at N=3 and N=5. `porcupine v1.0.3` promoted
  to a direct `go.mod` require.
- Full `./internal/cluster/...` green under `-race` (~18s: happy-path + failover + N=3/5).
- ADR-0019 amended (M19.2 subsection: chaos-wrapped-transport contract + why not subprocess/inproc).
  Dogfood findings appended to `docs/TOYRAFT-MIGRATION-REPORT.md` (M19 section).

## Blog-worthy?

The spine: **"the friction that came back twice."** One un-constructible `Clock`, flagged as
minor at M18, escalates to a hard blocker on the load-bearing transport (M19.1, fixed
upstream), then blocks the *test* harness a release later because the fix wasn't generalized
to `inproc` (M19.2). The payoff is a nicer design forced by the block — testing partitions
over the real transport instead of a simulator. Second nugget: **the test that failed because
the library was right** (REPL-06 current-term commit), a clean lesson in reading the
dependency's invariants before trusting your assertion. Meta-point, now proven twice over:
dogfooding a pre-1.0 consensus library pays out entirely at the *embedding-ergonomics* seams
(constructibility, result delivery) — the consensus core itself was flawless across happy-path,
failover, partition, and linearizability.
