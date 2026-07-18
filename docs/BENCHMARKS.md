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

### v2.0.0 typed workloads

Re-run for the v2.0.0 release with the **typed** default (`set,get,lpush,rpush,hset`) — strings, lists, and hashes, not just strings. Same methodology and tooling as the v1.0.0 baseline: `valkey-benchmark` from `valkey/valkey:8-alpine` via `docker run --rm --network host`, `-n 100000`, default 50 clients, `--csv`. **Bind is `127.0.0.1:6390`** (loopback) — v2's protected mode refuses the unspecified `:6390` bind without auth/TLS, which is the M15 feature working as designed. Latencies in ms.

**Host:** Linux 7.0.10-arch1-1 x86_64, 13th Gen Intel Core i7-1355U, 16 GiB RAM, NVMe SSD (same box as the v1.0.0 baseline). Binary: `feat/release-v2` with the M16 telemetry disabled (default). Recorded 2026-07-18.

| fsync | command | rps | p50 | p95 | p99 |
|---|---|---|---|---|---|
| `always` | SET | 38 715 | 0.615 | 1.439 | 2.183 |
| `always` | GET | 57 803 | 0.407 | 0.879 | 1.295 |
| `always` | LPUSH | 48 239 | 0.535 | 1.031 | 1.687 |
| `always` | RPUSH | 43 860 | 0.567 | 1.039 | 1.391 |
| `always` | HSET | 43 917 | 0.567 | 1.143 | 1.607 |
| `everysec` | SET | 42 974 | 0.575 | 1.191 | 1.767 |
| `everysec` | GET | 60 241 | 0.391 | 0.815 | 1.271 |
| `everysec` | LPUSH | 35 236 | 0.663 | 1.535 | 2.239 |
| `everysec` | RPUSH | 48 733 | 0.519 | 0.967 | 1.463 |
| `everysec` | HSET | 55 249 | 0.447 | 0.847 | 1.343 |
| `no` | SET | 51 099 | 0.495 | 0.911 | 1.375 |
| `no` | GET | 60 976 | 0.399 | 0.751 | 1.111 |
| `no` | LPUSH | 49 628 | 0.519 | 0.911 | 1.327 |
| `no` | RPUSH | 51 626 | 0.495 | 0.871 | 1.239 |
| `no` | HSET | 49 044 | 0.527 | 0.903 | 1.327 |

> **Reading these:** absolute numbers run lower and noisier than the v1.0.0 baseline — this is a longer 5-command sustained run on a fanless mobile i7 that thermally throttles, not a same-conditions regression (single-command bursts on a cold box hit the v1 range). List/hash mutations land in the same band as `SET`, as expected: all mutating commands share the store-mutex + AOF-append path. The point of the typed run is coverage of the v2 value types, not a headline number.

#### Observability no-regression (OTel-off parity)

The M16 telemetry is off by default; the release gate requires that to be a true no-op vs the pre-M16 binary. A back-to-back A/B (pre-M16 `a3d62e1` vs `feat/release-v2`, `everysec`, interleaved) initially showed the disabled path **~18–21% slower** — attribute/option construction allocates even against no-op providers (~29 allocs/op). Fixed by memoizing per-command instrument attributes (see [ADR-0017](./adr/0017-opentelemetry-signal-model-and-otlp-export.md) M17 amendment), **without** adding an `if enabled` guard: disabled path dropped to **14 allocs/op (1757 ns/op)**, SET returned to parity, and pipelined GET to within ~7% (residual = the two irreducible no-op `Tracer.Start` context allocations). Guarded by `TestObserveCommand_Disabled_AllocBudget`.

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
