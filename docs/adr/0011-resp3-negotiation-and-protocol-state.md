# ADR-0011: RESP3 negotiation & per-connection protocol state

- Status: Accepted
- Date: 2026-07-13
- Milestone: M10
- PR: (to be filled on merge)

## Context

M10 opens the v2 arc by adding RESP3 as the wire foundation the typed
replies of M11 will ride. Four load-bearing questions:

1. **How does a connection opt into RESP3, and where does the negotiated
   version live?** RESP3 must be opt-in — a default `redis-cli` (RESP2)
   and every v1 client must keep working byte-for-byte.
2. **Where do the RESP2→RESP3 differences get resolved?** Per handler, or
   at a single encoding point?
3. **Does the inbound reader need to change?** RESP3 introduces new frame
   types (`%`, `~`, `,`, `#`, `_`, `=`, `>`).
4. **Do existing commands emit richer frames now, or only once M11 gives
   them typed values?**

The constraint inherited from the RESP2 subset (HLD/LLD §2; the ADR-0001
slot was reserved but never written) is that RESP2 replies are a golden
contract: they may not drift. RESP3 has to be additive under that
contract.

A key observation bounds the whole milestone: **clients always send
commands as RESP2 arrays of bulk strings regardless of the negotiated
protocol.** RESP3 changes replies and adds out-of-band push frames — it
never changes how commands are *sent*. So the reader is out of scope; M10
is reply-side + negotiation only.

## Decision

**Four coupled rules.**

### 1. Opt-in via `HELLO`, state on a per-connection `connState`

`HELLO [protover [AUTH username password]]` negotiates the protocol.
A new `connState` (created per accepted connection in `handleConn`) holds
the negotiated `proto` and the connection `id`. It is threaded through
`dispatch` to every handler; `HELLO` is the only M10 command that reads or
writes it. The default is `Proto2` until a successful `HELLO 3`.

`connState` is deliberately the seam M12 (AUTH) needs: authentication
state lands on the same struct, and command gating lands in `dispatch`
where the state is already in hand. Paying the (mechanical) handler-
signature change once, in M10, avoids a second churn in M12.

### 2. The Writer is the single downgrade point

Handlers return protocol-agnostic `resp.Value`s using rich kinds
(`Map`, `Set`, `Double`, `Boolean`, `Null`, `Verbatim`, `Push`).
`Writer.WriteFrameProto(v, proto)` emits native RESP3 frames at `Proto3`
and downgrades each rich kind to its RESP2 equivalent at `Proto2` — map→
flat array, set/push→array, double→bulk string, boolean→`:1`/`:0`,
null→`$-1`, verbatim→bulk string. This mirrors Redis's internal
`addReply*` design and is exactly what M11's typed replies reuse.
`WriteFrame(v)` remains the RESP2 default for the inherently-RESP2
producers — AOF record encoding and the outbound `internal/client`
command path — which never negotiate RESP3.

### 3. The reader is untouched

Because commands arrive as RESP2 arrays at every protocol version, the
RESP3 frame types are write-only in M10. No reader change, no new parse
paths, materially smaller blast radius.

### 4. Existing commands migrate only their logical nulls

M10 ships the full encoder set but does not invent typed replies for
string commands (that is M11). The one exception is the logical null:
`GET` on a miss and `SET NX/XX` on a failed condition move from
`NullBulk` to `Null()`. A RESP3 client now receives `_`; a RESP2 client
still receives `$-1`, byte-identical to v1. This makes the compat sweep
test a real RESP2-vs-RESP3 divergence on a common command and lets
go-redis `Protocol: 3` behave naturally, at the cost of touching two
handlers — all guarded by the writer's single downgrade point.

## Consequences

**Positive.**

- **RESP2 is provably unchanged.** The dual-protocol compat sweep asserts
  every PRD §5.1 command's exact RESP2 wire bytes, and the writer goldens
  assert every RESP2 downgrade — so the ADR-0001 golden contract is
  mechanically enforced, not hoped for.
- **One decision point for the wire.** A handler never branches on
  protocol; it returns the richest value it has and the writer resolves
  the rest. M11 adds `HGETALL`→map with zero writer changes.
- **`connState` is the M12 seam.** AUTH state and command gating slot onto
  the exact plumbing M10 lays down.
- **Small surface.** Reader untouched; `HELLO` is the only new command;
  two handlers migrate a null.

**Negative / neutral.**

- Every handler signature gained a `*connState` parameter that most ignore
  (`_ *connState`). This is visible churn for a value few handlers read
  today — justified by M12/M13 needing it, but noted honestly.
- `serverVersion` is a placeholder const (`2.0.0-dev`) until M15 wires the
  ldflags build version through `Config`, matching the CLI/TUI plumbing.
- The AUTH clause of `HELLO` is parsed but non-functional in M10 (errors
  as Redis does with no `requirepass`). The grammar exists a milestone
  before its behaviour — the same "wire grows before the feature" pattern
  ADR-0004 used for `PEXPIREAT`.
- RESP3 is reply-only: the `>` push frame has an encoder but no producer
  until v3 pub/sub. Scaffold now, use later — flagged so a future reader
  does not mistake it for dead code.

## Alternatives considered

- **Special-case `HELLO` outside the command table.** Rejected: it avoids
  the handler-signature change but forfeits the `connState`-in-`dispatch`
  seam that M12's command gating and M13's proto-aware `INFO` both need.
  The churn is paid once here instead of re-litigated in M12.
- **Per-handler protocol branching.** Rejected: leaks the wire protocol
  into every command and multiplies the RESP2-drift risk across N call
  sites instead of concentrating it in one downgrade function.
- **Teach the reader RESP3.** Rejected as unnecessary: commands are RESP2
  arrays at every protocol version. Parsing RESP3 inbound would be dead
  code until a client sent a RESP3 command, which the protocol never
  requires.
- **Keep all existing commands byte-identical across both protocols
  (defer nulls to M11).** Rejected: it would make "RESP3 client gets
  richer frames" true only for `HELLO`, and leave go-redis `Protocol: 3`
  receiving deprecated `$-1` for a miss. Migrating the null is low-cost
  and makes the compat guarantee testable on a real command.
- **Bump the AOF format for RESP3.** Not applicable and explicitly
  avoided: RESP3 is wire-only. The AOF format bump belongs to M11 (typed
  records, v3), keeping v1's discipline of one format change per milestone
  that needs it.

## References

- HLD.md (wire protocol §)
- LLD.md §2 (RESP codec)
- PRD.md §5.1 (command surface — the compat-sweep target)
- docs/ROADMAP.md §M10
- Related ADRs: [[0003-aof-format-and-fsync-policy]] (the on-disk/wire
  boundary this ADR keeps RESP3 out of), [[0004-ttl-canonical-pxat-encoding]]
  (the "wire grows before the disk format / feature" precedent)
