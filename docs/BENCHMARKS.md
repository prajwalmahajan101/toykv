# toykv — Benchmarks

> Numbers, not promises. Recorded for posterity, not as a target.

## Methodology

Standard run:

```bash
# in one shell
./bin/toykv -addr :6390 -dir ./data -appendfsync always

# in another
redis-benchmark -p 6390 -t set,get -n 100000
```

Recorded fields per run:
- `commit` — short SHA
- `host` — hostname + kernel + CPU + RAM
- `fsync` — `always` | `everysec` | `no`
- `set_p50_ms`, `set_p95_ms`, `set_p99_ms`, `set_rps`
- `get_p50_ms`, `get_p95_ms`, `get_p99_ms`, `get_rps`

`make bench` writes the CSV row to `bench.csv` (post-v1 automation; v1 is hand-recorded).

## Knobs that matter

| Knob | Range | Effect |
|---|---|---|
| `-appendfsync` | `always` / `everysec` / `no` | Single biggest dial. `always` ≤ disk latency; `everysec` ≈ in-memory; `no` ≈ in-memory + kernel risk |
| Disk type | NVMe / SSD / HDD / tmpfs | Dominates `always` |
| `-n` (bench size) | 100k+ | Smaller runs hide tail latency |
| `-c` (parallel clients) | 50 default | Reveals single-mutex contention |

## Recorded runs

### v1.0.0 baseline — TBD

> Run after M8 lands. Record one row per fsync policy.

| Date | Commit | Host | fsync | `SET` p50 | `SET` p95 | `SET` rps | `GET` p50 | `GET` p95 | `GET` rps |
|---|---|---|---|---|---|---|---|---|---|
| _pending_ | _pending_ | _pending_ | `always` | — | — | — | — | — | — |
| _pending_ | _pending_ | _pending_ | `everysec` | — | — | — | — | — | — |
| _pending_ | _pending_ | _pending_ | `no` | — | — | — | — | — | — |

## Reading the numbers

- **`SET` under `always`** is gated by `fsync` round-trip. Compare to your disk's `fdatasync` latency, not Redis.
- **`GET` rps** should saturate the single-mutex `RLock`. If it doesn't, suspect the RESP codec.
- **`SET` rps vs `GET` rps under `everysec`** should be similar — if not, the AOF path has hot allocation.

## Profiling

`make profile` (post-v1):

```bash
./bin/toykv -addr :6390 -dir ./data -appendfsync everysec -cpuprofile cpu.prof
redis-benchmark -p 6390 -t set,get -n 100000
go tool pprof -http=:8080 cpu.prof
```

`-cpuprofile` is the only profiling flag added in v1. Heap/block/mutex profiles via `runtime/pprof` if needed; not exposed as flags by default.

## Anti-targets

- We do **not** chase Redis's numbers. A learning KV in a single mutex won't match a battle-hardened C codebase tuned for years.
- We do **not** publish numbers as marketing. Repo is not "Redis but in Go".
- We do **not** regress-gate CI on bench. Noise > signal at this scale.
