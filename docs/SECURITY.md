# toykv — Security

> v1 is a **learning artefact**, not a production-ready datastore. Read this before deploying it anywhere networked.

## Threat model

| Asset | Threat | v1 mitigation |
|---|---|---|
| Stored values | Unauthorised read | **None.** Bind to localhost by default; document loud-and-clear |
| Stored values | Unauthorised write | Same as above |
| Stored values | Tampering of AOF | File-system permissions on `-dir` |
| Server availability | DoS via huge commands | 64 MiB max bulk string; conn drop on parse error |
| Server availability | DoS via many connections | **No limit in v1.** Documented gap |
| Server availability | Slowloris | **None in v1.** Documented gap |
| Wire transport | Sniffing / MITM | None; use SSH tunnel if exposed |
| Process integrity | Supply chain | Stdlib-only server; TUI deps from `charmbracelet/*` |

## Defaults

- `tinykv -addr :6390` binds to **all interfaces** *if you ask*; the canonical examples use `-addr 127.0.0.1:6390`.
- No auth, no TLS, no IP allowlist.
- AOF file mode: `0600`. Directory: `0700`. Set by the server on creation.
- `BGREWRITEAOF` writes via `0600` tempfile in the same directory.

## What you must not do

- **Do not expose toykv to the public internet.** There is no auth.
- **Do not store secrets in toykv.** There is no encryption at rest.
- **Do not run multiple toykv instances on the same `-dir`.** AOF assumes single-writer.

## What's safe

- Localhost dev tools.
- Test harnesses (CI, integration tests).
- Behind a tightly-scoped private network with auth handled upstream (reverse proxy + mTLS, SSH tunnel).
- As a `tinyraft` (future) state machine, where Raft handles the network.

## Reporting vulnerabilities

Open a **private** GitHub Security Advisory on this repo. Expect a response within 7 days.

Public disclosure: 90 days after report, or coordinated earlier.

## Known limitations (won't fix in v1)

- No connection limit.
- No idle-timeout disconnection.
- `KEYS *` is O(n) — abusable on large keyspaces. (Mitigated by being localhost-only.)
- `INCR`/`DECR` on a 20+ digit string returns `-ERR not an integer` rather than silently truncating; tests verify.
- TUI does not authenticate; it is just a client.

## Post-v1 security backlog

- TLS via Go `crypto/tls` (out of scope; non-trivial dep on cert ops).
- AUTH command (RESP-compatible).
- IP allowlist.
- Per-client connection limit.
- `RENAME` for atomic keyspace edits (currently can be done with `GET` + `SET` + `DEL`, racy).
