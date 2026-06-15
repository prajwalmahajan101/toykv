# 01 — Store shape & implementation notes

**Date:** 2026-06-11

## What landed

`internal/store/` — `Store`, `entry`, `SetMode`/`SetOpts`, `ErrNotInteger`/`ErrOverflow`. Plus tests + the concurrent risk test.

## Things worth quoting later

### 1. `path.Match` is *good enough* — and explicit about it

Wrote the package doc to say so. `path.Match` does not behave exactly like Redis's glob matcher (`[charset]` semantics differ), but every test the M2 suite needed worked under it. A custom matcher would have been a ~60-line file plus tests, deferred until a real test demonstrates a Redis-compat gap.

Pattern I'm trying to lock in across this project: don't write code for hypothetical gaps. Wait for the test that fails.

### 2. The `Keys` early-validation trick

```go
if _, err := path.Match(pattern, ""); err != nil {
    return nil, err
}
```

`path.Match` only surfaces a bad-pattern error when it actually walks the pattern. If the store is empty, `Keys("[bad")` would silently return `[]string{}` with `err == nil` — a footgun for the dispatch layer. Probing once up front against an empty string fixes that without paying the cost on a populated keyspace either (it short-circuits on the literal `[`).

Worth noting in the post: this is the kind of bug you only notice when you write the test first against a *populated* store and then realise the empty-store case lies.

### 3. The defensive-copy asymmetry

`Set` copies the caller's `[]byte`; `Get` does not copy on return. The reasoning is in the package doc. The test that locks this in (`TestSet_DefensiveCopy`) mutates the caller buffer *after* `Set` returns and verifies `Get` is unaffected — that's the actual contract.

If the inverse ever bites (handler mutates a returned slice before writing it on the wire), the race detector should scream about it immediately. So far the M2 server handlers don't mutate — only `resp.Bulk()` wraps the slice and the writer just emits it.

### 4. INCR semantics

- Missing key → 0 (Redis behaviour).
- Existing non-int → `ErrNotInteger` → `-ERR value is not an integer or out of range` on the wire.
- Overflow at MaxInt64 / MinInt64 → `ErrOverflow` → `-ERR increment or decrement would overflow`.

The exact wire-string is what makes us Redis-cli compatible later. Tested both ends: handler maps the sentinel error to the exact byte string Redis uses.

Overflow guard: `(delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta)`. Single branch, easy to read; symmetrical for Incr and Decr through the shared `incrBy(k, ±1)`.

### 5. The risk test passed first time. Both interesting and boring.

100 goroutines × 1000 INCR → exactly 100 000, `-race` clean, repeated 10× via `-count=10` for good measure. The store uses a single `sync.Mutex` (well, `RWMutex` but `Incr` takes the write lock), so the read-modify-write is naturally atomic. No interesting bug to write about — but the *interesting thing to write about* is that it passes precisely because the lock policy is "obvious", and how easy it would be to break by sharding the map "for performance" without measuring. Tie back to the v1 vs v2 trade-off.

### 6. End-to-end smoke without `redis-cli`

`redis-cli` isn't installed on this box, so the manual smoke used raw RESP frames via `nc`:

```
printf '*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$5\r\nhello\r\n...' | nc -q1 127.0.0.1 16390 | od -c
```

Output: `+OK $5\r\nhello\r\n :1 :2 *2 $1 k $3 ctr :2` — every M2 command, byte-correct. The Go integration tests already exercise the full TCP path, but proving it against the *built binary* with hand-written RESP is the kind of detail that makes a blog post feel grounded.

## What I didn't journal

The defensive copy could become a hotspot — a `redis-benchmark` SET-heavy workload is the right way to measure. Defer to M9. Noting here so future-me doesn't forget.
