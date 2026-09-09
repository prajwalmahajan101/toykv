# toykv — Security

> toykv started as a **learning artefact**. As of M12 (v2 cycle) it has authentication and TLS, which lifts the localhost-only ceiling to "deployable on a network you control" — but it is still not a hardened production datastore. Read this before deploying it anywhere networked.

## Threat model

| Asset | Threat | Mitigation (M12) |
|---|---|---|
| Stored values | Unauthorised read | `-requirepass` + `AUTH` gating: unauthenticated connections may run only `AUTH`/`HELLO`/`PING`, everything else returns `-NOAUTH` |
| Stored values | Unauthorised write | Same gating; pre-auth mutations are rejected before dispatch and never reach the store or AOF |
| Credentials | Password oracle / timing | Constant-time compare (`crypto/subtle`); wrong user and wrong password return the identical `-WRONGPASS`; passwords never logged |
| Stored values | Tampering of AOF | File-system permissions on `-dir` |
| Server availability | DoS via huge commands | 64 MiB max bulk string; conn drop on parse error |
| Server availability | DoS via many connections | **No limit.** Documented gap |
| Server availability | Slowloris | **None.** Documented gap |
| Wire transport | Sniffing / MITM | `-tls-cert`/`-tls-key` terminate TLS (stdlib `crypto/tls`, min TLS 1.2) |
| Process integrity | Supply chain | Stdlib-only server; TUI deps from `charmbracelet/*` |

## Defaults

- `toykv -addr :6390` binds to **all interfaces** *if you ask*; the canonical examples use `-addr 127.0.0.1:6390`.
- Auth and TLS are **opt-in** (`-requirepass`, `-tls-cert`/`-tls-key`). Since M15, **protected mode** makes the default safe: the server refuses to *start* on a non-loopback bind that has neither auth nor TLS (exit non-zero, message names the fix). Override with `-protected-mode no`. This is the deliberate breaking change that earns the `2.0.0` tag — see [ADR-0016](./adr/0016-protected-mode-and-atomic-keyspace-ops.md).
- `-tls-cert` and `-tls-key` must be given as a pair; the server exits non-zero otherwise (no silent plaintext fallback).
- Unauthenticated `PING` is allowed by design (health checks, readiness probes) — a documented deviation from Redis; see [ADR-0013](./adr/0013-auth-model-and-tls-termination.md).
- AOF file mode: `0600`. Directory: `0700`. Set by the server on creation.
- `BGREWRITEAOF` writes via `0600` tempfile in the same directory.

## What you must not do

- **Do not expose toykv to the public internet**, even with auth — there is no lockout, no rate limit on `AUTH` attempts, and no audit log.
- **Do not run a non-loopback bind without `-requirepass` and TLS.** Since M15, protected mode refuses to start such a bind unless you explicitly pass `-protected-mode no` — do not disable it to work around the refusal.
- **Do not store secrets in toykv.** There is no encryption at rest.
- **Do not run multiple toykv instances on the same `-dir`.** AOF assumes single-writer.
- **Do not pass `-requirepass` on shared machines carelessly** — the password is visible in the process list (`ps`). An env/config alternative is backlog.

## What's safe

