# 31 — M20: client routing, and the address gap between planes

**Date:** 2026-09-06
**Context:** M20 complete on `feat/cluster-routing` — client routing (write redirect + read
model), merged via PR #46. A follower write now replies `-NOTLEADER host:port`; a new
`ClusterClient` wraps the bare `Client` with bounded redirect retry; reads are leader-routed
by default with a `READONLY` per-connection opt-in for stale follower-local reads. CLI and
TUI dial through it; standalone behaviour is unchanged. Owns ADR-0020.

## Decision / surprise

**The hint the consensus plane hands you is not the hint the client plane needs.** ToyRaft's
`LeaderHint()` returns a `NodeID` — the identifier peers use among themselves. The only
address the cluster carried per member was the raft-transport bind (a separate port from the
client listener, by deliberate design: consensus traffic should not share the RESP port).
So a follower could *say* who the leader is but not *where a client should connect*. There
is no way around this without the consumer (us) owning address translation.

The fix: extend `-peers` from `id@host:raftport` to `id@host:raftport/host:clientport` —
a one-grammar-one-entry-point model (`ParsePeers` is still the single parser). The suffix
is optional for M19 back-compat: an entry without it parses fine but that member cannot be
an auto-redirect target, degrading to an operator-readable hint, never a silent failure.
`Node` builds a `NodeID → clientAddr` map at construction and exposes `LeaderClientAddr()`;
dispatch resolves the leader to a dialable address and never handles a raw `NodeID`.

## Why it mattered

- **The plan said "reuse `newStableCluster`" — the reality couldn't.** The owned-risk test
  needs a *client* hitting a *RESP listener* and following a redirect. `newStableCluster`
  drives `Node.Propose()` directly (no listener), so the test had to stand up full `server.Server`
  instances, each with its own RESP listener and Raft node. That required plumbing the
  election-timeout knobs from `cluster.Config` through `server.Config` into `raft.New` — a
  change the plan didn't anticipate. It turned out to be the *same* gap M19.3 hit (leader
  churn under `-race` on slow CI runners), now closed for all downstream harnesses.

- **Retry is not just redirect.** The first `ClusterClient` cut surfaced a dialable
  `NOTLEADER` error immediately when the hint wasn't a `host:port` — i.e., during a leaderless
  window mid-election, the client gave up on attempt 1 instead of waiting. Revised to retry
  any `NOTLEADER` under the bounded cap: a dialable hint re-dials that leader; a non-dialable
  one re-polls the same node after backoff. A transient election now converges; a permanently
  leaderless cluster terminates in an error rather than hanging. This was not in the spec and
  only became obvious when thinking about what the routing test's "redirect during forced
  leader change" assertion actually means.

- **The read model is three flags, not a feature.** `readKeyed` on the handler, `readonly`
  on `connState`, and the `Role() != Leader` check in dispatch — that's the entire read gate.
  The rest is wire format (the same `notLeaderReply` helper writes handle) and two trivial
  handlers (`READONLY`/`READWRITE` flip a bool, reply `+OK`). Most of M20's complexity lives
  in the client, not the server.

- **Zero new ToyRaft API.** The entire redirect layer was built on `LeaderHint()`,
  `Status().Role`, and `ErrNotLeader` — all already exercised by M19. The migration report
  records this explicitly: no bugs, no friction on the consensus API itself. The friction was
  on our side (the election-timeout plumb, the two-address gap) — both additive fixes.

## What's next

M21 (`WAIT` + `INFO replication` + cluster observability) — the first milestone that reads
`Status().MatchIndex` and exposes replication lag to the operator. Then M22 (TUI v3: cluster
view, consuming M20/M21). Three milestones to v3.0.0.
