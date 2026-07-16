# 21 — M13: INFO + SCAN

**Date:** 2026-07-16
**Branch:** `feat/info-scan` → **PR pending**. Six commits, dependency-ordered: store seq-cursor + `Scan` (unit tests) → `SCAN` command → `INFO` command + metadata plumbing → e2e (SCAN enumeration/guarantee + INFO compat) → docs (README + ROADMAP line-186 correction) → ADR-0014 + this entry. ADR and journal ride the branch (same in-branch call as M12).
**Trigger:** The two v1 gaps that actually annoy in real use — no server introspection (`INFO`) and `KEYS *`-only iteration that loads the whole keyspace into one reply (`SCAN`). Both depend on surfaces already shipped: SCAN iterates M11's typed keyspace, INFO rides M10's RESP3-aware writer.

## Decision / surprise

1. **Go maps can't do Redis's cursor, so the cursor became a per-key sequence number.** Redis SCAN is reverse-binary iteration over hash buckets — it survives rehashing and gives the "every key present for the whole scan is returned" guarantee. Go's runtime `map` randomizes iteration and exposes no buckets, so that technique is simply unavailable. The fix: stamp each `entry` with a monotonic `seq` at creation, preserve it across updates, and make the cursor a *seq value*. `Scan` returns live keys with `seq > cursor` sorted by seq, `next` = last examined seq (0 = done). A key alive for the whole scan keeps its seq and is always reached — the guarantee, with an integer cursor, without bucket access. Recorded as **ADR-0014**.

2. **The seq must survive updates but not re-creation — and the code already made that almost free.** The entry is a value replaced wholesale by `Set`/`Incr`, but read-modify-write paths (`LPUSH`, `HSET` on an existing key, `EXPIRE`, `pop`, `HDEL`) copy the whole `entry` struct back — so `seq` rides along automatically. Only four sites build a *fresh* entry (`Set` new key, `incrBy` new key, `push` new list, `HSet` new hash); those four stamp `nextSeq()`, everything else preserves. Auditing the create sites up front (one grep for `entry{`) meant no path was missed and the "update doesn't restamp" invariant held on the first test run.

3. **The roadmap was wrong about INFO, and following it literally would have shipped a broken command.** ROADMAP line 186 said "RESP3 map when the client is on RESP3." Real Redis returns INFO as a **bulk string** (RESP2) / **verbatim string** (RESP3) — never a map — and `go-redis .Info()` / `redis-cli info` both parse the `# Section\nkey:value` text. A map breaks both. Caught it at design time, surfaced it as a decision rather than silently complying, and corrected the roadmap. `resp.Verbatim("txt", blob)` already downgrades to `$` bulk on RESP2 via the ADR-0011 single-point-downgrade, so one handler serves both protocols.

4. **The owned risk test had to be built to actually exercise the hazard.** A naive stress test (must-see keys, random churn) would pass even on a *broken* positional cursor if the churn happened to miss the shift window. The real hazard is deleting keys with *lower* seq than a still-present key. So the test seeds a body of low-seq "noise" keys first, then the higher-seq must-see set, then deletes the noise range while scanning — the exact index-shift a positional cursor fails and the seq cursor survives. It passes `-race`; a positional-index implementation would fail it.

## Why it mattered

- **Read the constraint before the feature.** "Add SCAN" reads like a command handler; the actual work was a store-model decision forced by Go maps not being Redis hash tables. Mapping the constraint first (the cursor problem) is what turned a handler into ADR-0014 instead of a buggy positional cursor discovered later under `-race`.
- **A spec can be wrong; comply with reality, not the doc.** The "RESP3 map" line would have passed a shallow "did you follow the roadmap" check and failed every real client. Surfacing it as a decision (string vs map) rather than either silently following or silently diverging is the honest move — and it left a corrected roadmap behind.
- **In-memory derived state keeps the format frozen.** `seq` is never persisted; replay re-derives it in record order. No AOF bump, no version-byte plumbing, no crash-test matrix for M13 — the milestone's blast radius stayed at the wire/store layer exactly as scoped.

## Code / measurement

- `Store.Scan(cursor, match, count) (keys, next, err)`: RLock, gather `seq > cursor` live keys, sort by seq, examine ≤COUNT (default 10), `path.Match` filter, `next` = last examined seq or 0.
- `entry` grows one `uint64` (`seq`); `Store.seqCounter` bumped only at absent→present transitions.
- INFO fields: `# Server` (redis_version, tcp_port, uptime_in_seconds/days) · `# Clients` (connected_clients) · `# Persistence` (loading, aof_enabled, appendfsync, aof_current_size, aof_rewrite_in_progress) · `# Stats` (aof_replay_records/bytes) · `# Keyspace` (db0:keys). Plumbing added: `Server.startTime`, stored `replayStats` (was discarded), a `clientCount atomic.Int64` gauge, `aof.Writer.Size()`.
- Tests: store unit (`scan_test.go`, 9 cases) + e2e (`TestScan_*` incl. `TestScan_CursorGuaranteeUnderConcurrentMutation`, `TestInfo_*` on RESP2 + RESP3 + restart-replay). `go test -race ./...` green across all packages incl. chaos; `golangci-lint` 0 issues.
- Live smoke (`toykv-cli`): `SCAN 0 COUNT 5` → cursor "5" + first 5 keys in seq order; `MATCH key:1*` → cursor 0 + matches; bad cursor → `ERR invalid cursor`; `INFO persistence` → `aof_enabled:1 appendfsync:everysec aof_current_size:473 …`.

## Follow-ups

- **README M11 gap.** The command table never listed the M11 list/hash/`TYPE` commands, yet claims "anything outside this table returns `-ERR unknown command`" — now false. Out of M13 scope; worth a small follow-up commit or folding into M16 docs reconciliation.
- **SCAN sort cost.** `Scan` sorts candidates `O(N log N)` per call vs Redis's amortized `O(1)` bucket step. Fine at toy scale (SCAN here bounds *reply* size, not iteration cost); if a large keyspace ever bites, the fix is an auxiliary ordered index, not a cursor redesign (recorded in ADR-0014's alternatives).
- **`aof_last_bgrewrite_status`** was dropped (needs new bookkeeping); only the free `aof_rewrite_in_progress` ships. Revisit if INFO-driven ops tooling wants last-rewrite outcome.

## Blog-worthy?

Yes — *"you can't port Redis SCAN to a Go map, so the cursor becomes a sequence number."* A clean worked example of a data-structure mismatch forcing an architectural decision: Redis's cursor is inseparable from its open-addressed hash table, and the moment your store is a `map[string]entry` you have to invent a different cursor that still honours the same guarantee. The seq-cursor is a tidy answer, and the owned risk test (delete *below* the must-see keys, the exact index-shift hazard) is the kind of adversarial test that only works if you understand *why* the naive design fails. Pairs with a second thread: *"the spec said RESP3 map; the spec was wrong"* — INFO is never a map in Redis, and following the roadmap literally would have shipped a command no real client could parse.
