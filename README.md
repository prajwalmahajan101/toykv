# toykv

[![CI](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml/badge.svg)](https://github.com/prajwalmahajan101/toykv/actions/workflows/ci.yml)

> Single-node in-memory KV store in Go. RESP2 wire protocol, AOF persistence, TTL, companion CLI and TUI.

Companion to [toymq](../toymq). Where toymq exercises the **log** pattern (append, replay, durable), toykv exercises the **map** pattern (in-memory, mutable, expirable). Two foundational network-server primitives, one Go module each.

**Status:** M0–M7 shipped (7 of 10). M8 (integration tests) and M9 (bench + v1.0.0 release) remain. See [ROADMAP](./docs/ROADMAP.md).

## What it is

- RESP2 wire protocol subset — `redis-cli` works against it.
- AOF persistence with configurable `appendfsync` policy (`always` | `everysec` | `no`), plus `BGREWRITEAOF` compaction.
- TTL with lazy + 1Hz sweep eviction; expiry round-trips through AOF v2.
- A line-oriented CLI (`toykv-cli`) — one-shot, REPL, and piped modes; modelled on `redis-cli`.
- A Bubble Tea TUI (`toykv-tui`) with two-pane keys/value layout, 2 Hz refresh, and every PRD §5.5 keybinding.

## What it isn't

- A Redis replacement.
- Production-ready (no auth, no TLS).
- Multi-node, replicated, or clustered.

See [PRD §4 Non-goals](./docs/PRD.md#4-non-goals-v1) and [SECURITY.md](./docs/SECURITY.md) before deploying anywhere networked.

## Quickstart

```sh
make build                       # produces bin/toykv, bin/toykv-cli, bin/toykv-tui

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

## Supported commands (PRD §5.1)

`PING`, `ECHO`, `GET`, `SET key value [EX seconds] [NX|XX]`, `DEL`, `EXISTS`, `EXPIRE`, `TTL`, `INCR`, `DECR`, `KEYS pattern`, `FLUSHDB`, `DBSIZE`, `BGREWRITEAOF`.

## Durability

`-fsync always` (default) provides the strong guarantee — zero acknowledged-write loss across SIGKILL + restart, proven by a crash-injection test in `internal/aof`. `everysec` trades up to one second of recent writes for throughput; `no` leaves durability to the OS. See [ADR-0003](./docs/adr/0003-aof-format-and-fsync-policy.md).

`BGREWRITEAOF` compacts the log into a fresh snapshot while traffic continues. The rewrite is crash-safe by atomic rename — see [ADR-0005](./docs/adr/0005-bgrewriteaof-dual-write-and-tmp-cleanup.md).

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
docs/
  PRD.md HLD.md LLD.md ROADMAP.md TESTING.md
  adr/          # architectural decision records
  journal/      # per-PR milestone journal
```

## Documentation

- [PRD](./docs/PRD.md) — product requirements
- [HLD](./docs/HLD.md), [LLD](./docs/LLD.md) — architecture
- [ROADMAP](./docs/ROADMAP.md) — milestone plan with risk ownership
- [TESTING](./docs/TESTING.md) — test layers (unit, integration, crash injection)
- [SECURITY](./docs/SECURITY.md), [CONTRIBUTING](./docs/CONTRIBUTING.md)
- [ADRs](./docs/adr/) — decision records, including the dep-policy relaxation in [ADR-0009](./docs/adr/0009-tui-bubble-tea-and-injectable-doer.md)

## Dependencies

Server and CLI binaries: stdlib only. TUI binary adds `github.com/charmbracelet/{bubbletea,lipgloss,bubbles}` and their transitive surface — see [ADR-0009](./docs/adr/0009-tui-bubble-tea-and-injectable-doer.md) for the rationale.

## License

MIT. See [`LICENSE`](./LICENSE).
