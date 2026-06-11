# ADR-0004: TTL canonical PXAT encoding (AOF v2)

- Status: Accepted
- Date: 2026-06-11
- Milestone: M4
- PR: (to be filled on merge)

## Context

M4 adds per-key TTL on top of the AOF. Three load-bearing questions:

1. **How is a TTL encoded in an AOF record?** Relative duration (`PX 5000`) or absolute deadline (`PXAT 1717000000000`)?
2. **What does an `EXPIRE k 5` record look like on disk?** A self-rewrite to absolute form, or a fresh per-command opcode?
3. **What is the version-byte contract between M3 and M4 binaries?** Does the format byte bump force forwards compatibility, backwards compatibility, both, or neither?

The constraint set inherited from ADR-0003 ("the on-disk format is the wire format") forbids inventing a binary expiry field — TTL has to ride RESP arrays of commands like everything else. ADR-0003 also explicitly anticipated this milestone: "the version byte gets its first real exercise in M4."

The temptation is to "write the bytes the client sent." That is wrong: a `SET k v EX 5` written 10 minutes ago, replayed under that rule, would silently resurrect an expired entry as alive-for-5-more-seconds. The format must encode a deadline anchored to a fixed reference (epoch), not a duration anchored to "now."

## Decision

**Three coupled rules.**

### 1. AOF v2 records encode TTL as absolute `PXAT <unix-ms>`

Canonical wire-to-AOF translation, all paths land at PXAT:

| Wire command | Appended record |
|---|---|
| `SET k v` | `SET k v` (no PXAT token — identical to v1 record) |
| `SET k v EX s` / `PX ms` / `EXAT s` / `PXAT ms` | `SET k v PXAT <unix-ms>` |
| `SET k v NX EX 5` (success) | `SET k v PXAT <unix-ms>` (NX stripped, same principle as ADR-0003) |
| `SET k v NX …` (failure) | *(nothing)* |
| `EXPIRE k 5` / `PEXPIRE k ms` / `PEXPIREAT k ms` | `PEXPIREAT k <unix-ms>` |
| `PERSIST k` (success) | `PERSIST k` |
| `PERSIST k` (no-TTL or missing) | *(nothing)* |

`PEXPIREAT` is also exposed on the wire (Redis-compatible) so replay never needs a non-public command path.

### 2. Version byte bumps `0x01 → 0x02`; v2 binaries accept both

- `writeHeader` emits `CurrentVersion = 0x02` on fresh files.
- `readHeader` consults `supportedVersions = {0x01, 0x02}`. A v1 file (every record is a valid RESP array of v1-known commands) replays cleanly on M4+ code. Old M3 code refuses v2 files via the pre-existing `ErrBadVersion` path, because a M3 binary does not understand `PEXPIREAT` and would fail at dispatch — gating at the header is friendlier than a half-replayed file.

### 3. Records are still pure RESP arrays — there is no binary format change

The v1→v2 diff in `format.go` is **one constant (`Version2 = 0x02`), one slice (`supportedVersions`), one `CurrentVersion` indirection in `writeHeader`, and one supported-set loop in `readHeader`.** Total: ~15 lines. The "format pays for itself" prediction made in the M3 journal landed correctly.

## Consequences

**Positive.**

- **Replay survives any downtime.** Absolute deadlines are wall-clock anchored; the downtime no longer extends a key's life. The `short` key in `TestAOF_CrashInjection_TTL` is the proof — a 150 ms TTL set before SIGKILL is correctly absent on restart, not magically alive.
- **One dispatch path for live and replay.** Same handlers, same canonical-form rewrite. Replay calls `s.dispatch` exactly as ADR-0003 set up; the PXAT token is "just another arg."
- **Backwards compat is free.** Old v1 AOF files written by M3 binaries replay on M4+ binaries without operator intervention. The version byte's gate behaviour is what unlocks this — without it, mixing binaries on the same `dir` would silently divergent-replay.
- **Forwards-incompatibility is honest.** Old binaries refuse to load v2 files at the header check, with a structured error pointing at the version byte. Operators upgrading get a clear "this needs M4+" rather than corrupted state.

**Negative / neutral.**

- A SET with TTL is no longer free at write time — it costs an `int64`-to-decimal conversion + two extra bytes-per-arg for the PXAT token. The AOF record for `SET k v EX 60` is ~30 bytes larger than v1's `SET k v`. For a learning artefact this is irrelevant; flagging it so a future "we got slow" investigation has the right place to look.
- `EXAT`/`PXAT` accept past instants on the wire to keep replay revalidation-free. A client could intentionally set a deadline in the past and immediately observe a tombstone-shaped key. This is Redis-canonical behaviour but worth knowing.
- The post-replay sweeper window: replay can produce entries whose `expireAt` is already past. Lazy expiry catches them on first read; the sweeper catches the rest on its first tick. Brief reads of `DBSIZE` immediately after restart may include not-yet-swept expired keys.

## Alternatives considered

- **Relative `SET k v PX <ms>`.** Rejected: silently extends entry life by the downtime. The single point of this format design is to avoid this.
- **Binary expiry field outside RESP records.** Rejected: breaks ADR-0003's "on-disk format is the wire format" invariant. Would also require a record-length prefix to delimit, which RESP already provides for free.
- **EXPIRE-as-self: rewrite as `SET k <existing-value> PXAT <ms>`.** Rejected: the AOF would need to copy the current value out of the store at append time, making EXPIRE materially heavier than a 3-token record. The PEXPIREAT canonical form keeps EXPIRE constant-size.
- **Single version byte that covers wire compat too.** Rejected: header gating is for the on-disk format, not the wire protocol. PR B exposed PEXPIREAT on the wire one milestone before AOF v2 — the wire is allowed to grow without bumping the disk format.
- **No version bump (just accept v2 records on v1 reader).** Rejected: a v1 reader sees an unknown `PEXPIREAT` command during replay and fails dispatch mid-file. Better to gate at the header with a clear error than half-replay into corrupted state.

## References

- HLD.md (durability §)
- LLD.md §3.1–3.3 (TTL types, lazy expiry, sweeper), §4.1 (AOF format)
- Related ADRs: [[0003-aof-format-and-fsync-policy]]
- The M3 journal entry (`docs/journal/02-aof.md`) predicted "small diff" for the version bump; this ADR records the prediction landing correctly.
