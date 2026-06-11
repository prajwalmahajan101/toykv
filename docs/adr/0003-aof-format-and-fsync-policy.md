# ADR-0003: AOF format and fsync policy

- Status: Accepted
- Date: 2026-06-11
- Milestone: M3
- PR: (to be filled on merge)

## Context

M3 turns toykv into a durable key-value store. The design surface has three load-bearing decisions: (1) the on-disk record format, (2) the fsync policy that controls the durability/latency tradeoff, and (3) the ordering between in-memory mutation, durable append, and the client acknowledgement. These together define the **durability contract** — what guarantees a client gets when the server returns `+OK`. Getting this wrong on M3 makes every downstream milestone (TTL in M4, compaction in M5) stand on sand.

Constraints:

- Single-node, single-writer learning KV. No replication, no log shipping.
- The wire protocol is already RESP2 (M1). We have a working RESP reader and writer.
- We must not block other readers on `fsync` (LLD §8 "store lock never crosses I/O").
- The AOF format must be **version-byte safe** — M4 will add TTL encoding on top.

## Decision

The AOF is a RESP-encoded log of mutating commands, fronted by a small versioned header. Three named fsync policies trade durability for latency. The reply path acks the client only after the configured fsync returns.

Concretely:

- **File layout.** 7-byte magic `"TOYKV\x00\x00"` followed by a 1-byte version (`0x01` for M3) — 8 bytes total. After the header, a stream of RESP-encoded command arrays exactly as they would arrive on the wire, e.g. `*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n`.
- **Storage of commands as RESP arrays.** Reuses the wire codec on disk; one codec serves two consumers. Append becomes "write the bytes you'd send"; replay becomes "feed the file through `resp.Reader` and dispatch each frame." There is no second serialization format to maintain.
- **fsync policies.** `always` (per-Append fsync; default), `everysec` (background ticker fsync), `no` (kernel decides). The default favours durability — appropriate for a learning artefact whose contract is the demonstrable property.
- **Ordering: mutate → append → fsync → reply.** Handlers mutate the in-memory store under the store lock, then release the lock and call `aof.Append`. The conn loop sends the reply only after `Append` returns nil. Under `always`, fsync precedes the reply by construction. Under `everysec`/`no`, the reply may precede durability — the policy's contract is published in this ADR and the journal, not promised by the server.
- **Append on success only.** Failed `SET … NX`/`XX`, `INCR` on a non-integer, and `DEL` of a missing key do **not** produce records. `SET` records are written in canonical 3-arg form so replay can apply them against an empty store without re-evaluating the original conditional.
- **Corrupted-tail recovery: hard fail.** The replayer fails fast with the byte offset of the failing record. v1 does not auto-truncate; an operator-opt-in `-aof-truncate` flag is deferred (no live operator yet, no reason to design the safety net before someone needs it).

## Consequences

**Positive.**

- The on-disk format is the wire format. Adding a command in a future milestone needs no AOF format change — the new RESP array is just bytes.
- Replay reuses the live dispatch table. The same handlers serve replay and live traffic; replay just runs while `s.aof == nil`, so the post-success `appendIfLive` becomes a no-op. No second handler table to drift.
- The version byte gets its first real exercise in M4 (TTL adds an expiry-encoding bump to `Version2`). The bump path is exactly the one this design is built for.
- Under `appendfsync=always`, every acked write survives a SIGKILL (the M3 risk test is exactly this contract).

**Negative.**

- `appendfsync=always` is fundamentally bounded by `fsync()` latency. p95 under this policy is the fsync cost of the underlying filesystem; no amount of in-process tuning will change that. The everysec policy is the escape hatch.
- "Append on success only" means the in-memory state can briefly lead the on-disk state by exactly one mutation if the process crashes between the store mutation and the AOF append. The client did not receive `+OK`, so no consistency promise to the *client* is broken; but a debugger reading both surfaces would see the divergence. Acceptable per LLD §8.2.
- Hard-failing on a corrupted tail means an operator who pulls the plug at a bad time may need to manually trim the file to bring the server back. v1 has no operators yet; this is the right time to be strict.

**Neutral.**

- RESP encoding on disk is verbose vs. a packed binary format. For a learning KV, the byte savings of a packed format do not justify maintaining a second codec.

## Alternatives considered

- **Custom packed binary format (length-prefixed CMD opcode + N×length-prefixed args).** Smaller on disk, faster to parse. Rejected: the wire codec is already written, tested, and known-correct. Maintaining two formats is a long-term liability for a v1 toy.
- **Write-ahead log per command, in-memory state derived from log only (a la LMDB-style).** Conceptually clean but requires a different concurrency model than M2's single-RWMutex store. Out of scope.
- **Group commit / batched fsync under `always`.** Real latency win, but introduces an "ack delay" that complicates the durability contract. Defer until benchmarks demand it (M9).
- **Auto-truncate the corrupted tail with a warning log.** Tempting but unsafe — once the file silently shortens, an operator cannot distinguish "harmless tail" from "data loss". Make the operator opt in.

## References

- HLD.md §7 (single-mutex tradeoff), §8 (durability contract)
- LLD.md §4 (AOF), §5.4 (errors → RESP mapping), §8 (concurrency invariants)
- Related ADRs: [[0001]] RESP2 subset (codec reused here), [[0002]] single-mutex store (lock release before fsync)
