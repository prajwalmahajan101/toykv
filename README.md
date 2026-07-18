# toykv

[![CI](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml/badge.svg)](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml)

> Single-node in-memory KV store in Go — **deployable, safe-by-default, observable**. RESP2 wire protocol (RESP3 opt-in via `HELLO 3`), string/list/hash value types, AOF persistence, TTL, `AUTH`+TLS with a protected-mode default, `INFO`/`SCAN` introspection, OpenTelemetry (logs/metrics/traces → LGTM), companion CLI and TUI.

Companion to [toymq](../toymq). Where toymq exercises the **log** pattern (append, replay, durable), toykv exercises the **map** pattern (in-memory, mutable, expirable). Two foundational network-server primitives, one Go module each.

**Status:** M0–M17 shipped — v2.0.0 (deployable, safe-by-default, observable single-node KV). See [ROADMAP](./docs/ROADMAP.md) and [CHANGELOG](./CHANGELOG.md).

## What it is

- RESP2 wire protocol subset — `redis-cli` works against it. RESP3 is opt-in via `HELLO 3` (`redis-cli -3`); RESP2 replies are unchanged.
- AOF persistence with configurable `appendfsync` policy (`always` | `everysec` | `no`), plus `BGREWRITEAOF` compaction.
- TTL with lazy + 1 Hz sweep eviction; expiry round-trips through AOF v2.
- A line-oriented CLI (`toykv-cli`) — one-shot, REPL, and piped modes; modelled on `redis-cli`.
- A Bubble Tea TUI (`toykv-tui`) with two-pane keys/value layout, 2 Hz refresh, and every PRD §5.5 keybinding.

## What it isn't

- A Redis replacement.
- Production-ready (no auth, no TLS).
- Multi-node, replicated, or clustered.

> 🔒 **Security posture (v2).** `AUTH` (`-requirepass`, constant-time compare) and TLS (`-tls-cert`/`-tls-key`, min 1.2) lift v1's localhost-only ceiling, and **protected mode** refuses an unauthenticated non-loopback bind by default — so an accidental `0.0.0.0` exposure fails closed rather than serving the world. Still single-node with no IP allowlist and no RBAC. The full threat model lives in [SECURITY.md](./docs/SECURITY.md); the v2.0.0 release-gate audit (incl. two pre-auth codec DoS bounds it added) is in [SECURITY-REVIEW-v2.md](./docs/SECURITY-REVIEW-v2.md).

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

Authenticated and/or TLS-terminated (M12):

```sh
./bin/toykv -addr :6390 -dir ./data -requirepass s3cret
redis-cli -p 6390 -a s3cret PING          # AUTH; unauthenticated PING still works,
                                          # everything else returns -NOAUTH

./bin/toykv -addr :6390 -dir ./data -tls-cert cert.pem -tls-key key.pem
redis-cli -p 6390 --tls --cacert cert.pem PING
```

`-tls-cert`/`-tls-key` must be given as a pair (min TLS 1.2). Both flags compose with `-requirepass` — see [SECURITY](./docs/SECURITY.md) for the deployment posture and [ADR-0013](./docs/adr/0013-auth-model-and-tls-termination.md) for the auth model.

