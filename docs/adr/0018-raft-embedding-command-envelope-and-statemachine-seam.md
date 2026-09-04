# ADR-0018: Raft embedding, command envelope & StateMachine seam

- Status: Accepted
- Date: 2026-09-04
- Milestone: M18
- PR: _(feat/raft-embed)_

## Context

v3.0 makes toykv a replicated cluster by embedding **ToyRaft**
(`github.com/prajwalmahajan101/toyraft v1.0.0-rc.1`, frozen public API). M18 is
the foundational, highest-blast-radius milestone: it proves the state-machine
seam on a **single node** before any distributed complexity (multi-node
transport, election, and file-backed Raft storage are M19). toykv's running
cluster is also the dogfood gate that lets ToyRaft tag its own `v1.0.0`.

toykv already mutates at a single `dispatch()` chokepoint: **mutate store →
append AOF → reply**. Replicated, a mutating command must become **Propose →
Raft replicates → StateMachine.Apply mutates store + appends AOF → reply**,
landing identically to today with AOF durability unchanged. Constraints the
milestone imposed on itself:

- **Standalone stays byte-identical to v2.** Replication is opt-in
  (`-replicate`); the non-replicated path must not change at all.
- **Determinism.** `StateMachine.Apply` runs on every replica from a single
  goroutine and must be a pure function of the log — any wall-clock input has to
  be resolved *before* it enters the log.
- **AOF written exactly once.** The leader must not run the handler on the
  propose side; only `Apply` writes the AOF.
- **Single clock (M18 scope).** The store's `nowFunc()` lazy-expiry and the
  background sweeper delete keys locally, off-log — deterministic with one node
  only. Multi-node expiry determinism is explicitly M19's problem.

## Decision

**Propose *above* dispatch; `Apply` *reuses* dispatch. A mutating command from a
live client is resolved to absolute form and proposed through Raft inside
`dispatch` (gated on `s.replicated && h.mutating && !cs.applying`); the leader
does not run the handler locally. `Apply` re-enters `dispatch` with a synthetic
`connState{authenticated:true, applying:true}`, so the handler mutates the store
and writes the AOF exactly once. The reply is captured inside `Apply` and handed
back to the proposing client by log index.**

Four load-bearing sub-decisions:

1. **Command envelope = version byte + RESP array**, mirroring the AOF record
   format so both durability surfaces speak the same bytes. Only mutating
   commands are enveloped and replicated.

2. **Reply capture by log index.** ToyRaft's `Node.Propose(ctx, data) (Index,
   Term, error)` blocks until the entry is *applied* (SC3) and propagates
   `Apply`'s **error**, but **discards `Apply`'s `result any`** at the Node
   boundary. toykv replies carry data computed inside `Apply` — INCR's new value,
   LPUSH's length, DEL's count — that a follow-up read cannot reconstruct (the
   reference `toyraftd` sidesteps this with a leader-local read; toykv cannot).
   So `StateMachine.Apply` stashes each reply under `entry.Index` in a small map,
   and `Node.Propose` collects it by the index Propose returned. This is
   race-free precisely because Propose returns only *after* Apply has run.

3. **Determinism by propose-time resolution.** `resolveNondeterministic`
   rewrites `SET …EX/PX/EXAT → …PXAT <abs>` and `EXPIRE/PEXPIRE → PEXPIREAT
   <abs>` using the leader's clock before proposing. Conditional tokens that
   depend on live state (`SET NX/XX`) are preserved and still evaluated during
   `Apply`; malformed args pass through so `Apply` surfaces the same
   clock-independent error the standalone path would.

4. **No-op transport, in-memory Raft log, AOF as durability.** A single-node
   cluster (`Peers=[self]`) fans out no vote/heartbeat messages (ToyRaft's
   ELEC-04 single-node fast path makes the self-vote an immediate quorum), so a
   minimal no-op `raft.Transport` suffices — and it sidesteps `pkg/transport/
   inproc`, whose required `HubConfig.Clock` is typed on toyraft's
   *un-importable* `internal/clock`. The Raft log lives in `pkg/storage/memory`
   and starts empty every boot; durable state is re-derived from the AOF before
   the node starts. `Snapshot`/`Restore` return `ErrSnapshotUnsupported` per the
   v1 contract, but the store-serialization they will call is built and unit-
   tested now (forward-compat for ToyRaft v2).

## Consequences

- **Positive.** The seam is a single point: handlers never learn about Raft, and
  the same `dispatch` serves live traffic, replay, and Apply. Standalone is
  byte-identical (the gate is never taken when `s.replicated` is false). The
  exactly-once AOF write and strict apply order fall out of the design, so M19
  only adds transport/storage/election, not a re-architecture. The forward-compat
  serializer makes ToyRaft-v2 compaction a one-line swap.
- **Negative / owned.** The reply-by-index map is toykv working around a v1 API
  gap (Propose discards the result) — reported upstream as dogfood feedback. On
  multi-node (M19) `Apply` runs on followers too and would stash replies no
  client takes; M18 is single-node so every stash is taken, but M19 must bound
  the map for non-leader applies.
- **Neutral.** Startup blocks on `WaitLeader` (~one election timeout) before
  serving, so a client's first write never races the election into a spurious
  NOTLEADER.

## Alternatives considered

- **Propose *inside* each handler.** Rejected: 18 mutating handlers would each
  learn about Raft, and the mutate/append/reply split would leak everywhere. The
  pre-`h.fn` gate keeps it to one site.
- **Reconstruct replies with a leader-local read after Propose** (the `toyraftd`
  pattern). Rejected: works for GET-after-SET but not for INCR/LPUSH/DEL whose
  reply is the *outcome* of the mutation, computed only inside `Apply`.
- **Patch ToyRaft's `Node.Propose` to return the Apply result.** Rejected: the
  public API is frozen for the `v1.0.0` dogfood gate; the index→reply map is a
  consumer-side fix that needs no upstream change.
- **`pkg/transport/inproc` for the single node.** Rejected: its `HubConfig.Clock`
  is an un-importable internal type, and a single node needs no transport at all.
- **File-backed Raft log now.** Deferred to M19: with one node the AOF already
  provides durability, and an empty in-memory log on boot changes nothing
  observable.

## References

- HLD.md — dispatch chokepoint; LLD §4 (AOF record format)
- ROADMAP.md §v3.0 M18; plan `gleaming-roaming-adleman.md`
- Related ADRs: [[0003]] (AOF format & version byte), [[0004]] (TTL canonical
  PXAT encoding — reused by `resolveNondeterministic`), [[0012]] (tagged-union
  store & AOF v3 — the records `Apply` writes)
