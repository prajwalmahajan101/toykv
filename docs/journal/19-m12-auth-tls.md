# 19 — M12: AUTH + TLS

**Date:** 2026-07-15
**Branch:** `feat/auth-tls` → **[PR #30](https://github.com/prajwalmahajan101/toykv/pull/30)**. Ten planned commits, dependency-ordered: config/flags → connState auth field → dispatch gating → AUTH command → HELLO AUTH wiring → TLS listener → concurrent auth stress → e2e harness → e2e exit criteria → docs; plus ADR-0013 and this entry riding the same branch (user call: ADR + journal in-branch rather than post-merge this time).
**Trigger:** The two documented blockers to any networked deployment — no auth, plaintext transport (SECURITY.md's v1 threat-model rows read "**None.**"). Self-contained connection-layer work; the `connState` seam M10 built for exactly this purpose gets its payoff.

## Decision / surprise

1. **The M10 seam absorbed the whole milestone — zero new mechanisms.** `connState` gained one `bool`, dispatch gained one condition, `HELLO`'s stub branch swapped for a real check, and the listener got wrapped. The plan's "verified against live code" pass found `conn_state.go`'s doc comment already saying *"M12 adds authentication state here"* and `hello.go`'s stub comment-marked *"M12 replaces this branch"* — the milestone was pre-plumbed. Paying the handler-signature churn once in M10 (ADR-0011's bet) meant M12 touched no handler at all.

2. **Gating goes *before* the command-table lookup, not after.** The subtle ordering call in `dispatch`: an unauthenticated client sending `NOSUCHCMD` gets `-NOAUTH`, not `-ERR unknown command` — the command table's contents leak nothing pre-auth. Redis does the same. The gating-matrix test pins it with an explicit unknown-command row.

3. **Pre-authed connState when no requirepass — gating is one condition, not two.** `newConnState(id, s.cfg.RequirePass == "")` marks every connection of an auth-less server authenticated at accept. No `if requirepass != ""` sprinkled anywhere; the no-auth path is provably v1-identical (control case in the matrix test). Same trick auth-exempts AOF replay: `replayApply`'s throwaway state is constructed authenticated — by construction, not by special case.

4. **One `authenticate` helper, two entry points, no oracle.** `AUTH` and `HELLO … AUTH` share it: `subtle.ConstantTimeCompare` on the password; wrong user and wrong password return the *identical* `-WRONGPASS` string. In `cmdHello`, auth commits before the proto switch — a failed `HELLO 3 AUTH default wrong` leaves the connection RESP2 *and* unauthenticated (validate-before-mutate, second exercise after ADR-0011). Alignment fix en route: M10's stub error lacked Redis 7's "Did you mean AUTH \<username\> \<password\>?" hint; both call sites now emit the byte-exact string.

5. **Unauthenticated `PING` is a deliberate Redis deviation — and it was already load-bearing.** Real Redis gates `PING`; the roadmap whitelists it. The e2e harness's readiness probe turned out to *depend* on the whitelist (it PINGs before the test can AUTH), which is the deviation's own argument: liveness checks shouldn't need credentials. Recorded prominently in ADR-0013 §3 as the one operability-over-parity call.

6. **`*tls.Config` in server Config, not file paths.** `main` owns `LoadX509KeyPair` + the pair-or-refuse validation (exit 2, no silent plaintext fallback); `server.Config` carries the built config. Unit tests inject in-memory self-signed certs without touching disk; only the e2e layer writes PEM files (redis-cli needs real paths). The listener wrap itself is two lines — accept loop, EMFILE backoff, and drain are all `net.Listener`-shape agnostic, proven by the drain test holding an open TLS conn through ctx-cancel.

7. **gosec flagged the error string as a hardcoded credential.** G101 on `errNoPassSet` — it contains the word "password". One documented `//nolint` (the repo's existing G204 pattern). Also: `make lint` runs clean, so the M16 release-gate item ".golangci.yml missing `version:` key breaks lint" appears to have been fixed somewhere in M10/M11 — one gate item already closed.

## Why it mattered

- **The owned risk test proves isolation, not locking.** `connState` is single-goroutine by construction, so there's no mutex to test — the stress test (50 concurrent connections: 20 authed hammering SET/GET, 20 never authing and asserting every reply is `-NOAUTH`, 10 authing mid-stream) proves no auth-state bleed and, via a post-hoc sweep, that *no pre-auth mutation ever reached the store*: every `unauth:*` key must be absent, every `mid:*` key must hold a post-AUTH value. Race detector clean.
- **The posture change is real:** SECURITY.md's threat-model rows flip from "**None.**" to concrete mitigations, and "what's safe" gains "a private network you control, with requirepass + TLS". The ceiling now sits at M15's protected mode (the server still won't *stop* you from binding 0.0.0.0 naked — that refusal is the earned 2.0.0 break).

## Code / measurement

- `make ci` green: golangci-lint 0 issues; `go test -race -timeout 5m ./...` — 14/14 packages.
- Manual probe (built binary, `-requirepass s3cret`): unauth `PING` → `PONG`; unauth `GET k` → `(error) NOAUTH Authentication required.` exit 1; `AUTH wrong` → `(error) WRONGPASS invalid username-password pair or user is disabled.`; piped `AUTH s3cret` / `SET k v` / `GET k` → `OK` / `OK` / `"v"`.
- e2e: go-redis round-trips with `Password:` on RESP2 *and* RESP3 (the RESP3 path exercises `HELLO 3 AUTH default <pass>` end-to-end — go-redis authenticates via HELLO, not AUTH, on proto 3); TLS handshake + round-trip via `RootCAs`-pinned self-signed cert; TLS+auth composed. redis-cli cases skip locally (not installed) and run in CI via `redis-tools`.
- TLS negative cases: plaintext client against TLS listener fails cleanly and the server keeps serving; TLS 1.1-capped client rejected (MinVersion 1.2).

## Follow-ups

- **Roadmap M12 row flipped in this branch**; PR number back-filled into the status table, ADR-0013 header, and this entry on merge.
- **`AUTH` brute-force is unthrottled** — no lockout, no rate limit. Documented in SECURITY known-limitations + backlog; a real deployment story wants it before anything internet-adjacent (which remains "do not").
- **`-requirepass` in argv** is visible to `ps`; env/file sourcing is backlog.
- **TUI auth prompt is M14** (TUI currently can't connect to a requirepass server); `INFO`/`SCAN` (M13) are next in sequence.
- **M16 gate bookkeeping:** the lint-config item looks already-fixed — verify and strike it at release time rather than re-fixing.

## Blog-worthy?

The seam story: *"the milestone that shipped in one boolean"* — M10 paying the handler-signature churn so M12 could land auth without touching a single command handler. Pairs with the pre-authed-connState trick (making the common path the degenerate case) and the gating-before-lookup detail as evidence that auth ordering is a design surface, not a checklist item.
