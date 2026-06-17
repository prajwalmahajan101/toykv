# toykv

[![CI](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml/badge.svg)](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml)

> Single-node in-memory KV store in Go. RESP2 wire protocol, AOF persistence, TTL, companion CLI and TUI.

Companion to [toymq](../toymq). Where toymq exercises the **log** pattern (append, replay, durable), toykv exercises the **map** pattern (in-memory, mutable, expirable). Two foundational network-server primitives, one Go module each.

**Status:** M0–M9 shipped. v1.0.0 cut. See [ROADMAP](./docs/ROADMAP.md).

## What it is

- RESP2 wire protocol subset — `redis-cli` works against it.
- AOF persistence with configurable `appendfsync` policy (`always` | `everysec` | `no`), plus `BGREWRITEAOF` compaction.
- TTL with lazy + 1 Hz sweep eviction; expiry round-trips through AOF v2.
- A line-oriented CLI (`toykv-cli`) — one-shot, REPL, and piped modes; modelled on `redis-cli`.
- A Bubble Tea TUI (`toykv-tui`) with two-pane keys/value layout, 2 Hz refresh, and every PRD §5.5 keybinding.

## What it isn't

- A Redis replacement.
- Production-ready (no auth, no TLS).
- Multi-node, replicated, or clustered.

> ⚠ **Security limits.** v1 has **no auth, no TLS, no IP allowlist**. Bind to `127.0.0.1` and do not expose to networks you don't fully trust. The full threat model lives in [SECURITY.md](./docs/SECURITY.md); [PRD §4 Non-goals](./docs/PRD.md#4-non-goals-v1) explains what's deliberately out of scope.

## Install

Three options.

