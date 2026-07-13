# ADR-0012: Tagged-union store model & AOF v3 format

- Status: Accepted
- Date: 2026-07-13
- Milestone: M11
- PR: [#28](https://github.com/prajwalmahajan101/toykv/pull/28)

## Context

M11 is the highest-blast-radius milestone in the v2 arc: the store's
string-only value model becomes typed (string / list / hash), sixteen
commands land on the new surface, and the AOF format takes its second
bump. Four load-bearing questions:

1. **How is the union represented in Go?** `entry` must hold exactly one
   of three payload shapes without inviting runtime type-assertion bugs.
2. **What backs a list?** LPUSH cost is not academic: AOF replay of a
   left-heavy workload executes every historical push back-to-back, so an
   O(n) prepend turns replay quadratic.
3. **What do v3 records look like — live and in the rewriter snapshot?**
   v1/v2 established "the on-disk format is the wire format" (ADR-0003)
   and "canonicalize only where determinism demands it" (ADR-0004,
   EX→PXAT). The typed commands must slot into both rules.
4. **What happens to the version byte of an *existing* v1/v2 file?** The
   writer appends to the file it finds. The moment an `LPUSH` record
   lands in a file whose header still says `0x02`, the header lies: a
   pre-M11 binary would accept the header and then die mid-replay on an
   unknown command — precisely the failure the version gate exists to
   prevent.

Inherited constraints: RESP2 golden replies must not drift (ADR-0011's
compat sweep now covers the typed commands too); replay must accept
v1, v2, and v3 files; and TTL state stays uniform across value types
(EXPIRE/PERSIST/TTL are type-agnostic).

## Decision

### 1. Type tag + three concrete fields — no `any`, no interface

```go
type entry struct {
    typ      valueType         // typeString | typeList | typeHash
    str      []byte            // typeString
    list     *deque            // typeList
    hash     map[string][]byte // typeHash
    expireAt time.Time         // uniform across types
}
```

Every typed accessor checks `typ` first and returns `ErrWrongType` on a
mismatch; the server maps it to Redis's byte-exact `-WRONGTYPE …` reply.
The two unused pointer fields per entry are irrelevant at toykv scale,
and in exchange every access is compiler-checked — no type assertion can
panic, no interface boxing.

Semantics (Redis parity): `SET` overwrites any type wholesale;
`GET`/`INCR`/`DECR` on a non-string are `WRONGTYPE`; the generic
commands (`DEL`/`EXISTS`/`EXPIRE`/`TTL`/`PERSIST`/`KEYS`) stay
type-agnostic; and **empty collections never exist** — the last
`LPop`/`RPop`/`HDel` deletes the key.

### 2. Lists ride a growable ring-buffer deque

`internal/store/deque.go` (~100 lines): O(1) push/pop at both ends, O(1)
`at(i)` for LINDEX, Redis-negative-index `rng` for LRANGE. The
motivating case is replay: N historical LPUSHes against a slice would be
O(N²); against the ring they are O(N).

### 3. v3 records: verbatim live, one canonical record per key on rewrite

- **Live appends are the command verbatim** (`LPUSH k a b`, `HDEL h f`).
  Unlike `SET EX`, the typed commands contain no relative time — they
  replay deterministically as-is, so ADR-0003's "on-disk = wire" holds
  with zero canonicalization.
- **The rewriter snapshot emits one record per key**: strings keep
  `renderCanonicalSet` (`SET k v [PXAT ms]`); a list becomes one
  `RPUSH k e1 … eN` (front-to-back preserves order); a hash becomes one
  `HSET k f1 v1 … fN vN`. A typed key with a TTL gets a follow-up
  `PEXPIREAT k ms` — the same canonical TTL form the live EXPIRE path
  appends (shared `renderPExpireAt`), because RPUSH/HSET have no expiry
  clause to fold it into.
- No replayer change: records flow through the normal dispatch-style
  apply path, and the typed commands are simply known now.

### 4. In-place header upgrade on Open

`aof.Open()` on an existing file whose version byte is older than
`CurrentVersion` pwrites the single byte at offset 7 to `0x03` and
fsyncs, *before* accepting any append. Invariant: **a file's header
version ≥ the newest record format it contains, at every instant.** A
pre-M11 binary pointed at the file now refuses it up-front with
`ErrBadVersion` instead of dying mid-replay. The update is a one-byte
pwrite — both the pre- and post-states are valid headers, so there is no
torn-write window. (The append fd is `O_APPEND`, where Go rejects
`WriteAt`; the upgrade uses a dedicated short-lived handle.)

## Consequences

**Positive.**

- **The M10 bet paid off as designed.** `HGETALL` returns `resp.Map(...)`
  and the writer's single downgrade point (ADR-0011) renders `%N` to
  RESP3 clients and a flat array to RESP2 — zero codec changes in M11.
- **Durability is proven where the risk lives.** The milestone-owned
  crash test SIGKILLs a server mid-stream of typed ops and asserts the
  replayed store matches a client-side model of acked mutations exactly,
  under `FsyncAlways` — the M3 discipline extended to the typed surface.
- **Version-byte plumbing survived its second real exercise.** v1/v2
  files replay unchanged; the upgrade path is tested for both.
- **`WRONGTYPE` is enforced at the store boundary**, not per handler —
  one sentinel error, mapped once.

**Negative / neutral.**

- The in-place header upgrade **mutates a file the previous binary
  owned** before the new binary has proven it can serve it. Accepted: the
  mutation is one byte, reversible by definition (flip it back), and the
  alternative (lazy bump on first typed record) adds a stateful check to
  the hot append path for a marginal compat case.
- A rewrite snapshot encodes each list/hash as a **single record**, so a
  huge collection produces one huge RESP array. Bounded in practice by
  the 64 MiB frame cap; chunking (Redis-style 128-element records) is
  deferred until a real workload hits it.
- `HKEYS`/`HVALS`/`HGETALL` order is map-iteration order — unspecified,
  as in Redis. The compat sweep pins bytes using single-field hashes.
- `Get` grew a third return (`value, ok, err`) to carry `WRONGTYPE`,
  churning every call site once. The alternative — a separate
  `GetChecked` — would have left two get paths to keep honest.

## Alternatives considered

- **`value any` + type assertions.** Rejected: every access site gains a
  runtime panic path the compiler cannot see; the three-field struct
  makes the union total and checkable.
- **Sealed interface (`stringVal` / `listVal` / `hashVal`).** Rejected:
  proper union shape, but interface boxing and an extra indirection for
  a 3-variant internal type with exactly one consumer.
- **Plain slice for lists.** Rejected: O(n) LPUSH makes replay of
  left-heavy history quadratic — the exact workload the M11 crash test
  replays.
- **Lazy header bump on first typed record.** Rejected: preserves
  old-binary readability for string-only workloads, but puts a
  have-I-bumped-yet check on the hot append path and leaves a window
  where the guarantee depends on runtime state rather than an Open-time
  invariant.
- **Full rewrite on startup for old files.** Rejected: correct but heavy
  — a BGREWRITEAOF-scale operation to fix one byte.
