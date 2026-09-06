# 29 — M18: embedding ToyRaft, and the API gap that shaped the whole seam

**Date:** 2026-09-05
**Context:** M18 merged — PR [#43](https://github.com/prajwalmahajan101/toykv/pull/43),
rebase-merged onto `main` as `91bdedd..387ebb2` (5 commits). First milestone of the v3
distributed arc: embed `toyraft v1.0.0-rc.1` behind an opt-in `-replicate`, single node,
`Propose → Apply` for every mutating command, standalone stays byte-identical to v2.

## Decision / surprise

The plan said "Propose returns the reply to the client." **The frozen API doesn't do that.**
`Node.Propose(ctx, data) (Index, Term, error)` blocks until *applied* (SC3) and forwards
`Apply`'s **error**, but the non-error `result any` that `StateMachine.Apply` returns is
**dropped at the Node boundary** — the caller only gets `(Index, Term, error)`. The reference
`toyraftd` never noticed because it reads GET back from the applied map on the leader; toykv
can't do that for INCR/LPUSH/DEL, whose reply *is* the outcome computed inside `Apply` and
can't be reconstructed by a follow-up read.

Fix that shaped the package: `StateMachine.Apply` stashes each reply in a
`map[raft.Index]resp.Value`, and the `Node.Propose` wrapper reads it back by the index Propose
returned. Race-free **only** because Propose returns after Apply has run (SC3) — the stash is
always present. That one gap is the reason `internal/cluster` has a StateMachine that owns a
results map instead of a thin pass-through.

## Why it mattered

- **Read the real API before trusting the plan's verbs.** The whole reply-capture design is a
  workaround for a one-line API shape I'd assumed away in planning. Verifying `Propose`'s
  actual signature against the module cache (not the local toyraft checkout) caught it before
  I wrote a line of the server wiring.
- **The seam stayed a single point despite the workaround.** Propose lives *inside* `dispatch`
  gated on `s.replicated && h.mutating && !cs.applying`; `Apply` re-enters `dispatch` with
  `applying:true`. Handlers never learned about Raft, and the same `dispatch` serves live
  traffic, AOF replay, and Apply. Standalone never takes the branch → byte-identical to v2,
  proven by running the *existing* `typedRoundTrip` e2e unchanged against a `-replicate` server.
- **`inproc.Hub` is a trap for external consumers.** Its `HubConfig.Clock` is required and typed
  on toyraft's `internal/clock` — un-importable from outside the module, no public constructor.
  So the "obvious" single-node transport is unbuildable. A single node fans out no messages
  (ELEC-04 self-vote quorum), so a ~5-line no-op `raft.Transport` is both correct and simpler.
  Both frictions went into `docs/TOYRAFT-MIGRATION-REPORT.md` as the M18 dogfood findings.
- **AOF exactly-once fell out of the gate, not extra code.** The leader returns from the propose
  branch *before* touching the handler, so it never appends; `Apply` runs with `s.aof` live and
  appends once. The M3/M11 crash-injection suite, re-run with the state machine in the loop
  (`TestAOF_CrashInjection_Replicated`), stayed green — every acked SET survived SIGKILL.

## Code or measurement

- `Propose` reply capture: `internal/cluster/statemachine.go` (`results` map + `TakeResult`),
  `internal/cluster/node.go` (`Propose` fetches by returned index).
- Propose gate: `internal/server/dispatch.go`; `resolveNondeterministic` +
  `applyReplicated`: `internal/server/replicate.go`.
- Determinism: SET EX/PX/EXAT → PXAT abs, EXPIRE/PEXPIRE → PEXPIREAT abs at propose time;
  NX/XX preserved, malformed passed through. Cross-clock test proves a resolved envelope
  applied under a +50s-skewed clock yields identical expiry.
- CI: all 8 checks green (build, lint, chaos-smoke, test on ubuntu+macos × go 1.25/1.26).
- ADR-0018 records the four sub-decisions; single-clock lazy-expiry divergence is explicitly
  deferred to M19 (documented, not fixed).

## Blog-worthy?

Strong one: **"the milestone the frozen API rewrote."** The narrative is planning said
`Propose` returns the reply, the real signature discards it, and that single gap is why the
state machine owns an index→reply map — a clean illustration of *verify the dependency's real
shape before designing against your assumption of it*. Bonus nugget: the no-op-transport
insight (a single-node Raft cluster needs no transport at all because self-vote is already a
quorum) and the meta-point that dogfooding a pre-1.0 library is most valuable exactly at these
embedding-ergonomics seams, not in the consensus core (which was flawless).
