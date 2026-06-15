# 00 — Kickoff (M2: Store core + concurrent commands)

**Date:** 2026-06-11

## Where we are

- M0 (skeleton) and M1 (RESP codec + PING/ECHO) merged. Tags `m0`, `m1`.
- Branch off: `feat/store-core` → PR → `main` → tag `m2`.
- Roadmap put M2 here for a reason: it's the first milestone that **owns its own risk test** (100×1000 INCR under `-race`). Catching mutex misuse at the layer that introduces it instead of punting to M8 integration.

## Scope picked

In: `GET / SET[NX|XX] / DEL / EXISTS / INCR / DECR / KEYS / FLUSHDB / DBSIZE`. Single `sync.RWMutex`. Strict `[]byte` values.

Out (deliberately): AOF (M3), TTL (M4), `BGREWRITEAOF` (M5), multi-DB / SCAN / RENAME / auth / TLS (post-v1).

## Decisions I expect to defend / measure

- **One mutex, not sharded.** LLD §3.2 accepts the contention; HLD §7 says revisit if benchmarks justify. We won't shard until `redis-benchmark` in M9 shows the single lock is the bottleneck — premature otherwise.
- **No lock upgrade in M2.** Lazy expiry (the thing that needs upgrade) lands in M4. `Get` stays a clean `RLock → lookup → unlock`. Means M4's `Get` diff is non-trivial; documenting now so future-me isn't surprised.
- **Defensive copy on `Set`, no copy on `Get`.** Symmetric copies would double the per-call allocation budget for no real safety win (race detector catches misuse). `Get`'s docstring tells the caller the slice is store-owned. *Watch for*: a server-side handler that mutates a returned `[]byte` before writing it out — should bite immediately in tests if it happens.
- **`path.Match` for KEYS, not a custom matcher.** Won't add `store/glob.go` until a test demonstrates a Redis-compat gap. Documenting the `[charset]` shortcut in the package comment is the cheapest way to keep this honest.
- **`nowFunc` clock injection on Store from day one.** Costs nothing in M2; M4 sweeper tests need it. Adding it later would churn `Store` construction across the whole codebase.

## The risk-anchor test

100 goroutines × 1000 `INCR k` → exact 100 000, race detector clean.

What it's actually testing: that the read-modify-write inside `Incr` happens under a single write-lock and doesn't accidentally split into `RLock → unlock → Lock` (the classic mutex-misuse bug for "increment a thing"). If the value comes back as anything other than 100 000, we shipped the bug. If `-race` complains, we shipped a different bug. Both are loud and easy to triage.

## Blog-worthy candidates already

- "The milestone that owns its risk test" — the ordering principle from ROADMAP. The journal's worth showing alongside.
- "Why a learning KV ships with a single mutex." Tie to the deliberate v1 acceptance + v2 deferral.
- "`Get` doesn't copy. Here's why, and what the race detector caught."

## Things to capture as I go

- Any benchmark output from `make test -bench=.` (will need to add bench in M9 anyway — keep early numbers).
- The first time the race detector complains, even on a discarded approach. Especially that one.
- The `KEYS *` iteration-order non-determinism trip if any test relies on it.
- The exact error string formatting for `INCR` non-int vs overflow — Redis-compat matters more than it looks.
