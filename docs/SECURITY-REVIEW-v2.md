# toykv v2.0.0 — Security Review (M17 release gate)

**Date:** 2026-07-18 · **Branch:** `feat/release-v2` · **Reviewer:** adversarial agent pass,
findings independently verified against source before action.

## Scope & method

The `2.0.0` major is earned by M15's protected-mode break — the server now refuses a
non-loopback bind without auth/TLS — so "safe-by-default" is the release's central claim. This
review adversarially audits the surface that claim rests on:

- `internal/server/auth.go` — password comparison (timing), no-oracle failure path, secret hygiene.
- `internal/server/dispatch.go` — the pre-auth command gate.
- `cmd/toykv/main.go` — TLS cert/key loading and posture.
- `internal/server/server.go` — connection accept/drain lifecycle.
- `internal/server/protected.go` — protected-mode loopback detection.
- The `internal/resp` codec underneath all of the above (in scope because it runs **before**
  auth on every connection).
- A grep sweep for logged/exposed secrets and hardcoded credentials.

Method: read the actual code (not comments), construct concrete exploit scenarios, and require
positive evidence (file:line) for every "clean" verdict — a clean bill needs proof too.

## Findings

| Severity | Location | Issue | Status |
|---|---|---|---|
| **BLOCKING** | `internal/resp/reader.go:184` | Unbounded array element count → pre-auth memory-amplification OOM | **Fixed** (`7a81176`) |
| **BLOCKING** | `internal/resp/reader.go:185-191` | Unbounded nested-array recursion → pre-auth stack-exhaustion fatal panic | **Fixed** (`7a81176`) |
| Informational | `internal/server/server.go` | No max-concurrent-connections cap (FD-bounded, EMFILE-backed-off) | Accepted for v2; noted |
| Clean | `internal/server/auth.go:33` | Constant-time password compare, no oracle | Verified |
| Clean | `internal/server/dispatch.go:86` | Fail-closed pre-auth gate, fixed 3-verb whitelist | Verified |
| Clean | `cmd/toykv/main.go:155-172` | TLS pair-or-refuse, MinVersion 1.2, no InsecureSkipVerify | Verified |
| Clean | `internal/server/protected.go:67-97` | Fail-closed loopback detection | Verified |
| Clean | `internal/server/server.go:266-285` | Graceful WaitGroup drain, no goroutine leak | Verified |
| Clean | (grep) | No password in logs/INFO/spans; no hardcoded secrets | Verified |

## Blocking findings (fixed before the tag)

### BLOCKING-1 — Unbounded array allocation (`reader.go:184`)
`readArray` bounded only the *bulk-string* length (`MaxBulkSize`); the *array element count*
`n` was checked against negative but had no upper bound, so `make([]Value, n)` ran with `n` up
to `math.MaxInt64`. A ~13-byte header (`*1000000000\r\n`) on a fresh connection drives a
multi-GB allocation **before dispatch, before the auth gate** — a single-packet remote OOM from
any peer that can reach the port, including on loopback/authed/TLS binds (the parse precedes
auth). **Fix:** `MaxArrayLen = 1<<20` (matches Redis `proto-max-multibulk-len`), rejected with
`ErrTooLarge` before the allocation.

### BLOCKING-2 — Unbounded recursion depth (`reader.go:185-191`)
`readArray` re-entered `ReadFrame` per element with no depth counter, so a stream of nested
`*1\r\n` headers exhausted the goroutine stack — an un-recoverable `fatal error` (not catchable
by `recover`), 4 bytes per level. **Fix:** depth threaded through `ReadFrame`/`readArray`;
nesting past `MaxDepth = 32` rejected with `ErrTooLarge`. Inbound commands are flat top-level
arrays, so the bound never rejects legitimate traffic.

Both fixes are localized to `internal/resp/{reader,errors}.go` and ship with regression tests
(`TestReader_ReadFrame_OversizedArray`, `_OverDeepNesting`, `_NestingWithinDepthOK`).

## Clean surface (verified)

- **Auth/timing** — `subtle.ConstantTimeCompare` with no content-dependent short-circuit; the
  username check compares against the public constant `"default"` (no secret); failure emits a
  shared `WRONGPASS` for both bad-user and bad-pass (no oracle); no-password path never sets
  `authenticated`. Password never reaches a span, log, metric, or `INFO`.
- **Pre-auth gate** — runs before the command-table lookup and arity check, on the ASCII-upper
  normalized name, so unknown commands and case variants all get `NOAUTH`; the whitelist is
  exactly `{AUTH, HELLO, PING}`. `HELLO … AUTH` requires the exact token form and leaves proto
  and `authenticated` untouched on a failed AUTH (no partial-auth / proto-switch-on-failure).
  Pipelining just loops `ReadCommand`; each command is independently gated.
- **TLS** — exactly-one-of-cert/key is an error; both-empty is explicit plaintext;
  `LoadX509KeyPair` validates the pair; `MinVersion: tls.VersionTLS12`; no `InsecureSkipVerify`,
  no weak cipher override; handshake forced up-front so bad handshakes fail fast.
- **Accept/drain** — `wg.Add(1)` before spawn, `Done` deferred, `Wait` on both exit paths;
  ctx-cancel closes the listener via `sync.Once`; the per-conn ctx-watcher uses a `done` channel
  to avoid leaking; EMFILE backoff keeps the loop alive. Race-free under `-race`.
- **Protected mode** — fail-closed: empty/unspecified host (`:6390`, `0.0.0.0`, `::`) →
  non-loopback; a hostname is loopback only if **every** resolved IP is loopback; unresolvable
  or ambiguous → non-loopback; refusal happens in `New` before the listener opens or AOF touches
  disk. `-protected-mode` accepts only the yes/no synonym sets.

## Non-blocking / accepted

- **No max-connection cap** (`server.go`). Bounded by the FD limit with EMFILE backoff; combined
  with the 16 KiB per-conn read buffer this is a real but bounded memory story. Acceptable for a
  single-node v2; a `MaxConns` semaphore is a reasonable v2.x hardening item. A `defer recover()`
  in the per-connection goroutine is worthwhile defense-in-depth but does not substitute for the
  two bounds fixes (a fatal stack/OOM is not recoverable) — now moot for the codec vectors.

## Verdict

The auth/TLS/protected-mode surface the gate named is **clean and well-built**. The two blocking
codec DoS vectors the review surfaced underneath it are **fixed and regression-tested** on
`feat/release-v2`. With those fixes in, **zero unwaived blocking findings remain** and the
surface is safe to ship at `v2.0.0`.
