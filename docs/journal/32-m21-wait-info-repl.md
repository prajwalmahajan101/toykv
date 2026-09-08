# 32 — M21: WAIT, INFO replication, and the self-ID trap in MatchIndex

**Date:** 2026-09-08
**Context:** M21 complete on `feat/wait-info-repl`. Three surfaces shipped on top of the
stable M20 cluster: `WAIT numreplicas timeout_ms` (Redis-faithful durability ack),
`INFO replication` section (role, per-replica lag, master_repl_offset), and OTel extension
(`raft.propose` span + `toykv.raft.{is_leader,replication_lag}` gauges). Owns ADR-0021.

## Decision / surprise

**ToyRaft's `Status().MatchIndex` includes the leader's own node ID.** `leader.go:116` sets
`n.matchIndex[n.id] = idx` so the quorum-commit rule can count the leader's own log position
alongside its peers. `Status().MatchIndex` is a direct clone of this internal map. For an
embedder implementing `WAIT`, this is a footgun: on a 3-node cluster (leader + 2 followers)
with one dead follower, `WAIT 2 500` returned `2` instead of `1` because the leader's own
index counted as a "replica" that had acked.

The fix is one line in `countReplicas`: skip `matchIndex[selfID]`. The self-ID is exposed
via a new `cluster.Node.NodeID()` accessor (stored at construction from `cfg.NodeID`).
Redis `WAIT` counts only remote replicas; the leader always holds the committed data —
counting it would let a 1-node deployment return `WAIT 1 0 = 1` as if a remote peer had
acked, which is semantically wrong.

The fix is documented in ADR-0021 and filed as a footgun note in the ToyRaft migration
report with a suggestion to document or expose a `ReplicaMatchIndex()` that strips the
leader's own entry.

## Why it mattered

- **The owned-risk test caught it immediately.** The test structure (stop a follower, write,
  check WAIT < 2) was designed exactly to catch over-counting. Without excluding `selfID`,
  it would have been silently wrong forever — `WAIT 2 0` on a leader with 2 followers always
  returns 2 even if one follower is dead, because the leader itself makes up the difference.

- **Port-check trick doesn't work here.** My first fix attempt polled the dead follower's
  client port until it became unreachable. That never fired: `Server.Close()` stops the Raft
  transport (raft plane) but leaves the TCP client listener open (the accept loop runs under
  the shared test context, not under `Close()`). The correct approach was to wait for two
  heartbeat intervals (600ms) so the leader had time to fail its AppendEntries attempts to the
  dead follower — and then do the new write, so the leader's MatchIndex for the dead peer
  provably couldn't advance.

- **`INFO replication` needed peer addresses unavailable in the cluster package.** The
  `cluster.Node` didn't store the raw `[]Peer` slice (only the derived `clientAddrs` map,
  which omits un-migrated entries). Added a `Peers() []Peer` accessor, stored from
  `cfg.Peers` at `New()`. The server then filters out its own peer entry (`replicaPeers`)
  and formats each remaining peer's `ClientAddr` as `ip:port` for the `slaveN:` line.

## What's next

M22 — TUI v3: cluster view. Depends on `INFO replication` (now stable) and M21 OTel
gauges. Branch: `feat/tui-v3`.
