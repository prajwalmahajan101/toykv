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

### v1.0.0 baseline

Recorded on the M9 release-prep branch, before tagging. Tool: `valkey-benchmark` (drop-in for `redis-benchmark`) from `valkey/valkey:8-alpine`, run via `docker run --rm --network host` against `bin/toykv` on the loopback. One process each; `-n 100000`, default 50 parallel clients, `--csv` for per-percentile latency. Latencies in ms.

**Host:** Linux 7.0.9-arch2-1 x86_64, 13th Gen Intel Core i7-1355U, 16 GiB RAM, NVMe SSD (ext4, default mount opts), kernel page cache warm.

| Date | Commit | fsync | `SET` p50 | `SET` p95 | `SET` p99 | `SET` rps | `GET` p50 | `GET` p95 | `GET` p99 | `GET` rps |
|---|---|---|---|---|---|---|---|---|---|---|
| 2026-06-17 | `824ca78` | `always`   | 0.295 | 0.591 | 0.863 |  80 515 | 0.359 | 0.631 | 0.831 |  67 843 |
| 2026-06-17 | `824ca78` | `everysec` | 0.479 | 0.703 | 0.903 |  55 340 | 0.471 | 0.687 | 0.847 |  56 148 |
| 2026-06-17 | `824ca78` | `no`       | 0.487 | 0.647 | 0.807 |  53 908 | 0.319 | 0.599 | 0.775 |  72 780 |

> **Reading these:** `always` came out fastest on this NVMe + warm-cache box, which inverts the usual ordering. Two reasons. (1) `fdatasync` on a desktop NVMe with the kernel write cache primed is well under 100 µs — cheaper than the per-request RESP+mutex overhead that dominates `everysec`/`no`. (2) Each policy runs a fresh AOF in a fresh tmpdir, so there's no pre-existing fragmentation. On a spinning disk, a saturated NVMe, or a cold box, the ordering will flip to the conventional `no > everysec > always`. The numbers are recorded — not a target.

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
