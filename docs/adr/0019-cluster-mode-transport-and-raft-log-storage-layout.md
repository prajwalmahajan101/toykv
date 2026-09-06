# ADR-0019: Cluster mode — HTTP peer transport & file-backed Raft log

- Status: Accepted
- Date: 2026-09-06
- Milestone: M19 (M19.1)
- PR: _feat/cluster_

## Context

M18 embedded ToyRaft as a **single-node** cluster: `Peers=[self]`, an in-memory
Raft log (`pkg/storage/memory`), and a no-op transport. Every mutating command
flowed `Propose → StateMachine.Apply → store + AOF → reply`, but nothing left
the process. M19 makes the cluster real: multiple nodes, leader election, and
replication over a network.

ToyRaft is deliberately transport- and storage-agnostic — `raft.Config.Peers` is
a bare `[]NodeID`, and the node takes a `Storage` and a `Transport` it does not
construct. The consumer owns three things M18 left stubbed:

1. **Peer address resolution.** The core addresses messages by `NodeID`; someone
   must map an id to a socket. ToyRaft's `pkg/transport/http` owns a `PeerURLs`
   table for exactly this (ADR-0015 on the ToyRaft side).
2. **A durable Raft log.** In-memory is fine for a single node that re-derives
   from the AOF, but a replicated cluster needs the Raft log itself to survive a
   restart so a rejoining node can catch up. ToyRaft ships `pkg/storage/file`.
3. **A listen/dial transport.** ToyRaft ships `pkg/transport/http` (HTTP/JSON).

Standing this up surfaced a blocker: at `v1.0.0-rc.1`, `pkg/transport/http` could
not be constructed by an external module — its `Config.Clock` was typed on
ToyRaft's `internal/clock` and rejected nil, and that interface is neither
constructible nor implementable from outside. This was filed as a dogfooding
finding; ToyRaft `v1.0.0-rc.2` fixes it by defaulting a nil `Clock` to the real
clock in `http.New` (parity with `raft.Config`). toykv depends on `rc.2`.

## Decision

**Wire ToyRaft's `pkg/transport/http` on a dedicated peer port and
`pkg/storage/file` as the Raft-log store; keep the M18 single-node path
byte-identical as a fallback.**

- **Peer spec.** `-peers "id@host:raftport,…"` is the full membership, **self
  included**, parsed once in `internal/cluster/peers.go` (`ParsePeers`). It must
  be odd (ToyRaft requires odd N so quorum can't split) with unique ids.
  `-raft-addr` is this node's transport bind (defaults to the self entry's
  address); `-raft-dir` is the file-log directory (required when multi-node).
  The Raft/peer port is always distinct from the client `-addr`.
- **Construction seam.** `cluster.New(Config)` replaces M18's positional
  constructor. `len(Peers) <= 1` selects the single-node path (memory log,
  no-op transport) unchanged; a larger membership builds `file.New(raftDir)`,
  an `http.New` transport with `PeerURLs` = every peer **except self**, and a
  `raft.Config` with `Peers` = all ids. The transport's `Clock` is left unset
  (defaulted by rc.2).
- **The Raft log is the replication source of truth; the AOF stays each node's
  *local* applied-state durability.** `StateMachine.Apply` still appends the AOF
  on every node, so a node's own state survives a crash; the file-backed Raft
  log lets a rejoining/lagging node be caught up by the leader.
- **`WaitLeader` waits for a leader to *exist*, not for *self* to lead.** M18
  waited until this node became leader (correct when it is the whole cluster). A
  follower never becomes leader, so startup now unblocks as soon as a leader is
  known (`Role==Leader || LeaderHint != ""`), then serves.
- **A write to a follower returns `NOTLEADER <hint>`, not a silent drop.**
  ToyRaft rejects a non-leader `Propose` with `ErrNotLeader`; the dispatch propose
  gate surfaces it as a `-NOTLEADER` reply carrying the leader hint. Client-side
  auto-redirect is deferred to M20.

## Amendment (M19.2) — failure-mode correctness is proven over a chaos-wrapped real transport

_Added 2026-09-06 after M19.2 (failover + partition correctness). The roadmap grants
19.2/.3 authority to amend this ADR when a new contract surfaces; this is that amendment._

