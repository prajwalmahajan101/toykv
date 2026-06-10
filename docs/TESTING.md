# toykv — Testing Strategy

> Five layers, each catching a different class of bug. Integration > unit when in doubt.

## Layer 0 — `go vet` / `golangci-lint`

Run on every push via CI. Failing lint blocks merge. Configured strict enough to catch:
- Shadowed errors.
- Mismatched printf verbs.
- Mutex copies.
- Channel deadlock patterns staticcheck can spot.

## Layer 1 — Unit tests

Per-package, table-driven. Target the leaf packages:

| Package | What's covered |
|---|---|
| `internal/resp` | Frame parse/serialise round-trip for every kind; nil-bulk & nil-array; oversized bulk → `ErrTooLarge`; CRLF-less garbage → `ErrProtocol` |
| `internal/store` | `SET`/`GET`/`DEL` semantics; `NX`/`XX` modes; `INCR` overflow; `TTL` boundary values (-2, -1, 0, positive); glob matcher edge cases |
| `internal/store/ttl` | Expired keys returned as miss; sweeper evicts under no traffic; deterministic with injected clock |
| `internal/aof/format` | Header read/write; version mismatch rejection; record framing |
| `internal/client` | Dial, `Do`, error mapping, conn close on EOF |
| `internal/cli` | Pretty/raw formatting per RESP kind; argv tokeniser edge cases; exit-status mapping; OneShot/Piped/REPL with fake `Doer` |

**Rule:** no mocks for things you own. If `store_test.go` would mock `aof.Writer`, the test belongs in `internal/server/`, not `internal/store/`.

Run: `go test -race -count=1 ./internal/...`

## Layer 2 — Integration tests (in-process)

Spin a real `*Server` in the test process, bind to `127.0.0.1:0` (random port), drive with `go-redis/v9`. Cover:

- Every command in PRD §5.1 with positive and negative cases.
- TTL expiry round-trip (set with `EX 1`, sleep 1.1s, expect miss).
- `KEYS pattern` matches against a known seeded set.
- Concurrent writers: 100 goroutines × 1000 `INCR k` → expect final value 100_000.
- `BGREWRITEAOF` while writers are active → no record loss; AOF size shrinks.

Location: `internal/server/server_integration_test.go`. Tagged `//go:build integration`.

Run: `go test -tags integration -race ./internal/server/...`

## Layer 3 — End-to-end (subprocess)

Build the binary, exec it, talk over TCP. Three harnesses:

1. `go-redis/v9` — primary; runs always.
2. `toykv-cli` — built from this repo; runs always. Exercises one-shot, REPL (scripted via expect-style pty), and piped-stdin modes against every PRD §5.1 command.
3. `redis-cli` — runs if found on `$PATH`; skipped otherwise (CI install step).

Lives in `test/e2e/`. Verifies the **shipped binaries** match PRD §5.1, including flag parsing.

Run: `go test ./test/e2e/...`

## Layer 4 — Crash-injection

The highest-blast-radius requirement is "zero acknowledged-write loss under `appendfsync=always`". This layer proves it.

Pattern (`test/e2e/crash_test.go`):

```
1. Start server in subprocess.
2. Client SET 1000 keys; each ack confirmed.
3. Client issues SET k v && SYNCWAIT (acked) repeatedly while …
4. Test process sends SIGKILL to server at a random t.
5. Restart server (same -dir).
6. After replay, verify every acked SET is present.
```

The `SYNCWAIT` step is "perform a synchronous round-trip with an ack-blocking command" — under `FsyncAlways`, the ack itself proves fsync returned.

## Layer 5 — TUI smoke tests

Bubble Tea's `teatest` snapshots. Pin the UI for a handful of scripted interactions:

- Boot against a seeded server → assert key list renders.
- Press `/` → filter input appears.
- Press `e enter "new value" enter` → store reflects the change.
- Press `F y` → store is empty.

Lives in `internal/tui/*_test.go`. Run as part of normal `go test ./...`.

## Layer 6 — Benchmarks

Not pass/fail, but tracked.

- `make bench` runs `redis-benchmark -p 6390 -t set,get -n 100000`.
- Results recorded in [`BENCHMARKS.md`](./BENCHMARKS.md) per commit-of-interest.
- A regression beyond ~20% triggers investigation; not a merge block.

## CI matrix

| Job | OS | Go | Tags | Always-on |
|---|---|---|---|---|
| `lint` | ubuntu-latest | 1.26 | — | yes |
| `unit` | ubuntu-latest, macos-latest | 1.26 | — | yes |
| `integration` | ubuntu-latest | 1.26 | `integration` | yes |
| `e2e` | ubuntu-latest | 1.26 | — | yes |
| `bench` | self-hosted runner | 1.26 | — | nightly + on release tag |

## Coverage policy

Track `go test -coverprofile`, **report**, don't gate. Coverage targets:
- `internal/resp` ≥ 90% (small, mechanical).
- `internal/store` ≥ 85%.
- `internal/aof` ≥ 80%.
- `internal/client` ≥ 85%.
- `internal/cli` ≥ 80%.
- `internal/server` ≥ 70%.
- `internal/tui` ≥ 60%.

No hard floor on global coverage — gaming it is worse than missing it.

## What we explicitly DO NOT test

- Network failures simulated via `iptables`/`tc` — interesting but out of v1 scope.
- Disk-full conditions — should exit non-zero; documented, not exhaustively fuzzed.
- Real Redis compatibility byte-for-byte beyond the documented subset.
