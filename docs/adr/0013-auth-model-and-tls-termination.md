# ADR-0013: AUTH model & TLS termination

- Status: Accepted
- Date: 2026-07-15
- Milestone: M12
- PR: [#30](https://github.com/prajwalmahajan101/toykv/pull/30)

## Context

M12 lifts v1's localhost-only ceiling: no authentication and plaintext
transport were the two documented blockers to any networked deployment
([SECURITY](../SECURITY.md)). The load-bearing questions:

1. **What auth model** — Redis 6 ACLs (users, categories, key patterns)
   or the classic single-`requirepass` model?
2. **Where does gating live** — per handler, or one chokepoint?
3. **What may an unauthenticated connection do?** Redis answers "almost
   nothing" (even `PING` is gated); the roadmap mandates a `{AUTH,
   HELLO, PING}` whitelist.
4. **How does TLS attach** — a separate listener/port, a proxy
   recommendation, or wrapping the existing accept loop?
5. **How do AUTH failures avoid becoming an oracle** — timing or
   error-string differences that leak which part of the credential was
   wrong?

Constraints inherited from M10 (ADR-0011): per-connection state lives on
`connState`, dispatch is the single chokepoint that already threads it,
and `HELLO … AUTH u p` is grammar-parsed but stub-rejected, comment-marked
for M12. The M3 durability contract (mutate → AOF → reply) and the AOF
replay path (`replayApply` dispatches through the same table) must be
unaffected.

## Decision

**Single-password `requirepass` model, gated at dispatch, shared
`authenticate` helper, TLS as a listener wrap.**

### 1. Auth model: `requirepass` + implicit `default` user

One server-wide password (`-requirepass`). `AUTH password` and
`AUTH username password` are both accepted; the only valid username is
`default`, exactly Redis's single-password compatibility surface. ACLs
are rejected as v2 scope — toykv has no multi-tenant story, and the ACL
model (users × categories × key patterns) is an order of magnitude more
surface than "deployable single node" needs.

### 2. Gating at dispatch, before the table lookup

One condition in `dispatch`, ahead of the command-existence check:

```go
if !cs.authenticated && name != "AUTH" && name != "HELLO" && name != "PING" {
    return resp.Error("NOAUTH Authentication required.")
}
```

- `connState.authenticated` is pre-set `true` when `RequirePass == ""`,
  so the no-auth configuration costs one boolean test and nothing else.
- Gating **precedes** the table lookup: an unauthenticated client gets
  `-NOAUTH` for unknown commands too, learning nothing about the
  command table.
- `replayApply` constructs its throwaway `connState` as authenticated —
  AOF replay is auth-exempt by construction, not by a special case in
  the handlers.

### 3. The whitelist: `AUTH`, `HELLO`, `PING` — a documented Redis deviation

Real Redis gates `PING`. The roadmap deliberately whitelists it: the e2e
harness readiness probe, load-balancer health checks, and "is it up"
scripting all want an unauthenticated liveness check, and `PING` leaks
nothing about stored data. `HELLO` (without AUTH) similarly succeeds and
returns the handshake map — server name/version/proto, nothing secret.
This is the one place M12 chooses operability over strict parity, and it
is recorded here precisely because it *is* a deviation.

### 4. One `authenticate` helper for both entry points

`AUTH` and `HELLO … AUTH` call the same
`authenticate(s, cs, user, pass)`:

- `crypto/subtle.ConstantTimeCompare` on the password. The username
  check is a plain compare — the only valid username is the public
  constant `default`, so its timing carries no secret.
- Wrong user and wrong password return the identical
  `-WRONGPASS invalid username-password pair or user is disabled.` — no
  oracle for which half failed.
- Error strings are Redis 7 byte-exact, including the
  "Did you mean AUTH <username> <password>?" hint (M10's stub lacked
  it; M12 aligned both call sites).
- In `cmdHello`, `authenticate` runs **before** the protocol switch
  commits — a failed HELLO AUTH changes neither auth state nor proto
  (the validate-before-mutate rule from ADR-0011). A successful `AUTH`
  persists across later `HELLO` calls; `HELLO` never de-authenticates.

### 5. TLS: wrap the listener, hold `*tls.Config` in server Config

```go
l, err := net.Listen("tcp", s.cfg.Addr)
if s.cfg.TLS != nil { l = tls.NewListener(l, s.cfg.TLS) }
```

Everything downstream — accept loop, EMFILE backoff, ctx-cancel drain —
is `net.Listener`-shape agnostic and untouched. `cmd/toykv` builds the
config from `-tls-cert`/`-tls-key` (`MinVersion: tls.VersionTLS12`) and
exits non-zero if only one flag is given: no silent plaintext fallback.
`server.Config` carries the built `*tls.Config` rather than file paths
so tests inject in-memory self-signed certs without touching disk.

## Consequences

**Positive.**
- The no-`requirepass` path is behaviourally identical to v1 (proven by
  the gating-matrix test's control case).
- Auth state needs no locking: `connState` is single-goroutine by
  construction. The M12 owned risk test (50 concurrent connections,
  authed/unauthed/mid-stream cohorts, `-race`) proves isolation rather
  than mutex discipline.
- TLS and auth compose freely with each other and with every fsync
  policy; the AOF layer never sees either.
- M15's protected mode has its hook: `RequirePass`/`TLS` presence on
  Config is exactly what the bind-refusal check will read.

**Negative / accepted.**
- No rate limit or lockout on `AUTH` attempts — brute force against a
  weak password is only slowed by the network. Documented in SECURITY;
  backlog item.
- `-requirepass` is visible in the process argv (`ps`). Env/file
  sourcing is backlog.
- Unauthenticated `PING`/`HELLO` deviates from Redis; a compliance
  test-suite run against toykv will flag it. Accepted per roadmap.
- Single user, no ACLs — any authenticated client has full access,
  including `FLUSHDB`.

**Neutral.**
- TLS termination in-process (vs "use a proxy") adds ~40 lines and zero
  deps (stdlib), and is what makes `redis-cli --tls` work directly.

## Alternatives considered

- **Redis-6-style ACLs.** Massive surface (users, command categories,
  key patterns) for a single-tenant toy; `requirepass` is the documented
  compatibility subset Redis itself keeps for this exact use case.
- **Gating inside each handler.** 30+ call sites, one forgotten handler
  = auth bypass. The dispatch chokepoint is the only defensible shape.
- **Strict Redis parity (gate `PING`).** Breaks the e2e readiness probe
  and unauthenticated health checks; roadmap explicitly chose the
  whitelist. Revisit if a compliance suite ever matters more than
  operability.
- **Separate TLS port (Redis's `tls-port`).** Two listeners, two drain
  paths, for a feature nobody asked of a single-node toy. One listener,
  one transport per process.
- **Recommending an external TLS proxy instead of in-process TLS.**
  Punts the roadmap exit criterion (`redis-cli --tls` round-trip) and
  every deployment inherits a second moving part.

## References

- ROADMAP §M12 (scope, whitelist mandate, owned risk test)
- LLD §5.5 (implementation shape)
- SECURITY.md (posture, known limitations, backlog)
- Related ADRs: [0011](./0011-resp3-negotiation-and-protocol-state.md)
  (connState seam, validate-before-mutate), [0003](./0003-aof-format-and-fsync-policy.md)
  (durability contract unaffected)
