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
- Auth and TLS are **opt-in** (`-requirepass`, `-tls-cert`/`-tls-key`). A non-loopback bind without either still starts in M12 — the safe-by-default refusal (**protected mode**) lands in M15 and is the deliberate breaking change that earns the `2.0.0` tag.
- `-tls-cert` and `-tls-key` must be given as a pair; the server exits non-zero otherwise (no silent plaintext fallback).
- Unauthenticated `PING` is allowed by design (health checks, readiness probes) — a documented deviation from Redis; see [ADR-0013](./adr/0013-auth-model-and-tls-termination.md).
- AOF file mode: `0600`. Directory: `0700`. Set by the server on creation.
- `BGREWRITEAOF` writes via `0600` tempfile in the same directory.

## What you must not do

- **Do not expose toykv to the public internet**, even with auth — there is no lockout, no rate limit on `AUTH` attempts, and no audit log.
- **Do not run a non-loopback bind without `-requirepass` and TLS.** Until M15's protected mode, the server will not stop you.
- **Do not store secrets in toykv.** There is no encryption at rest.
- **Do not run multiple toykv instances on the same `-dir`.** AOF assumes single-writer.
- **Do not pass `-requirepass` on shared machines carelessly** — the password is visible in the process list (`ps`). An env/config alternative is backlog.

## What's safe

- Localhost dev tools.
- Test harnesses (CI, integration tests).
- A private network you control, with `-requirepass` + TLS enabled — the M12 posture.
- Behind a tightly-scoped private network with auth handled upstream (reverse proxy + mTLS, SSH tunnel).
- As a `tinyraft` (future) state machine, where Raft handles the network.

## Reporting vulnerabilities

Open a **private** GitHub Security Advisory on this repo. Expect a response within 7 days.

Public disclosure: 90 days after report, or coordinated earlier.

## Known limitations

- No connection limit.
- No idle-timeout disconnection.
- No rate limit / lockout on failed `AUTH` attempts.
- Single password, single implicit `default` user — no ACLs.
- `-requirepass` value appears in the process argv.
- `KEYS *` is O(n) — abusable on large keyspaces (SCAN lands in M13).
- `INCR`/`DECR` on a 20+ digit string returns `-ERR not an integer` rather than silently truncating; tests verify.
- TUI does not authenticate yet; the AUTH prompt lands in M14.

## Security roadmap (v2 cycle)

- ~~TLS via Go `crypto/tls`~~ — **shipped in M12**.
- ~~AUTH command (RESP-compatible)~~ — **shipped in M12**.
- Protected mode: refuse non-loopback bind without auth/TLS — **M15** (the earned `2.0.0` break).
- `RENAME`/`RENAMENX`/`COPY` for atomic keyspace edits — **M15**.
- IP allowlist — backlog.
- Per-client connection limit — backlog.
- `AUTH` attempt rate-limiting / lockout — backlog.
- Password via env var or file instead of argv — backlog.
