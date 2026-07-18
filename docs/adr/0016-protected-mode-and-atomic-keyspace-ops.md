# ADR-0016: Protected-mode default & atomic keyspace ops

- Status: Accepted
- Date: 2026-07-18
- Milestone: M15
- PR: [#33](https://github.com/prajwalmahajan101/toykv/pull/33)

## Context

M15 is the milestone that has to *earn* the `2.0.0` major rather than default
to it. Everything in M10–M14 was additive — opt-in RESP3, backward-compatible
AOF v3, opt-in AUTH/TLS — so by semver alone the v2 arc is a `1.x`. Two calls
in M15 draw real boundaries worth recording:

1. **The deployment-safety default.** v1's implicit contract was "bind
   anywhere, serve anyone." M12 shipped auth and TLS but left them *opt-in* — a
   non-loopback bind with neither still started and served every client. That
   is the exact footgun the "deployable, safe-by-default" v2 goal exists to
   close, and closing it is a behavioural break.

2. **Atomic keyspace edits.** `RENAME`/`RENAMENX`/`COPY` were expressible only
   client-side as a racy `GET`+`SET`+`DEL` dance — non-atomic, TTL-lossy, and
   wrong under concurrent mutation. Promoting them into the server is a
   correctness fix, but it raises two design questions the store model forces:
   what sequence does a moved key get (SCAN's cursor is a seq — ADR-0014), and
   does the AOF format have to change to carry the new records.

## Decision

**Protected mode refuses to *start* an unsafe bind; atomic keyspace ops are
single store-mutex moves that ride the existing AOF v3 format verbatim.**

### Protected mode

By default the server **refuses to start** when bound to a non-loopback address
with neither `requirepass` nor TLS configured, exiting non-zero with a message
that names the fix. The check (`checkProtectedMode`) runs inside `server.New`,
*before* AOF replay and before the listener opens, so an unsafe bind never
touches disk and any embedder — not just the CLI — is protected.

- **Refuse-to-start, not refuse-to-serve.** Real Redis's protected mode still
  accepts the connection and only rejects non-loopback *commands*. toykv
  surfaces the unsafe posture at boot instead — simpler, and it fails the
  operator loudly at the moment they can fix it. This is a deliberate,
  documented deviation.
- **Fail-safe loopback detection.** An empty or unspecified host (`:6390`,
  `0.0.0.0`, `::`) is treated as non-loopback (the all-interfaces case);
  `localhost` and any loopback IP are loopback; a hostname is loopback only when
  *every* resolved address is (an unresolvable host is treated as non-loopback).
- **Two error classes.** `-protected-mode yes|no` is validated in `main` (a bad
  value is a usage error → exit 2); the unsafe-bind refusal is a deployment
  error raised by `New` → exit 1. Override with `-protected-mode no` (logged at
  startup). The `Config.ProtectedMode` field is a `string` whose zero value
  (`""`) means *enabled*, so a bare `Config{}` is safe by default.

### Atomic keyspace ops

`Rename`/`RenameNX`/`Copy` are single `sync.Mutex`-guarded operations in
`internal/store/keyspace.go`. The value payload, TTL (`expireAt`), and value
type travel with the key inside the moved `entry`.

- **Fresh seq at the destination.** The destination is a newly-appearing key, so
  it receives `nextSeq()`. This keeps it consistent with SCAN's "keys created
  mid-iteration may or may not be returned" guarantee (ADR-0014): a moved key
  never hides behind a cursor that already passed a stale low seq.
- **Move vs. deep-copy.** `RENAME` moves the entry (the source is deleted, so a
  pointer move is safe); `COPY` deep-copies the deque/map so later mutation of
  the source can never leak into the copy.
- **No AOF format bump.** The three commands are recorded verbatim (like `DEL`)
  and replayed deterministically through the normal dispatch path under the v3
  reader. The header version and record shape are untouched (ADR-0012 stands).
  A no-op self-`RENAME` and a `:0` `RENAMENX`/`COPY` skip the append entirely.
- **Single-DB `COPY DB`.** `COPY` accepts `DB 0` (which every real client,
  including go-redis, sends by default) and rejects any other index with
  `-ERR DB index is out of range`; unknown tokens are `-ERR syntax error`.

## Consequences

**Positive.** The `2.0.0` major is honest — one deliberate breaking change
(protected mode) plus one correctness win (atomic keyspace ops), not
feature-count padding. Safe-by-default closes the single biggest deployment
footgun. Keyspace edits are now atomic and TTL/type-preserving, and because they
ride v3 verbatim, a v1/v2/v3 AOF still replays with no new format to support.

**Negative / neutral.** Protected mode is a behavioural break: an operator who
relied on v1 serving `0.0.0.0` with no auth must now set auth/TLS, bind
loopback, or pass `-protected-mode no`. The loopback policy can refuse a
hostname bind the operator considered safe (mixed loopback/routable resolution
→ non-loopback); the override is the escape hatch and the message says so. A
non-`localhost` hostname bind pays one `net.LookupIP` at boot (rare). `COPY`'s
deep-copy is O(n) in the source size — expected for a copy.

## Alternatives considered

- **Refuse-to-serve (Redis's model).** Rejected: per-command gating on the bind
  interface is more code and hides the misconfiguration until first traffic;
  refuse-to-start fails at the fixable moment.
- **Reject `COPY DB` outright.** Rejected: go-redis always sends `DB 0`, so a
  blanket rejection breaks the standard client round-trip. Accepting index 0 and
  rejecting others is Redis-faithful and single-DB-honest.
- **Keep the destination's source seq on RENAME.** Rejected: a moved key with a
  stale low seq can silently vanish from an in-flight SCAN; a fresh seq matches
  the "newly-appearing key" semantics SCAN already documents.
- **A `Config.ProtectedMode bool`.** Rejected: a bool zero-value would default to
  *disabled*, making `Config{}` unsafe — the opposite of the milestone's point.

## References

- ROADMAP.md §M15 (the earned `2.0.0`), §"Breaking risk"
- LLD.md §5.6 (protected-mode boot check + atomic keyspace ops)
- SECURITY.md (safe-by-default posture, `-protected-mode` override)
- Related ADRs: [[0012]] (tagged-union store & AOF v3 — the format this does not
  bump), [[0013]] (AUTH/TLS — the config protected mode gates on), [[0014]]
  (SCAN insertion-seq cursor — the contract fresh-seq-at-dst preserves)
