# 34 — M23: the release milestone that was mostly docs, one guard, and an honest bench

**Date:** 2026-09-10
**Context:** M23 merged to `main` via PR #49 (rebase), base `995d932`, on `feat/release-v3`.
The v3.0.0 release-prep milestone: bench + finalized dogfood report + docs + the one piece of
new code the whole cycle needed — a raft-bind security guard. No new ADR body; ADR-0019 gained
an M23 amendment.

## Decision / surprise

**The only new code in a "release" milestone was a security guard.** M18–M22 shipped the whole
replicated feature surface. M23's job was to make it *shippable*: document it, benchmark it,
and close the one real gap — the client bind was protected since M15, but the **Raft peer bind
was not**. A multi-node cluster would happily bind its plaintext, unauthenticated peer transport
to `0.0.0.0` with zero friction.

`checkRaftBind` closes that. It sits next to `checkProtectedMode` in `server.New`, but is
**deliberately independent of `-protected-mode`**: the client guard is satisfied by auth *or*
TLS, because a public client bind *can* be made safe. The peer transport has neither — ToyRaft's
threat model is a trusted network — so the only safe posture is loopback, and the sole override
is an explicit trusted-network acknowledgement (`-raft-insecure`). Folding it into
`-protected-mode` would have implied auth/TLS could rescue the peer plane; it can't.

## Why it mattered

- **The bench is the honest part.** Cluster `SET` runs **~83 rps, p50 ~600 ms** on the local
  3-node stack — three orders of magnitude below standalone. That is not a regression to hide;
  it is the literal cost of `Propose → HTTP-replicate to peers → commit → Apply → fsync(always)`
  with no client pipelining. The write `n` in `BENCHMARKS.md` is small (5 000 / 3 000) on
  purpose: at 83 rps a 100 000-op run is ~20 minutes. Leader reads are local and stay in the
  standalone band (~35 k rps, sub-ms). Recorded the delta and the *why*, not a flattering
  headline — this is a correctness demo, not a throughput target.

- **The bench forced a real feature: election-timeout flags.** The first bench attempt kept
  aborting with `NOTLEADER` — leadership flapped under write load because the containers ran
  ToyRaft's default timings with no CLI knob to widen them. `server.Config` already carried
  `ElectionTimeout{Min,Max}` + `HeartbeatInterval` (added in M20 for the routing harness), but
  they weren't wired to flags. Added `-election-timeout-min/-max` + `-heartbeat-interval`
  (0 = ToyRaft default, so M19 behaviour is unchanged), set them wide in the compose, and the
  leader held. The bench blocker turned into a shipped operator knob.

- **`deploy/cluster` exercises the guard for real.** The compose binds each raft port to a
  service-DNS name (non-loopback), so the stack *only* comes up because it passes
  `-raft-insecure` — the guard's intended trusted-network path (compose bridge), documented
  inline and in SECURITY. The one caveat worth writing down: `NOTLEADER` hints are
  docker-internal names a host `redis-cli` can't resolve, so the smoke runs container-side or
  targets the leader port directly.

## What's next

**Two external gates remain before the `v3.0.0` tag**, neither doable from this repo:
1. **ToyRaft `rc.2 → v1.0.0` bump.** `go list -m -versions` shows only `rc.1`/`rc.2`; upstream
   hasn't tagged `v1.0.0`. The finalized `TOYRAFT-MIGRATION-REPORT.md` is the dogfood feedback
   that gates it — the reciprocal half of the mutual unblock. Two open asks carried upstream:
   generalize the `rc.2` nil-`Clock` default to `inproc.HubConfig.Clock` (closes the
   constructibility class that forced the `chaosTransport`), and document/strip the leader
   self-ID in `Status().MatchIndex`.
2. **The `v3.0.0` tag** — post-merge, once the dependency bump lands.

No 🐞 correctness defect surfaced in ToyRaft across the entire M18–M23 arc.