- Localhost dev tools.
- Test harnesses (CI, integration tests).
- A private network you control, with `-requirepass` + TLS enabled — the M12 posture.
- Behind a tightly-scoped private network with auth handled upstream (reverse proxy + mTLS, SSH tunnel).
- As a replicated cluster (`-replicate`, v3) **on a trusted private network** — the Raft peer transport is plaintext; see [Cluster / replication](#cluster--replication-v3).

## Observability & telemetry (M16)

OpenTelemetry is **off unless `-otel-endpoint` is set**; nothing leaves the
process by default. When enabled:

- **No secrets in signals.** Passwords never appear in any log, span, or metric
  — the `auth attempt` log records only `result`, mirroring the M12 no-oracle
  rule. Key **names and values never appear in metric labels** (they would be
  unbounded user data); the only labels are a fixed low-cardinality set
  (`command`, `status`, `kind`, `policy`, `result`, `proto`).
- **Key capture is opt-in and hashed.** With `-otel-capture-keys` off (default)
  store spans record no key; on, they record a **salted SHA-256** truncated hex,
  never the plaintext key or value.
- **Telemetry never fails a command.** A dead OTLP collector logs `otel export
  failed (dropped)` and drops the batch — the command still succeeds. Export is
  async; a slow/unreachable endpoint cannot stall or error a client request.
- **The local stack is local-only.** `deploy/otel-lgtm/` runs Grafana with
  anonymous admin and open OTLP ports for convenience — never expose that
  container beyond localhost.

## Cluster / replication (v3)

`-replicate` embeds [ToyRaft](https://github.com/prajwalmahajan101/toyraft) and, for a
multi-node cluster, opens an HTTP peer transport (`-raft-addr`) that carries the Raft log
between nodes. **This peer transport is plaintext and unauthenticated** — ToyRaft's threat
model is a *trusted network*. Anyone who can reach the raft port can read replicated data,
inject log entries, or impersonate a peer. There is no auth, no TLS, and no per-peer identity
check on that plane.

- **Raft-bind guard (M23).** A multi-node cluster **refuses to start** when its effective raft
  bind (`-raft-addr`, or the self entry in `-peers`) is a non-loopback address, unless you pass
  `-raft-insecure` to acknowledge the trusted-network posture. Loopback raft binds (same-host
  multi-process testing) start clean. This is independent of `-protected-mode`: a plaintext peer
  bind on a public interface is unsafe regardless of the client-bind knob.
- **`-raft-insecure` is not "make it secure" — it is "I accept the plaintext peer plane."** Only
  set it when every peer sits on a private network you control (VPC, private subnet, WireGuard /
  IPsec mesh, or an SSH-tunnelled overlay). It logs a warning at startup.
- **The client plane still applies.** `-requirepass` / TLS / protected mode gate client
  connections (`-addr`) exactly as in v2; the raft guard is an additional, separate check for
  the peer plane. A cluster that serves clients over TLS still ships raft traffic in cleartext.
- **Do not expose the raft port to any untrusted network**, with or without `-raft-insecure`.
  mTLS / auth on the peer transport is a **v4 deferral** (gated on ToyRaft transport security —
  see the ROADMAP v4 table). See [ADR-0019](./adr/0019-cluster-mode-transport-and-raft-log-storage-layout.md).

## Reporting vulnerabilities

Open a **private** GitHub Security Advisory on this repo. Expect a response within 7 days.

Public disclosure: 90 days after report, or coordinated earlier.

## Known limitations

- No connection limit.
- No idle-timeout disconnection.
- No rate limit / lockout on failed `AUTH` attempts.
- Single password, single implicit `default` user — no ACLs.
- `-requirepass` value appears in the process argv.
- `KEYS *` is O(n) — abusable on large keyspaces; use `SCAN` (shipped M13) for cursor-based iteration.
- `INCR`/`DECR` on a 20+ digit string returns `-ERR not an integer` rather than silently truncating; tests verify.

## Security roadmap (v2 cycle)

- ~~TLS via Go `crypto/tls`~~ — **shipped in M12**.
- ~~AUTH command (RESP-compatible)~~ — **shipped in M12**.
- ~~Protected mode: refuse non-loopback bind without auth/TLS~~ — **shipped in M15** (the earned `2.0.0` break; override `-protected-mode no`).
- ~~`RENAME`/`RENAMENX`/`COPY` for atomic keyspace edits~~ — **shipped in M15** (single store-mutex ops, TTL + type preserved).
- ~~OpenTelemetry observability with a privacy-safe signal model~~ — **shipped in M16** (off by default; no secrets/keys in signals; opt-in salted key-hash; telemetry never fails a command).
- ~~RESP codec pre-auth DoS bounds~~ — **shipped in M17**: the frame decoder now caps array element count (`MaxArrayLen` = 1 048 576) and nesting depth (`MaxDepth` = 32), rejecting with `ErrTooLarge` before any allocation. Closes a single-packet memory-amplification OOM and a stack-exhaustion panic reachable *before* the auth gate. Found by the **v2.0.0 release-gate security review** — the full audit (and the clean bill on the auth/TLS/protected-mode surface it was scoped to) is in [SECURITY-REVIEW-v2.md](./SECURITY-REVIEW-v2.md).
- IP allowlist — backlog.
- Per-client connection limit — backlog.
- `AUTH` attempt rate-limiting / lockout — backlog.
- Password via env var or file instead of argv — backlog.
