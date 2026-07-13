# 18 — M11: value types (lists + hashes, AOF v3)

**Date:** 2026-07-13
**Branch:** `feat/types` — 10 commits: `b82c000` (deque), `ed9ed7a` (tagged-union store), `290a4bc` (AOF v3 + header upgrade), `146c006` (list commands), `1ab5cc2` (hash commands + TYPE), `7609106` (typed rewrite snapshots), `c638b92` (typed crash injection), `11a9473` (go-redis e2e), `ead3823` (LLD/CHANGELOG), plus this docs commit (ADR-0012 + journal + roadmap flip). PR pending.
**Trigger:** The highest-blast-radius milestone of the v2 arc (see entry 16): store model goes typed, AOF takes its second format bump, and the milestone owns its crash test. ADR-0012 written with the code, per discipline.

## Decision / surprise

1. **The design session found a scope hole in the roadmap: no `HGETALL`.** The whole M10-before-M11 ordering was justified by "types exercise RESP3's map replies (`HGETALL` → map)" — and then the roadmap's hash list omitted `HGETALL`. Without it, nothing in M11 emits a map frame. Added it during brainstorming. Lesson: the *rationale* for a milestone ordering is itself a requirement; check the deliverables list against it.

2. **The header-upgrade question was the sleeper decision.** Everyone plans "bump the version byte for new files"; the real question is the *existing* v2 file the new binary appends to. First `LPUSH` record in and the header lies — an old binary would pass the gate and die mid-replay on an unknown command, exactly what the gate exists to prevent. Resolution: `Open()` pwrites the single version byte at offset 7 + fsync *before* accepting appends. Invariant: header version ≥ newest record format in the file, at every instant. One byte, no torn-write window, and old binaries fail fast with `ErrBadVersion`. (Gotcha en route: the append fd is `O_APPEND` and Go rejects `WriteAt` on those — the upgrade needs its own short-lived handle.)

3. **The deque earns its ~100 lines via replay, not live traffic.** A slice-backed list with O(n) LPUSH looks fine interactively — but AOF replay executes every historical LPUSH back-to-back, so a left-heavy history goes quadratic at exactly the moment (startup) you can least afford it. Growable ring buffer: O(1) both ends, O(1) LINDEX, and `rng(0,-1)` gives LRANGE its Redis negative-index semantics in one place.

4. **Tagged union as tag + three concrete fields, not `any`, not a sealed interface.** Two "wasted" pointers per entry buys compile-checked access — no type assertion can panic. `WRONGTYPE` enforcement lives at the store boundary as one sentinel (`ErrWrongType`), mapped once in the server to the byte-exact Redis error. The cost that mattered: `Get` grew a third return (`v, ok, err`), churning every call site — paid once instead of maintaining a parallel `GetChecked`.

5. **M10's single-downgrade-point bet paid off exactly as advertised.** `cmdHGetAll` returns `resp.Map(...)` and is done: `%N` to RESP3 clients, flat `*2N` to RESP2, zero writer changes. The compat sweep grew typed steps and `HGETALL` is its one new RESP2↔RESP3 divergence — pinned byte-for-byte with a single-field hash (map iteration order is unspecified, so multi-field hashes can't be byte-asserted).

6. **Verbatim live records, canonical snapshot records.** LPUSH/HSET/HDEL carry no relative time, so unlike `SET EX` they replay deterministically as-is — ADR-0003's "on-disk = wire" holds with zero canonicalization on the live path. The rewriter emits one record per key (`RPUSH k e1…eN` / `HSET k f1 v1…`), with TTL as a follow-up `PEXPIREAT` (shared `renderPExpireAt` with the live EXPIRE path, so both emit byte-identical records).

## Why it mattered

- **The owned risk test is a model-based crash test, not a spot check.** The parent process maintains its own model of acked mutations (applying each op only when its ack arrives), SIGKILLs the child mid-stream of mixed LPUSH/RPUSH/LPOP/HSET/HDEL under `FsyncAlways`, restarts, and asserts the replayed store equals the model *exactly* — list order included. This is the M3 durability discipline extended to the surface that could newly corrupt it.
- **Empty-collection deletion is load-bearing, not cosmetic.** `TYPE` returns `none` and `EXISTS` returns 0 the instant the last element pops — and a subsequent `SET NX` can claim the key. Tested at the store layer, the wire layer, and through go-redis.

## Code / measurement

- Full suite: `make lint` → 0 issues; `go test ./... -race` → 12/12 packages green; crash test `-count=2 -race` stable.
- Live wire probe (nc, one connection): `LPUSH k b a` → `:2`; `LRANGE k 0 -1` → `*2 a b`; `TYPE k` → `+list`; `GET k` → `-WRONGTYPE Operation against a key holding the wrong kind of value`; `HGETALL h` → flat array on RESP2.
- Restart probe: header bytes `54 4f 59 4b 56 00 00 03` (`TOYKV\0\0` + v3) after opening a fresh dir; kill → restart → `LRANGE`/`HGET` reproduce state from replay alone.

## Follow-ups

- **Roadmap M11 row flipped in this branch** (rides the PR, honoring no-direct-commits-to-`main`); PR number to be back-filled in the status table and ADR-0012 header on merge.
- **Snapshot chunking deferred.** One record per list/hash is bounded by the 64 MiB frame cap; Redis-style 128-element chunking waits for a real workload that needs it (noted in ADR-0012).
- **`HKEYS`/`HVALS`/`HGETALL` order is unspecified** (Go map iteration) — same contract as Redis, but worth remembering when writing future byte-level tests: single-field hashes only.
- **M12 (AUTH+TLS) is next** and self-contained; the `connState` seam from M10 is already in place.

## Blog-worthy?

The header-upgrade beat is the one: *"the version byte's hardest case isn't new files — it's the old file you're about to append to."* Pairs with the `O_APPEND`/`WriteAt` gotcha for a concrete, honest systems anecdote. Second beat: the deque justified by replay (amortized analysis meeting crash recovery), which reframes a data-structure choice as a durability choice.
