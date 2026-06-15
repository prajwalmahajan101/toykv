# 05 — M4 PR B merged (TTL commands + sweeper lifecycle)

**Date:** 2026-06-11
**Merge:** PR #11 (`feat/ttl-commands` → `main`), rebase, 3 commits.

## Decision / surprise

The PR shipped the predicted asymmetry — TTLs work in memory, do **not** survive restart — and it shipped without any pre-merge handwringing about it. The asymmetry has a single owner: `TestPRB_AOFAppendsAreV1Shape`. The test asserts the asymmetry literally. When PR C lands the v2 record format, that test will fail and the diff will include deleting it. That's the kind of "this is a temporary state and here's how we'll know it's gone" lever I want every multi-PR milestone to have.

The surprise was small but worth noting: the **arity gate fired before the parser**. SET with two expiry tokens (`SET k v EX 10 PX 10`) hit `maxArgs=6` and got `ERR wrong number of arguments` instead of `syntax error`. Fix: drop SET's upper arity cap and let `parseSetOptions` be the single source of SET syntax errors. One-line code change, but worth thinking about: dispatch-level arity is a coarse filter, and for commands with rich option grammars it's better to push validation into the parser and present one consistent error vocabulary at the wire.

## Why it mattered

A consistent error vocabulary matters because **redis-cli scripts grep error strings**. If a SET with a typo'd token returns one error, and the same typo with a different argv length returns a different error, automation breaks in ways that look like "Redis acting weird."

## Code / measurement

- Commits: `e908064` `e1cd0d2` `bd78b6b`.
- New file: `internal/server/ttl_commands_test.go` — 339 lines covering every new wire command + the asymmetry contract.
- `cmdSet` parses `[NX|XX] [EX|PX|EXAT|PXAT N]` in any token order; one-of-each-group constraint enforced in the parser.
- `EX`/`PX` reject `n ≤ 0` → `ERR invalid expire time in 'set' command` (Redis-canonical).
- `EXAT`/`PXAT` accept any absolute instant including the past — so PR C's replay path never needs a separate validation rule.
- TTL/PTTL return `-2`/`-1` per Redis when key missing / no-expiry; truncates remaining duration to the requested unit.

## Blog-worthy?

Two:

1. **"Arity gates lie."** They surface "wrong number of arguments" for what is actually a duplicate-token bug. For SET, the right call is a thin dispatch and a chunky parser. For PING, the cap is fine. Boundary depends on the command's option grammar.
2. **"Ship the asymmetry with a test that pins it."** PR B intentionally ships a broken-on-restart TTL. The asymmetry has a name (`TestPRB_AOFAppendsAreV1Shape`) and a known end-state (deleted in PR C). This is the half-finished implementation discipline that "no half-finished" rules can't capture — the half-finishedness is the *point*, and the test is the contract that says "we have not forgotten about this."

## What's next

- PR C — AOF v2 + canonical PXAT append + new crash test + ADR-0004 + ROADMAP flip + tag `m4`. The asymmetry test will fail and gets deleted.
- After M4 ships, M5 (compaction) starts. Predicted shape: same three-PR split, with the BGREWRITEAOF crash test as the milestone-owned risk.
