# ADR-0015: TUI v2 — SCAN paging boundary & TLS-client deferral

- Status: Accepted
- Date: 2026-07-17
- Milestone: M14
- PR: [#32](https://github.com/prajwalmahajan101/toykv/pull/32)

## Context

M14 rebuilds the TUI (`internal/tui` + `cmd/toykv-tui`) onto the v2 surface:
list/hash values (M11), AUTH (M12), and `SCAN` + `INFO` (M13). The roadmap
projected **"ADR: none new — consumes M11–M13 surfaces,"** and most of M14 is
exactly that — a consumer of contracts already recorded (ADR-0011/0012/0013/0014).

Two calls in it are not consumption; they draw new boundaries, so — as ADR-0014
did against the same "no new ADR" projection — they are recorded here:

1. **What does the TUI's paging guarantee, and what does it delegate?** The keys
   pane now pages over `SCAN` (M13) instead of loading everything with `KEYS *`.
   Paging needs client-side state (a cursor stack for back-navigation). The
   question is where the consistency boundary sits: does the *client* promise
   anything about a multi-page walk, or does it lean entirely on the server's
   ADR-0014 SCAN guarantee?

2. **Does the shipped client speak TLS?** M12 gave the *server* TLS termination.
   The shared `internal/client` (used by both `toykv-cli` and `toykv-tui`) is
   still plain-TCP. A TUI pointed at a TLS-only server cannot connect. Do we
   extend the client now, or defer?

Constraints inherited: the single-connection, no-pipelining RESP2 client
(`internal/client`, ADR-0009's injectable `Doer`); the SCAN insertion-sequence
cursor and its deliberately-weak guarantee (ADR-0014); `-NOAUTH` dispatch gating
and the `{AUTH, HELLO, PING}` whitelist (ADR-0013).

## Decision

**The TUI is a plain-TCP, RESP2 SCAN *consumer*: paging is a client-side cursor
stack that carries no consistency promise of its own, filtering moves to
server-side `SCAN MATCH`, and TLS-client dialing is deferred to the v2.x backlog.**

### 1. Paging = client cursor stack over the server's guarantee

Each keys-pane page is one `SCAN cursor MATCH pattern COUNT n` call. The model
keeps `pageCursor` (the cursor that produced the current page), `nextCursor`
(0 ⇒ last page), a `cursorStack []uint64` for back-navigation, and `pageCount`
(the COUNT hint). `]`/`[` step forward/back; forward pushes the current cursor
and moves to `nextCursor`, back pops.

The consistency boundary is explicit: **the only guarantee is ADR-0014's** — a
start-to-cursor-0 walk returns every key present for the whole scan. The
client-side cursor stack is a *UX convenience for re-viewing a prior page*, not a
snapshot: a popped cursor re-issues `SCAN` against live data, so a page revisited
after mutation may differ. The TUI promises nothing stronger than the server
does. Any state change that invalidates prior cursors — a new `MATCH` pattern or
`FLUSHDB` — calls `resetPaging()` (cursor 0, empty stack).

### 2. Filtering is server-side `SCAN MATCH`

`/` sets the `SCAN MATCH` pattern instead of filtering a locally-materialised key
list. Match semantics are therefore the server's `path.Match` glob (ADR-0014's
Scan), not the TUI's old bespoke `globMatch` — which is **removed**. The pattern
is still reused client-side only to *highlight* the matched span in each rendered
name (`globLiterals`, retained). This is the change that lets the pane stop
materialising the whole keyspace — the point of adopting SCAN.

### 3. TLS-client dialing is deferred

The TUI connects over plain TCP and authenticates with `AUTH` (a masked prompt on
`-NOAUTH`, or `-a` at launch). TLS in `internal/client` is **not** in M14. The v2
goal is "usable, safe-by-default single node"; on the loopback/trusted-network
deployments the TUI targets, AUTH closes the gap that matters. TLS-client touches
the shared client (and thus the CLI), so it is recorded as a deliberate deferral,
not an oversight — tracked in the v2.x backlog.

## Consequences

**Positive.**
- The large-keyspace caveat from v1 is gone: the pane holds one page, not the
  whole keyspace. Cost is bounded by `COUNT`, not `DBSIZE`.
- The consistency story is honest and small: the client adds no contract the
  server doesn't already back, so there is nothing new to test for cross-page
  correctness — ADR-0014's owned risk test already covers the only guarantee.
- Filtering scales with the server (MATCH is applied during the scan) and the
  client sheds its bespoke glob matcher.
- AUTH-only keeps the shared client unchanged for the CLI; no dual-transport
  surface to get wrong this milestone.

**Negative / accepted.**
- **One `TYPE` + `TTL` round-trip per key per page.** The single-connection
  client does not pipeline (ADR-0009), so a full 50-key page is ~100 sequential
  calls. Bounded by `COUNT` (not the keyspace) and acceptable at toy scale; if it
  ever bites, the fix is pipelining in the client, not a paging redesign.
- **Page-back is not a snapshot.** A revisited page reflects live data. This is
  Redis-SCAN-faithful but can surprise a user expecting stable pages; documented
  in LLD §7.6 and surfaced in the page indicator, not hidden.
- **A TLS-only server is unreachable from the shipped TUI.** Explicit gap until
  the backlog item lands.

**Neutral.**
- The status bar is now `INFO`-driven (live `appendfsync`/`uptime`/`dbsize`/
  `clients`); `-fsync` becomes an override. This is pure consumption of ADR-0014's
  INFO wire form — noted, not a decision.

## Alternatives considered

- **Full-accumulate SCAN (loop to cursor 0 every refresh, keep client-side glob).**
  Simplest migration, but it re-materialises the whole keyspace on every sweep —
  it swaps `KEYS *` for a SCAN loop without buying the scaling that motivated
  adopting SCAN. Rejected; true paging is the point.
- **Client-side page snapshots (cache each page's keys for stable back-nav).**
  Would give stable pages, but that is a *stronger* promise than the server makes
  and a cache that silently drifts from live data — a consistency surface for a
  read-only browser UI to maintain. Rejected: the client should not invent a
  guarantee the server doesn't offer.
- **Add TLS to `internal/client` now.** Real work on the shared client + new TUI/
  CLI flags for a capability the v2 "usable single-node" goal doesn't require on
  the deployments the TUI targets. Deferred to backlog rather than widened into
  M14.
- **No ADR (follow the roadmap's "none new").** The TLS-deferral boundary and the
  "cursor stack is not a contract" split are exactly the kind of *what we
  deliberately do not guarantee* calls that read as an architectural boundary, not
  an LLD note — same reasoning that added ADR-0014 against the same projection.

## References

- ROADMAP §M14 (scope; "none new" projection revised here, as with M13/ADR-0014)
- LLD §7.6 (TUI v2 mechanics: paging, typed render, INFO status, AUTH prompt)
- Related ADRs: [0014](./0014-scan-cursor-and-info-wire-format.md) (the SCAN
  guarantee + INFO wire form this consumes), [0013](./0013-auth-model-and-tls-termination.md)
  (server AUTH/TLS; the TUI consumes AUTH, defers TLS), [0009](./0009-tui-bubble-tea-and-injectable-doer.md)
  (the single-conn `Doer` the paging fetch runs on)