M19.2 proves the three high-blast-radius guarantees — a leader kill loses no acked
write, a partition heals to the majority history, and no two leaders share a term —
with **in-process failure injection**, not the "subprocess/process-kill harness" the
roadmap originally sketched. Two reasons drove the change:

1. **A real `kill -9` cannot inject a partition.** A killed process is a crash, not a
   partition; an isolated-but-alive node needs OS network manipulation (iptables/root),
   which is non-portable and not race-clean. Two of the three exit conditions
   (partition-heal reconciliation, split-brain guard) require an alive-but-isolated node.
2. **ToyRaft's `pkg/transport/inproc` Hub — the natural chaos surface — is not
   externally constructible at `v1.0.0-rc.2`.** `HubConfig.Clock` is typed on
   `internal/clock`, required non-nil, with no public constructor and an
   externally-unimplementable interface (its methods return un-nameable internal
   `Timer`/`Ticker` types). This is the same class of blocker the http transport hit at
   rc.1, left unfixed for `inproc`. Recorded as a dogfood finding in
   [`docs/TOYRAFT-MIGRATION-REPORT.md`](../TOYRAFT-MIGRATION-REPORT.md) (M19 section).

**New contract.** Cluster failure-mode correctness is proven by `chaosTransport`
(toykv-side, test-only) — a `raft.Transport` that **wraps the real
`pkg/transport/http`** and drops messages crossing a mutable, mutex-guarded partition
cut: outbound in `Send`, inbound in the registered `step` callback. Tests toggle cuts
via `Partition`/`Heal`. This exercises the **production** consensus path over HTTP
under partition (closer to production than an in-process hub would be), race-clean; the
only thing given up is inproc's seeded delivery determinism, which was unattainable
anyway without an external fake clock.

**Construction seam.** The harness builds `*cluster.Node` **directly** in-package
(`internal/cluster/failover_test.go`, `package cluster`) — wrapping each node's own
`http.New` transport in `chaosTransport` and using an in-memory Raft log — rather than
going through `cluster.New`. This keeps the reply-capture path
(`Propose → Apply → TakeResult`) identical to production while injecting the wrapper,
with **no test-only field on the production `Config`**. Real crash/restart recovery of
the file-backed Raft log is out of scope for M19.2 (in-process only); `memory` storage
is sufficient for these tests.

## Consequences

**Positive**
- A real odd-N cluster elects a leader and replicates every acked write to all
  followers (M19.1's owned happy-path test proves convergence over the real HTTP
  transport under `-race`).
- The single-node and standalone paths are untouched — `len(Peers) <= 1` keeps
  M18 behaviour, and non-`-replicate` stays byte-identical to v2.
- The `Role`/`LeaderHint`/`Status` passthroughs added here are the surface M20
  (redirect) and M21 (`INFO replication`, `WAIT`) build on.

**Negative**
- The Raft log now lives on disk in a second location alongside the AOF — two
  durable surfaces to reason about. Compaction of the (currently unbounded) Raft
  log is deferred to v3.x pending ToyRaft `v2` snapshots.
- The peer transport is unauthenticated/plaintext (ToyRaft threat model =
  trusted network). The bind guard + security note is M23.

**Neutral**
- Depends on ToyRaft `v1.0.0-rc.2` (the transport-constructibility fix). The
  dependency bumps to `v1.0.0` at M23 once ToyRaft tags off this integration.

## Alternatives considered

- **Keep the in-memory Raft log even for multi-node.** Rejected: a restarted
  node would lose its log and could only rejoin by full re-replication, and a
  whole-cluster restart would lose committed-but-unapplied entries. `file` is the
  documented production store.
- **A single `-peers` list that *excludes* self (self given only by
  `-raft-addr`).** Rejected: including self keeps one authoritative membership
  list identical on every node (matching ToyRaft's `raft.Config.Peers`), and the
  transport's self-exclusion is a local derivation, not a per-node config diff.
- **Implement redirect now (M19).** Deferred to M20 by the roadmap split so
  M19.1 stays "wiring + happy path"; surfacing `NOTLEADER` is the minimal seam.
