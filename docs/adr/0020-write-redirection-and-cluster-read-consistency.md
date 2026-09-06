# ADR-0020: Write redirection & cluster read consistency

- Status: Accepted
- Date: 2026-09-06
- Milestone: M20
- PR: _feat/cluster-routing_

## Context

M19 made the cluster real — N nodes, elections, replication — but left the
client half-open. A mutating command that landed on a follower was rejected by
ToyRaft with `ErrNotLeader`, which the dispatch propose gate surfaced as a
`-NOTLEADER leader is <hint>` error and stopped there. Two things made that hint
useless for automatic routing:

1. **The hint was not client-dialable.** `Node.LeaderHint()` returns a
   `raft.NodeID`, and the only address the cluster knew per member was the
   **raft-transport** `host:raftport` (ADR-0019) — a *separate port* from the
   client-facing `-addr` listener. A client cannot connect to it.
2. **The shared client had no routing.** `internal/client.Client` is a
   single-connection RESP2 primitive that marks itself closed on any error; it
   had no notion of "try the leader instead."

Reads were the mirror problem: a follower will happily serve a `GET` from local
state that may lag the leader's commit index, so "read from whatever node you
connected to" is silently non-linearizable. We needed a default that is correct
and an opt-in that is fast, both stated explicitly rather than left to chance.

## Decision

**A follower emits a machine-parseable `-NOTLEADER host:port` redirect to the
leader's advertised client address; a new `ClusterClient` follows it under
bounded retry; keyspace reads are leader-routed by default, with a
per-connection `READONLY` opt-in for stale follower-local reads.**

Four parts:

1. **Client-address advertisement rides the `-peers` grammar.** The M19 entry
   `id@host:raftport` gains an optional suffix: `id@host:raftport/host:clientport`.
   `ParsePeers` splits on the first `/`; the suffix is **optional** so every M19
   config still parses, but a member without it cannot be an auto-redirect
   target. `Node` builds a `NodeID → clientAddr` map and exposes
   `LeaderClientAddr()`, so dispatch resolves a leader to a dialable address and
   never handles a `NodeID` itself.

2. **`-NOTLEADER host:port` is the redirect contract.** When the leader's client
   address is known the reply is exactly `-NOTLEADER <host:port>`; otherwise it
   falls back to the operator-readable, non-dialable `-NOTLEADER leader is <id>
   (no client address advertised)` / `-NOTLEADER no leader elected`. The token
   after `NOTLEADER` is a `host:port` **iff** a redirect is possible — a single
   rule the client keys on.

3. **`ClusterClient` wraps, it does not replace, `Client`.** The single-conn
   primitive stays simple. `ClusterClient` retries any `NOTLEADER` reply up to a
   cap: a dialable hint re-dials that leader; a non-dialable one (a leaderless
   window mid-election) re-polls the current node after backoff — so a transient
   election converges instead of failing the call. The cap makes a redirect
   storm terminate in an error rather than loop. CLI and TUI dial through it;
   against a standalone server no `NOTLEADER` is ever emitted, so behaviour is
   byte-identical to a bare `Client`.

4. **Reads are leader-routed by default; `READONLY` opts out per connection.**
   A new `readKeyed` handler flag marks the keyspace-reading commands (`GET`,
   `MGET`, `SCAN`, `HGETALL`, …) — distinct from local-admin reads (`PING`,
   `INFO`, `HELLO`), which always run locally. Under `-replicate`, a `readKeyed`
   command on a non-leader redirects to the leader (linearizable-by-default);
   the `READONLY` command sets a per-connection flag that serves it from local,
   possibly stale, state instead, and `READWRITE` clears it. The flag dies with
   the connection.

A supporting change: `cluster.Config`/`server.Config` gained pass-through
`ElectionTimeoutMin/Max` and `HeartbeatInterval`. Zero values inherit ToyRaft's
defaults (so M19 behaviour is unchanged), and the routing/linearizability test
harnesses widen them to hold a stable leader under `-race`.

## Consequences

- **Positive.** A client can hit any node and every write lands; the read model
  is explicit, not accidental. The redirect contract is one regex (`NOTLEADER`
  + a `host:port`), so any future client (SDK, other-language) implements it
  trivially. `ClusterClient` isolates all routing, keeping `Client` a clean RESP
  primitive that tests still drive over `net.Pipe`.
- **Negative.** The `-peers` grammar now carries two addresses per member —
  more to get right in a deployment (mitigated: the suffix is optional and its
  absence degrades to an operator-readable, non-dialable hint, never a silent
  failure). Leader-routed reads add a redirect hop for a client that happens to
  connect to a follower; `READONLY` is the escape hatch for read-heavy, staleness-
  tolerant workloads.
- **Neutral.** `READONLY`/`READWRITE` are per-connection and non-replicated —
  they never enter the Raft log or the AOF. Redirect retry is bounded, so a
  permanently leaderless cluster returns a `NOTLEADER` error rather than hanging.

## Alternatives considered

- **A separate `-peer-client-addrs` flag.** Rejected: two lists to keep in sync
  and cross-reference by id, versus one grammar that already pairs id with
  address.
- **Gossip the client address through the Raft log.** Rejected: couples toykv to
  ToyRaft-internal metadata plumbing we do not own, to replace a value that is
  static and known at startup.
- **Port-offset convention (client = raft ± k).** Rejected: zero-config but
  brittle and undocumented; breaks any non-uniform deployment and hides the
  mapping the operator should see.
- **Teach `Client` to redirect internally.** Rejected: complicates the
  single-conn primitive that also backs standalone mode and the TUI's injectable
  `Doer` (ADR-0009); a wrapper keeps the seam clean.
- **Always-local follower reads (no `READONLY`).** Rejected: simpler but
  silently non-linearizable by default, contradicting the roadmap's read-model
  exit criterion. The default must be correct; staleness must be opt-in.

## References

- HLD.md — cluster/replication section
- LLD.md — client, dispatch
- Related ADRs: [[0018]] (command envelope & StateMachine seam), [[0019]]
  (peer transport & `-peers` grammar this extends), [[0009]] (TUI `Doer` seam)
