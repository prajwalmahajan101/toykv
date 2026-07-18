# 23 — M15: Hardening (protected mode + atomic keyspace ops)

**Date:** 2026-07-18
**Branch:** `feat/hardening` → **[PR #33](https://github.com/prajwalmahajan101/toykv/pull/33)** (rebase-merged, green). Eight commits, dependency-ordered: store `Rename/RenameNX/Copy` → wire handlers + dispatch → `COPY DB 0` fix → protected-mode refusal → concurrent-rename risk test → e2e (refusal + round-trips) → chaos replay → docs. ADR-0016 + this entry are the post-merge follow-ups (same rhythm as M12–M14).
**Trigger:** M15 is the milestone that has to *earn* `2.0.0` rather than default to it. M10–M14 were all additive (opt-in RESP3, backward-compatible AOF v3, opt-in AUTH/TLS) — by semver that's a `1.x`. M15 ships exactly one deliberate break (protected mode) and one correctness win (atomic keyspace ops).

## Decision / surprise

1. **Protected mode refuses to *start*, not to *serve* — and that's the whole point of it being honest.** Real Redis accepts the connection and gates non-loopback *commands*. toykv refuses at boot instead. The decision that made it clean was putting `checkProtectedMode` inside `server.New` (returns an error) rather than inline in `main`: it's unit-testable in isolation *and* protects any embedder, and it runs before AOF replay so an unsafe bind never even touches disk. `main` maps the error to exit 1; a bad `-protected-mode` flag value is a separate class (exit 2), validated in `main` via the exported `ParseProtectedMode`.

2. **The safe default lives in a `string`, not a `bool`.** `Config.ProtectedMode` is a string whose zero value `""` means *enabled*. A bool would default to `false` = *disabled*, so a bare `Config{}` would be unsafe — the exact opposite of the milestone's point. This is the kind of API-shape call that's invisible until someone constructs the struct directly and ships an open bind.

3. **go-redis forced a COPY-semantics change mid-implementation.** The plan said "reject Redis's `DB` option outright." Then the e2e round-trip failed: go-redis v9's `Copy` *always* sends `COPY src dst DB 0`. A blanket DB rejection would break the standard client. The Redis-faithful fix — accept index 0 (toykv's single DB), reject others with `-ERR DB index is out of range` — is both more correct and unblocks go-redis. Committed as its own `fix(...)` so the command commit stayed clean. Lesson: verify the *client's* wire shape, not just the server's spec, before deciding what to reject.

4. **Fresh seq at the destination is a SCAN-contract decision, not a detail.** A renamed/copied key is a newly-appearing key, so it gets `nextSeq()`. If it kept the source's old (low) seq, an in-flight SCAN whose cursor already passed that seq would silently miss it. `store.go`'s own SCAN doc already says "keys created mid-iteration may or may not be returned" — fresh-seq makes RENAME/COPY fit that exactly. This is the boundary that earned ADR-0016 its keyspace half (paired with the protected-mode default).

5. **COPY deep-copy is the subtle store bug that didn't happen because the test looked for it.** A shallow entry copy would alias the source's `*deque`/map; a later `RPUSH`/`HSET` on the source would then corrupt the copy. `entry.clone()` + `deque.clone()` duplicate the payload, and `TestCopy_DeepCopyIsolation` mutates the source *after* copying and asserts the copy is unchanged.

## Why it mattered

- **A major has to be earned, and "earned" is a specific, defensible claim.** The whole framing of M15 is that protected mode is the *one* break that justifies `2.0.0` (RESP3/types/AUTH are additive). Writing that down — and making the code match (refuse-to-start is the break; keyspace ops are additive and don't bump the AOF) — is what keeps the version number honest rather than an epoch bump.
- **"No AOF format bump" is a hard invariant worth a test, not a comment.** RENAME/RENAMENX/COPY are recorded verbatim like DEL and replay under the v3 reader. The chaos test asserts the on-disk header byte is still `0x03` after a crash-restart that replayed them — so a future refactor that quietly introduces a v4 record fails loudly.
- **The safe-default footgun is exactly the kind you only catch by making the unsafe path fail.** Protected mode isn't interesting until you try to start `0.0.0.0` with no auth and the server stops you. The e2e test exercises all four branches (refuse; then requirepass / TLS / loopback / override each start clean).

## Code / measurement

- **Store.** `internal/store/keyspace.go`: `Rename` (move, `ErrNoKey` on miss, self-rename no-op), `RenameNX` (`false` on dest-exists), `Copy` (deep-clone via `entry.clone()`/`deque.clone()`, `ErrSameObject` on `src==dst`, fresh `nextSeq()` at dst). Sentinels `ErrNoKey`/`ErrSameObject` in `errors.go`.
- **Server.** `commands_keyspace.go`: `cmdRename`/`cmdRenameNX`/`cmdCopy`; COPY parses `[DB 0] [REPLACE]` (other DB → out-of-range, unknown → syntax error). `appendIfLive` only on real mutation. Dispatch: `RENAME`/`RENAMENX` arity 3, `COPY` 3–6.
- **Protected mode.** `protected.go`: `ParseProtectedMode` (yes/on/true/1/"" → on; no/off/false/0 → off), `checkProtectedMode`, fail-safe `bindIsLoopback`. Wired in `New` (before replay); `-protected-mode` flag + exit-2 validation + override warning in `main.go`.
- **Tests.** store `keyspace_test.go` (rename/copy semantics, TTL+type travel, deep-copy isolation, fresh-seq-via-SCAN) + `concurrent_test.go` (owned risk: 50 goroutines, exactly-one-winner, survivor keeps TTL/type, `-race`); server `keyspace_test.go` (wire + error strings) + `protected_test.go` (loopback matrix + `New` refusal); e2e `keyspace_test.go` (go-redis + redis-cli) + `protected_test.go` (subprocess refusal + all safe-start branches, via new `RunServerExpectRefusal` + `ServerOpts.BindHost`); chaos `keyspace_replay_test.go` (crash-restart replay + AOF header still v3).
- **Verification:** `go test -race ./...` green across all 14 packages; `make lint` 0 issues; `go test -tags=chaos -short ./test/chaos/...` green. Manual: `0.0.0.0` no-auth → exit 1 + documented message; `-protected-mode bogus` → exit 2; `-protected-mode no` → starts (logged); `127.0.0.1` → starts. CI: all 8 checks green (Ubuntu+macOS × Go 1.25/1.26, lint, chaos-smoke, build).

## Follow-ups

- **ROADMAP M15 → ✅; ADR-0016 + this journal ride the post-merge docs branch.** PR #33 rebase-merged.
- **`RENAME`/`COPY` are not yet in the TUI/CLI surface** — they're server + wire only. A future TUI affordance (rename a focused key) is possible but not scoped.
- **Loopback policy edge:** a hostname resolving to a mix of loopback + routable IPs is treated non-loopback (fail closed). Documented in ADR-0016; the `-protected-mode no` override is the escape hatch.
- **Next: M16 Observability (OpenTelemetry → LGTM).** Instruments the now-final command surface, including these keyspace ops (span per dispatch) and the protected-mode boot path.

## Blog-worthy?

- "How to earn a major version" — the M10–M15 arc where five milestones are additive and *one* (refuse-to-start) is the honest break. Good semver-discipline post.
- The go-redis `COPY DB 0` gotcha: your server spec and your client's wire bytes disagree, and the client wins. Small, concrete, relatable.
- "Fresh seq at the destination" as a worked example of a data-structure choice (insertion-seq SCAN cursor) rippling into command semantics (where does a renamed key land in iteration order).
