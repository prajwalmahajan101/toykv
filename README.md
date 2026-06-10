# toykv

> Single-node in-memory KV store in Go. RESP2 wire protocol, AOF persistence, TTL, companion TUI.

Companion to [toymq](../toymq). Where toymq exercises the **log** pattern (append, replay, durable), toykv exercises the **map** pattern (in-memory, mutable, expirable). Two foundational network-server primitives, one Go module each.

**Status:** pre-implementation. Docs in [`docs/`](./docs/), starting with [PRD](./docs/PRD.md) and [ROADMAP](./docs/ROADMAP.md).

## What it is

- RESP2 wire protocol subset — `redis-cli` works against it.
- AOF persistence with configurable `appendfsync` policy (`always` | `everysec` | `no`).
- TTL with lazy + 1Hz sweep eviction.
- A line-oriented CLI (`toykv-cli`) — one-shot, REPL, and piped modes; modelled on `redis-cli`.
- A Bubble Tea TUI (`toykv-tui`) that talks RESP, renders live state, and performs every mutating command.

## What it isn't

- A Redis replacement.
- Production-ready (no auth, no TLS).
- Multi-node, replicated, or clustered.

See [PRD §4 Non-goals](./docs/PRD.md#4-non-goals-v1) and [SECURITY.md](./docs/SECURITY.md) before deploying anywhere networked.

## License

MIT. See [`LICENSE`](./LICENSE).
