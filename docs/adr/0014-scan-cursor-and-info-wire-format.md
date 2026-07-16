# ADR-0014: SCAN insertion-sequence cursor & INFO wire format

- Status: Accepted
- Date: 2026-07-16
- Milestone: M13
- PR: [#31](https://github.com/prajwalmahajan101/toykv/pull/31)

## Context

M13 adds `SCAN` and `INFO`. Two decisions in it close off real
alternatives, so they are recorded here even though the roadmap originally
projected "no new ADR" for M13.

1. **How does `SCAN` provide a stateless, resumable cursor?** Redis's real
   cursor is reverse-binary iteration over its hash-table buckets — it
   survives rehashing and gives the "every key present for the whole scan
   is returned at least once" guarantee. toykv's store is a Go
   `map[string]entry`, whose iteration order is deliberately randomized
   and which exposes no bucket structure. Redis's technique is simply
   unavailable. A cursor model has to be built from what a Go map *does*
   offer, while still honouring the SCAN guarantee under concurrent
   mutation (M13's owned risk test).

2. **What wire form does `INFO` take?** ROADMAP line 186 said "a RESP3 map
   when the client is on RESP3, a bulk string on RESP2." Real Redis returns
   `INFO` as a **bulk string** (RESP2) / **verbatim string** (RESP3) — it
   never maps `INFO` — and `go-redis .Info()` / `redis-cli info` both parse
   the `# Section\nkey:value` text. Emitting a map would break the client
   compat the project has held since M8.

Constraints inherited: the store's single `sync.RWMutex` (HLD §; no ADR —
0002 was reserved but never written); the tagged-union `entry` (ADR-0012)
that SCAN must iterate uniformly across string/list/hash keys; the
single-point RESP2 downgrade in the writer (ADR-0011). No AOF format
change is in scope.

## Decision

**`SCAN`'s cursor is a per-key monotonic insertion sequence; `INFO` is the
Redis-faithful text form, not a map.**

### 1. SCAN cursor = insertion sequence (`entry.seq`)

Each `entry` carries a `seq uint64` stamped from a store-wide counter at
the moment a key is **created** (absent→present), and **preserved across
updates** — an overwrite (`SET`, `INCR`), a list/hash mutation, an
`EXPIRE`/`PERSIST` all keep the key's original seq. The counter starts at
1, so seq `0` unambiguously means "unset."

`Store.Scan(cursor, match, count)`:
- gathers live (non-expired) keys with `seq > cursor`,
- sorts them by seq ascending,
- examines up to `count` (default 10; Redis COUNT is a hint),
- applies a `path.Match` glob to the examined set,
- returns `next` = the last examined seq, or **0** when the end is reached.

The cursor a client passes back is therefore a seq value, mirroring
Redis's opaque integer cursor.

**Guarantee.** A key present for the entire iteration keeps its seq and is
therefore always reached — satisfying Redis's SCAN contract under
concurrent mutation. This is what the seq buys over a positional cursor:
deleting keys with *lower* seq never shifts a still-present key out of the
walk, because the cursor is a seq, not an index.

**What it deliberately does not promise** (Redis parity): keys created
mid-iteration (higher seq) may or may not appear; a key mutated mid-scan
can be returned more than once across the full loop. Both match Redis's
weak SCAN semantics.

### 2. INFO = verbatim string (RESP3) / bulk string (RESP2)

`cmdInfo` builds the `# Section\r\nkey:value\r\n` blob and returns
`resp.Verbatim("txt", blob)`. The writer emits `=` on RESP3 and
auto-downgrades to `$` bulk on RESP2 (the single downgrade point from
ADR-0011) — so one handler serves both protocols and both parse cleanly in
`go-redis` / `redis-cli`. An optional `INFO [section]` filter selects one
section; the meta-sections `default`/`all`/`everything` (and no argument)
return the full set. The ROADMAP "RESP3 map" wording is corrected to match.

## Consequences

**Positive.**
- Integer cursor exactly like Redis; the SCAN guarantee holds under
  concurrent insert/delete, proven by the owned risk test
  (`TestScan_CursorGuaranteeUnderConcurrentMutation`, `-race`) which
  churns a low-seq noise range while must-see (higher-seq) keys are
  scanned — the precise index-shift hazard a positional cursor fails.
- `INFO` is byte-shaped like Redis and parses via `go-redis .Info()` on
  both RESP2 and RESP3 (dual-protocol e2e test).
- **No AOF format change.** `seq` is in-memory only; replay re-derives it
  in record order. Cursors are not persisted — same as Redis, where a
  restart invalidates any in-flight cursor.
- The seam is reusable: a future `HSCAN`/`SSCAN` (v3) can apply the same
  seq idea per-collection.

**Negative / accepted.**
- `Scan` sorts the candidate set (`O(N log N)`) on every call rather than
  Redis's amortized `O(1)` bucket step. Acceptable at toy-KV working-set
  sizes; the whole point of SCAN here is bounding *reply* size, not
  matching Redis's iteration cost. If a large keyspace ever makes this
  bite, the fix is an auxiliary ordered index (rejected below), not a
  cursor redesign.
- A `SET k v` that *creates* a key advances the global seq counter; a
  pure overwrite does not. Deleting and re-creating a key gives it a new
  (higher) seq — correct, it is a new key.
- SCAN can surface duplicates across a full loop for churning keys — Redis
  parity, but callers must dedupe if they need a set.

**Neutral.**
- `entry` grows by 8 bytes (one `uint64`). Negligible.
- INFO's field set is the roadmap five plus cheap staples
  (`redis_version`, `tcp_port`, `connected_clients`, `loading`,
  `aof_rewrite_in_progress`); `aof_last_bgrewrite_status` was dropped to
  avoid new bookkeeping.

## Alternatives considered

- **Reverse-binary bucket cursor (Redis's actual algorithm).** Impossible
  over Go's runtime map — no bucket access, randomized order, no rehash
  hook. This constraint is the whole reason a different model is needed.
- **Integer index into a freshly-sorted key snapshot.** Simple and
  stateless, but a concurrent delete of an earlier key shifts every later
  index down, so a still-present key can be skipped — it fails the SCAN
  guarantee exactly where the owned risk test probes.
- **Auxiliary ordered index kept in sync on every write.** Gives `O(log N
  + COUNT)` scans, but every mutating command must then update two
  structures that can never diverge — a large consistency-and-bug surface
  for a toy KV. Deferred; the seq-sort is the honest first cut.
- **INFO as a RESP3 map (roadmap literal).** Breaks `go-redis .Info()`
  parsing on RESP3 connections and diverges from real Redis, which never
  maps INFO. Rejected in favour of client compat; roadmap wording
  corrected.

## References

- ROADMAP §M13 (scope, owned risk test, corrected INFO wording)
- Related ADRs: [0011](./0011-resp3-negotiation-and-protocol-state.md)
  (single RESP2 downgrade point, `connState` proto), [0012](./0012-tagged-union-store-and-aof-v3.md)
  (the `entry` SCAN iterates)