**1. Pre-built binary (recommended).** Grab the archive for your OS/arch from the [latest release](https://github.com/prajwalmahajan101/toykv/releases/latest):

```sh
VERSION=v1.0.0
OS=$(uname -s | tr '[:upper:]' '[:lower:]')      # darwin | linux
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -L "https://github.com/prajwalmahajan101/toykv/releases/download/${VERSION}/toykv_${VERSION#v}_${OS}_${ARCH}.tar.gz" \
  | tar -xz -C /usr/local/bin
toykv --help
```

Each archive contains all three binaries: `toykv`, `toykv-cli`, `toykv-tui`. Verify with the published `SHA256SUMS` file.

**2. From source.** Requires Go 1.25+ (1.26 recommended).

```sh
git clone https://github.com/prajwalmahajan101/toykv && cd toykv
make build    # → bin/toykv, bin/toykv-cli, bin/toykv-tui
```

**3. `go install` (server only).**

```sh
go install github.com/prajwalmahajan101/toykv/cmd/toykv@latest
```

## Quickstart

```sh
./bin/toykv -addr :6390 -dir ./data
# in another terminal:
./bin/toykv-cli -addr :6390 SET hello world
./bin/toykv-cli -addr :6390 GET hello

# or with redis-cli, byte-for-byte compatible on the supported subset:
redis-cli -p 6390 PING
```

The TUI:

```sh
./bin/toykv-tui -addr :6390 -refresh 2s
```

Keybindings (PRD §5.5): `j/k` navigate · `/` filter · `n` SET new · `e` edit value · `d` DEL · `t` EXPIRE · `i` INCR · `D` DECR · `F` FLUSHDB · `r` refresh · `:` raw RESP prompt · `q` quit.

![toykv-tui demo](./assets/tui.gif)

If your fresh clone doesn't carry the GIF yet, here's the static rendering:

```
┌── toykv-tui ──────────────────────────────────────────────────────────────┐
│ keys (3)                          │ value                                  │
│ ───────────────                   │ ─────────────                          │
│ > counter      str   28s    2B    │ key:     counter                       │
│   hello        str   ---     5B   │ type:    string                        │
│   note         str  118s   13B    │ ttl:     28s                           │
│                                   │ size:    2 B                           │
│                                   │                                        │
│                                   │ "42"                                   │
│ /filter:                          │                                        │
├───────────────────────────────────────────────────────────────────────────┤
│  :6390   DBSIZE=3   fsync=always   last=INCR counter → 43   refresh=2s    │
└───────────────────────────────────────────────────────────────────────────┘
```

## Supported commands (PRD §5.1)

| Command | Arity | Reply | Notes |
|---|---|---|---|
| `PING [msg]` | 0–1 | `+PONG` / `$bulk` | Echoes msg if given |
| `ECHO msg` | 1 | `$bulk` | |
| `GET key` | 1 | `$bulk` / `$-1` | Nil bulk if absent or expired |
| `SET key value [EX s] [PX ms] [NX\|XX]` | 2–6 | `+OK` / `$-1` | Nil under `NX`/`XX` rejection |
| `DEL key [key …]` | ≥1 | `:N` | Number of keys removed |
| `EXISTS key [key …]` | ≥1 | `:N` | Counts duplicates |
| `EXPIRE key seconds` | 2 | `:0` / `:1` | 0 if key missing |
| `PEXPIRE key ms` | 2 | `:0` / `:1` | |
| `PEXPIREAT key ms-epoch` | 2 | `:0` / `:1` | |
| `TTL key` | 1 | `:N` | -2 missing, -1 no expiry, ≥0 seconds left |
| `PTTL key` | 1 | `:N` | Same sentinels in ms |
| `PERSIST key` | 1 | `:0` / `:1` | Removes TTL |
| `INCR key` | 1 | `:N` | `-ERR` on non-integer / overflow |
| `DECR key` | 1 | `:N` | Same |
| `KEYS pattern` | 1 | `*N` | stdlib `filepath.Match` glob |
| `FLUSHDB` | 0 | `+OK` | |
| `DBSIZE` | 0 | `:N` | |
| `BGREWRITEAOF` | 0 | `+OK` | Async; see [ADR-0005](./docs/adr/0005-bgrewriteaof-dual-write-and-tmp-cleanup.md) |

Anything outside this table returns `-ERR unknown command`. Everything in this table is exercised by [`test/e2e/protocol_*_test.go`](./test/e2e/) on every CI run.

## Durability + fsync tradeoff

`-appendfsync` is the single biggest knob. `always` (default) gives the strong guarantee — zero acknowledged-write loss across SIGKILL + restart, proven by [`internal/server/aof_crash_test.go`](./internal/server/aof_crash_test.go) and the M9 chaos soak in [`test/chaos/`](./test/chaos/). `everysec` trades up to one second of recent writes for throughput; `no` leaves durability to the OS.

Baseline numbers (NVMe + warm cache, 13th-gen i7-1355U; `valkey-benchmark -n 100000`):

| fsync | `SET` p50 | `SET` p95 | `SET` rps | `GET` p50 | `GET` p95 | `GET` rps |
|---|---|---|---|---|---|---|
| `always`   | 0.295 ms | 0.591 ms |  80 515 | 0.359 ms | 0.631 ms |  67 843 |
| `everysec` | 0.479 ms | 0.703 ms |  55 340 | 0.471 ms | 0.687 ms |  56 148 |
| `no`       | 0.487 ms | 0.647 ms |  53 908 | 0.319 ms | 0.599 ms |  72 780 |

On this hardware `always` came out fastest — `fdatasync` on a warm NVMe is cheaper than the per-request RESP+mutex overhead. On slower disks the conventional `no > everysec > always` ordering holds. Full methodology + how to reproduce: [`docs/BENCHMARKS.md`](./docs/BENCHMARKS.md). Why the format and policy look the way they do: [ADR-0003](./docs/adr/0003-aof-format-and-fsync-policy.md). Compaction safety story: [ADR-0005](./docs/adr/0005-bgrewriteaof-dual-write-and-tmp-cleanup.md).

## `toykv-cli` modes

```sh
# one-shot — single command, prints reply, exits.
toykv-cli -addr :6390 SET k v

# REPL — interactive prompt; `exit` or Ctrl-D quits.
toykv-cli -addr :6390

# piped — one command per line from stdin.
printf 'SET k v\nGET k\nDEL k\n' | toykv-cli -addr :6390

# raw — print bulk replies as raw bytes (script-friendly, no quoting).
toykv-cli -addr :6390 -raw GET k
```

Exit codes:

| Code | Meaning |
|---|---|
| `0` | Command succeeded (even if reply was nil) |
| `1` | Server returned `-ERR ...` |
| `2` | Connection / parse / flag failure |

Pretty-printer follows `redis-cli` conventions: `+OK`, `(nil)`, `(integer) 42`, `"value"`. Tradeoffs in [ADR-0008](./docs/adr/0008-cli-stdlib-and-mode-detection.md).

## Layout

```
cmd/
  toykv/        # server binary
  toykv-cli/    # RESP2 CLI (one-shot / REPL / piped)
  toykv-tui/    # Bubble Tea TUI
internal/
  resp/         # RESP2 reader/writer
  store/        # in-memory KV, TTL, sweeper
  aof/          # AOF v2 writer/replayer/rewriter
  server/       # TCP accept loop + command dispatch
  client/       # shared RESP2 client (CLI + TUI consume it)
  cmdparse/     # shell-like tokeniser (CLI + TUI raw prompt)
  respfmt/      # RESP-value renderer (CLI pretty/raw + TUI value pane)
  tui/          # Bubble Tea Model/Update/View
test/
  e2e/          # subprocess harness + go-redis/redis-cli/teatest protocol-compat (M8)
  chaos/        # soak harness: SIGKILL/SIGSTOP/BGREWRITEAOF under mixed workload (M9)
docs/
  PRD.md HLD.md LLD.md ROADMAP.md TESTING.md BENCHMARKS.md
  adr/          # architectural decision records
  journal/      # per-PR milestone journal
assets/         # TUI gif/png (regenerated from assets/tui.tape via vhs)
```

## Testing

Four layers, each runnable independently:

```sh
go test ./...                  # unit + in-process integration + crash injection
go test ./test/e2e/...         # subprocess harness: shipped binaries + go-redis + redis-cli
go test ./cmd/toykv-tui/...    # teatest-driven TUI smoke against an in-process server
make chaos-smoke               # 30s soak: SIGKILL/SIGSTOP/BGREWRITEAOF + workload
```

`make chaos` runs the 5-minute full soak with more workers; `make chaos-smoke` is the CI gate. The full crash matrix — what each layer proves and where the test lives — is in [`docs/TESTING.md`](./docs/TESTING.md#crash-matrix--who-proves-what).

The e2e suite builds `cmd/toykv` and `cmd/toykv-cli` from source on each `go test` invocation and drives them as real subprocesses — the same code path `make build` ships. The `redis-cli` byte-compat sweep skips automatically when `redis-cli` isn't on `PATH`; CI installs `redis-tools` on Linux runners to exercise it.

## Documentation

- [PRD](./docs/PRD.md) — product requirements
- [HLD](./docs/HLD.md), [LLD](./docs/LLD.md) — architecture
- [ROADMAP](./docs/ROADMAP.md) — milestone plan with risk ownership
- [TESTING](./docs/TESTING.md) — test layers, coverage policy, crash matrix
- [BENCHMARKS](./docs/BENCHMARKS.md) — methodology + recorded runs
- [RELEASE_PLAN](./docs/RELEASE_PLAN.md), [SECURITY](./docs/SECURITY.md), [CONTRIBUTING](./docs/CONTRIBUTING.md)
- [ADRs](./docs/adr/) — decision records, including the dep-policy relaxation in [ADR-0009](./docs/adr/0009-tui-bubble-tea-and-injectable-doer.md) and the v1 release-artefact policy in [ADR-0010](./docs/adr/0010-release-artefacts-and-distribution.md)

## Dependencies

Server and CLI binaries: stdlib only. TUI binary adds `github.com/charmbracelet/{bubbletea,lipgloss,bubbles}` and their transitive surface — see [ADR-0009](./docs/adr/0009-tui-bubble-tea-and-injectable-doer.md) for the rationale. Test-only deps (not in any shipped binary): `github.com/redis/go-redis/v9` and `github.com/charmbracelet/x/exp/teatest`, pulled in for M8 protocol-compat tests.

## License

MIT. See [`LICENSE`](./LICENSE).
