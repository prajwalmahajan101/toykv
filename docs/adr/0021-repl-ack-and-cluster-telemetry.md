# ADR-0021: Replication acknowledgement, INFO replication, and cluster OTel

- Status: Accepted
- Date: 2026-09-08
- Milestone: M21
- PR: #47

## Context

M20 shipped write-redirect and a cluster read model. The next operator need is to *observe* replication progress: to know whether a write has been durably acknowledged by N replicas (durability SLA), to see per-node lag in INFO output, and to have those signals in the existing OTel/LGTM stack (ADR-0017). Three decisions are required:

1. What does `WAIT numreplicas timeout_ms` actually block on, and can it over-count?
2. What fields go in `INFO replication`, and how do we get per-peer addresses?
3. How does the OTel extension fit into the existing precomputed-instrument model?

## Decision

### WAIT semantics

At call time, snapshot `CommitIndex` from `cluster.Node.Status()` as the target. Poll `Status().MatchIndex` (leader-only; nil on followers) until `count(MatchIndex[peer] >= target) >= numreplicas`, or the timeout expires. Returns the *actual* count — never over-reports a count higher than `len(MatchIndex)`. On a follower: NOTLEADER redirect. On single-node or standalone: returns 0 immediately (no follower entries in MatchIndex). Timeout = 0 means wait indefinitely; context cancellation terminates immediately. Poll interval is 50 ms.

Using `CommitIndex` as the target is correct: it is the highest index that ToyRaft has replicated to a quorum. `MatchIndex[peer] >= CommitIndex` means the follower has durably accepted everything the leader considers committed. Any follower with a lower MatchIndex is behind and must not be counted for the requested ack threshold.

ToyRaft includes the leader's own node ID in `MatchIndex` (`matchIndex[self] = idx`, `leader.go:116`) for its internal quorum accounting. Redis `WAIT` counts only *remote* replicas; the leader always holds the committed data. `countReplicas` therefore skips `matchIndex[selfID]` to match Redis semantics and avoid a systematic over-count of 1.

### INFO replication

Appended as a `# Replication` section (gated on `s.replicated`). Fields:

```
role:master|slave
connected_slaves:N          (leader-only; 0 on follower)
slaveN:ip=...,port=...,state=online,offset=<MatchIndex>,lag=<CommitIndex−MatchIndex>
master_repl_offset:N        (CommitIndex on leader; ApplyIndex on follower)
```

Peer client addresses come from `cfg.Peers` (already in Server) via a new `cluster.Node.Peers() []Peer` accessor that returns the slice stored at `New()` time. Using the client address (not the Raft-plane address) in the slave line matches Redis's semantics — it is the address clients dial, not an internal port. Lag is reported per peer and is live (snapshotted from Status() each INFO call); it may flicker by one heartbeat window but is never stale by more than one `CommitIndex` tick.

### OTel extension

Three new signals, consistent with the ADR-0017 "server-originated, no hot-path guard" model:

1. **`raft.propose` span** — child of the command span; wraps `cluster.Node.Propose()` in the dispatch propose gate. Captures end-to-end propose→commit→apply latency as seen from the server. Attributes: `command` (verb), `raft.commit_index` (int, on success). This is the only instrumentation point accessible without patching ToyRaft internals; it accurately reflects the wall-clock cost that the client pays.

2. **`toykv.raft.is_leader` gauge** — `Int64ObservableGauge`, 1 when leader, 0 otherwise. Attribute: none (single series per node). Enables Grafana alerting on leader-count anomalies in a multi-node dashboard.

3. **`toykv.raft.replication_lag` gauge** — `Int64ObservableGauge`, one series per peer, attribute `peer=<nodeID>`. Value = `CommitIndex − MatchIndex[nodeID]`. Enables per-replica lag alerting and visualisation. Recorded only when `s.replicated` and role is leader; on followers the gauge emits nothing (MatchIndex is nil; no callback registration overhead).

Both gauges use the observable-gauge pattern already established in `registerObservableGauges()` (metrics.go), so the collection cycle drives reads rather than the hot path — consistent with ADR-0017's "no per-command guard" design.

## Consequences

**Positive:**
- `WAIT` is provably truthful: it counts only entries present in `MatchIndex`, which ToyRaft populates only after a follower has persisted and acknowledged entries. A dead follower's entry does not advance; its count never contributes.
- `INFO replication` gives operators a redis-cli-compatible introspection surface without new wire protocol or ports.
- The propose span makes `raft.propose` latency visible in Grafana alongside command duration; the lag gauges close the observability gap between "cluster is running" and "cluster is caught up."

**Negative:**
- `WAIT` polls at 50 ms; a follower that catches up mid-interval will not be counted until the next tick. Maximum false-negative window: 50 ms. This is accepted — the alternative (callback into ToyRaft on MatchIndex advance) would require an internal API not exposed by `rc.2`.
- Lag gauge is leader-only; follower nodes emit nothing. Operators must scrape the current leader to get replica-lag signals.

## Alternatives considered

**WAIT based on ApplyIndex (follower self-report):** Each follower would need to report its own ApplyIndex via a side-channel. Rejected — no such RPC exists in ToyRaft's public API, and building one would require forking the transport layer.

**WAIT blocks indefinitely on zero timeout:** Consistent with Redis when `timeout_ms = 0`. A nil channel in Go's select never fires, so the implementation is a natural fit. Accepted.

**Replication-lag via Prometheus push from each node:** Rejected as out of scope for M21 and inconsistent with the push-to-OTLP model from ADR-0017.

## References

- ADR-0017: OpenTelemetry signal model
- ADR-0019: Cluster transport layout (MatchIndex source)
- ADR-0020: Write redirection (NOTLEADER redirect pattern)
- `internal/cluster/node.go` — `Status()`, `Peers()`
- `internal/server/dispatch.go` — propose gate, WAIT dispatch entry