Since M15, **protected mode** is on by default: the server refuses to *start* on a non-loopback bind (e.g. `-addr 0.0.0.0:6390`) unless `-requirepass` or TLS is set, exiting non-zero with a message naming the fix. Bind a loopback address, add auth/TLS, or pass `-protected-mode no` to override. This is the deliberate breaking change that earns `2.0.0` — see [ADR-0016](./docs/adr/0016-protected-mode-and-atomic-keyspace-ops.md).

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
| `HELLO [proto [AUTH user pass]]` | 0–4 | `%map` / `*array` | RESP3 handshake; `HELLO 3` opts in. RESP2 clients get the map as a flat array. See [ADR-0011](./docs/adr/0011-resp3-negotiation-and-protocol-state.md) |
| `AUTH [user] password` | 1–2 | `+OK` / `-WRONGPASS` | Single-user model; only `default` is valid. Constant-time compare. See [ADR-0013](./docs/adr/0013-auth-model-and-tls-termination.md) |
| `PING [msg]` | 0–1 | `+PONG` / `$bulk` | Echoes msg if given |
| `ECHO msg` | 1 | `$bulk` | |
| `GET key` | 1 | `$bulk` / `$-1` / `_` | Nil if absent or expired (`_` on RESP3) |
| `SET key value [EX s] [PX ms] [NX\|XX]` | 2–6 | `+OK` / `$-1` / `_` | Nil under `NX`/`XX` rejection (`_` on RESP3) |
| `DEL key [key …]` | ≥1 | `:N` | Number of keys removed |
| `EXISTS key [key …]` | ≥1 | `:N` | Counts duplicates |
| `RENAME key newkey` | 2 | `+OK` / `-ERR` | Atomic move; overwrites dest; TTL + type travel. `-ERR no such key` if absent. See [ADR-0016](./docs/adr/0016-protected-mode-and-atomic-keyspace-ops.md) |
| `RENAMENX key newkey` | 2 | `:0` / `:1` | Like `RENAME` but `:0` (no move) if dest exists |
| `COPY src dst [DB 0] [REPLACE]` | 2–5 | `:0` / `:1` | Deep copy; TTL + type travel. `:0` if dest exists without `REPLACE`. Single-DB: only `DB 0` |
| `EXPIRE key seconds` | 2 | `:0` / `:1` | 0 if key missing |
| `PEXPIRE key ms` | 2 | `:0` / `:1` | |
| `PEXPIREAT key ms-epoch` | 2 | `:0` / `:1` | |
| `TTL key` | 1 | `:N` | -2 missing, -1 no expiry, ≥0 seconds left |
| `PTTL key` | 1 | `:N` | Same sentinels in ms |
| `PERSIST key` | 1 | `:0` / `:1` | Removes TTL |
| `INCR key` | 1 | `:N` | `-ERR` on non-integer / overflow |
| `DECR key` | 1 | `:N` | Same |
| `KEYS pattern` | 1 | `*N` | stdlib `filepath.Match` glob |
| `SCAN cursor [MATCH pattern] [COUNT n]` | 1–5 | `*2` (cursor + `*N`) | Cursor-based keyspace iteration; `0` cursor starts, `0` reply ends. Large-keyspace alternative to `KEYS` |
| `FLUSHDB` | 0 | `+OK` | |
| `DBSIZE` | 0 | `:N` | |
| `TYPE key` | 1 | `+status` | `string` / `list` / `hash` / `none`. Underpins the TUI + `SCAN` rendering. See [ADR-0012](./docs/adr/0012-tagged-union-store-and-aof-v3.md) |
| `LPUSH key val [val …]` | ≥2 | `:N` | Prepend; new list length. `-WRONGTYPE` on a non-list key |
| `RPUSH key val [val …]` | ≥2 | `:N` | Append; new list length |
| `LPOP key` / `RPOP key` | 1 | `$bulk` / `$-1` | Pop head / tail; nil on empty or absent |
| `LLEN key` | 1 | `:N` | List length (0 if absent) |
| `LRANGE key start stop` | 3 | `*N` | Inclusive range; negative indices from the tail |
| `LINDEX key index` | 2 | `$bulk` / `$-1` | Element at index; negative from the tail |
| `HSET key field val [field val …]` | ≥3 | `:N` | Fields newly added |
| `HGET key field` | 2 | `$bulk` / `$-1` | Nil if field/key absent |
| `HDEL key field [field …]` | ≥2 | `:N` | Fields removed |
| `HEXISTS key field` | 2 | `:0` / `:1` | |
| `HKEYS key` / `HVALS key` | 1 | `*N` | Field names / values |
| `HLEN key` | 1 | `:N` | Field count |
| `HGETALL key` | 1 | `%map` / `*N` | Native RESP3 map; flat array on RESP2 |
| `INFO [section]` | 0–1 | `$bulk` / `=verbatim` | `# Section\nkey:value` server introspection (verbatim on RESP3); optional section filter |
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

## Observability (OpenTelemetry → LGTM)

toykv emits the three OpenTelemetry signals — **metrics**, **traces**, **logs** —
over **OTLP**, viewable in a Grafana **LGTM** stack (Loki / Tempo / Mimir /
Grafana). It is **off unless `-otel-endpoint` is set**: with it unset the
providers are SDK no-ops and behaviour/benchmarks match the pre-M16 binary
(there is no hot-path cost and no semver impact — M15's protected mode is what
earns `2.0.0`).

```sh
# start a local all-in-one LGTM stack (single grafana/otel-lgtm image)
docker compose -f deploy/otel-lgtm/compose.yaml up -d
# run toykv pointed at it (grpc:4317 by default)
toykv -otel-endpoint localhost:4317 -otel-sampling 1.0
# drive traffic, then open Grafana at http://localhost:3000
```

- **Metrics → Mimir** — RED per command (`toykv_command_duration_seconds`,
  `toykv_commands_total`, error counter by kind), plus `toykv_keys`,
  `toykv_aof_size_bytes`, `toykv_connections_active`, and the
  `toykv_aof_fsync_duration_seconds` durability signal.
- **Traces → Tempo** — a `connection` span per client with `command` children,
  each with `store.<op>` and (for writes) `aof.append` siblings; a slow fsync
  shows as span latency.
- **Logs → Loki** — structured logs; a record emitted inside a span carries its
  `trace_id` for one-click trace↔log correlation.

Flags: `-otel-endpoint`, `-otel-protocol` (`grpc`|`http`), `-otel-service-name`,
`-otel-sampling` (default `0.05`; errors always sampled), `-otel-capture-keys`
(records a **salted hash** of the key on store spans — never the plaintext).
Telemetry never fails a command: a dead collector logs `otel export failed
(dropped)` and drops the batch. Full walkthrough:
[`deploy/otel-lgtm/README.md`](./deploy/otel-lgtm/README.md); design:
[ADR-0017](./docs/adr/0017-opentelemetry-signal-model-and-otlp-export.md); complete
instrument inventory: [`docs/M16-observability.md`](./docs/M16-observability.md).

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
