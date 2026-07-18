# M16 — Observability telemetry inventory

> The **complete** set of metrics, logs, and traces toykv emits under M16.
> This is the implementation checklist for the milestone: every row is a
> real instrumentation point, anchored to the code that produces it.
> Signals export over **OTLP** to the Grafana **LGTM** stack — **L**oki
> (logs), **G**rafana (dashboards), **T**empo (traces), **M**imir
> (metrics). All of it is **off unless `-otel-endpoint` is set**.

Instrument naming uses OpenTelemetry dotted form (`toykv.commands`); the
OTLP→Prometheus/Mimir exporter maps dots to underscores and appends unit
suffixes (`toykv_command_duration_seconds`). Nothing here carries a **key
name or value** as an attribute — labels are bounded, low-cardinality sets
(command name, status, policy), never user data. Key capture on spans is
opt-in and hashed (see [Cardinality & privacy](#cardinality--privacy)).

---

## 1. Metrics

### 1.1 Command RED (rate / errors / duration) — hot path
Instrumented at the single dispatch chokepoint (`internal/server/conn.go`
→ `s.dispatch(cs, argv)`), so no per-handler wiring and every command is
covered uniformly.

| Instrument | Type | Attributes | Source |
|---|---|---|---|
| `toykv.commands` | Counter | `command`, `status` (ok\|error) | one per dispatched command |
| `toykv.command.duration` | Histogram (s) | `command` | dispatch enter→reply |
| `toykv.command.errors` | Counter | `command`, `kind` (wrongtype\|noauth\|syntax\|notinteger\|overflow\|badpattern\|invalid_cursor\|unknown\|protocol\|arity) | derived from the `-ERR…` reply prefix |
| `toykv.commands.inflight` | UpDownCounter | `command` | inc on enter, dec on reply |

`command` is the upper-cased verb from the dispatch table (bounded: the
~38 commands in `dispatch.go`); an unknown verb is labelled `UNKNOWN` so a
command-flood can't explode cardinality.

### 1.2 Connections & auth
Instrumented in `handleConn` / the accept loop (`server.go`) and the
`authenticate` helper.

| Instrument | Type | Attributes | Source |
|---|---|---|---|
| `toykv.connections` | Counter | — | accept loop, per accepted conn |
| `toykv.connections.active` | UpDownCounter | — | the existing `clientCount` gauge (inc on accept / dec on goroutine exit) |
| `toykv.connection.duration` | Histogram (s) | — | `handleConn` enter→return |
| `toykv.connections.rejected` | Counter | `reason` (emfile) | EMFILE backoff branch |
| `toykv.protocol.errors` | Counter | — | `ReadCommand` error branch |
| `toykv.auth.attempts` | Counter | `result` (success\|wrongpass) | `authenticate` |
| `toykv.tls.handshakes` | Counter | `result` (success\|error) | TLS listener wrap (M12) |
| `toykv.clients.by_protocol` | UpDownCounter | `proto` (resp2\|resp3) | set/flipped on `HELLO` |

### 1.3 Keyspace & store
| Instrument | Type | Attributes | Source |
|---|---|---|---|
| `toykv.keys` | UpDownCounter (gauge) | — | `store.DBSize()` (observable callback) |
| `toykv.keyspace.hits` | Counter | — | `GET`/`HGET`/`LINDEX` hit |
| `toykv.keyspace.misses` | Counter | — | same, miss (incl. expired) |
| `toykv.keys.expired` | Counter | `path` (lazy\|sweeper) | lazy eviction in `Get`/`getForWrite` + sweeper |

### 1.4 TTL sweeper
Instrumented in `Sweeper.tick` (`internal/store/sweeper.go`), which already
returns `sampled`/`evicted`.

| Instrument | Type | Source |
|---|---|---|
| `toykv.sweeper.passes` | Counter | one per `tick` |
| `toykv.sweeper.sampled` | Counter | `totalSampled` |
| `toykv.sweeper.evicted` | Counter | `totalEvicted` |
| `toykv.sweeper.duration` | Histogram (s) | `tick` wall time |

### 1.5 AOF / persistence
Instrumented in `internal/aof/writer.go` (`Append`, fsync path) and
`internal/server/bgrewriteaof.go`.

| Instrument | Type | Attributes | Source |
|---|---|---|---|
| `toykv.aof.appends` | Counter | — | `Append` |
| `toykv.aof.append.bytes` | Counter | — | RESP-encoded record length |
| `toykv.aof.fsyncs` | Counter | `policy` (always\|everysec\|no) | fsync path |
| `toykv.aof.fsync.duration` | Histogram (s) | `policy` | **the durability-latency signal** — where a slow disk shows |
| `toykv.aof.size` | UpDownCounter (gauge) | — | `Writer.Size()` (observable) |
| `toykv.aof.append.errors` | Counter | — | **durability breach** — the `appendIfLive` failure; also ERROR log + span error |
| `toykv.aof.rewrites` | Counter | `result` (ok\|error) | `bgrewriteaof` completion |
| `toykv.aof.rewrite.duration` | Histogram (s) | — | rewrite start→finalize |
| `toykv.aof.rewrite.in_progress` | UpDownCounter (gauge) | — | existing `rewriteInFlight` |

### 1.6 Replay (startup, one-shot)
Recorded once from `aof.ReplayStats` (already stored on the server for INFO).

| Instrument | Type | Source |
|---|---|---|
| `toykv.aof.replay.records` | Counter (once) | `replayStats.Records` |
| `toykv.aof.replay.bytes` | Counter (once) | `replayStats.Bytes` |
| `toykv.aof.replay.duration` | Histogram (once) | `replayStats.Duration` |

### 1.7 Process / runtime
Via `go.opentelemetry.io/contrib/instrumentation/runtime` plus a few
server-scoped gauges.

| Instrument | Type | Source |
|---|---|---|
| `process.runtime.go.*` (goroutines, gc, mem, heap) | (runtime pkg) | OTel runtime instrumentation |
| `toykv.uptime` | UpDownCounter (gauge) | `now - startTime` |
| `toykv.build.info` | Gauge (=1) | `version` attribute (`serverVersion`) |

---

## 2. Logs

Every existing `slog` call keeps its shape; M16 adds an `slog.Handler` that
**also** emits OTLP log records, so each record carries `service.name`,
`service.version`, and — when emitted inside a span — `trace_id` /
`span_id` for one-click correlation from a Grafana trace to its logs.
Console `slog` stays the default when OTel is off.

| Level | Event | Where (today / new) | Key fields |
|---|---|---|---|
| INFO | `listening` | `server.go:187` | addr |
| INFO | `aof ready` | `server.go:114` | dir, fsync, replay_records, replay_bytes, replay_duration |
| INFO | `shutdown clean` | `server.go:241` | — |
| INFO | `tls enabled` | *(new)* server start when `TLS != nil` | min_version |
| INFO | `protected-mode decision` | *(new, M15 hook)* | bind, mode, refused(bool) |
| INFO | `bgrewriteaof started` / `…completed` | *(new)* bgrewriteaof.go | duration, old_size, new_size |
| WARN | `accept … (EMFILE), backing off` | `server.go:212` | delay |
| WARN | `slow fsync` | *(new)* writer.go fsync path | policy, duration (over threshold) |
| WARN | `otel export failed (dropped)` | *(new)* exporter error hook | signal, err — **never fails a command** |
| ERROR | `aof append failed` | `commands.go:28` | err, argv0 — **durability breach** |
| ERROR | `aof rewrite failed` | `bgrewriteaof.go:48` | err |
| ERROR | `server init failed` / `server run failed` | `main.go:79/88` | err |
| ERROR | `tls handshake failed` | *(new)* accept path | remote, err |
| DEBUG | `protocol error` / `write reply failed` / `flush failed` | `conn.go:28/35/39` | remote, err |
| DEBUG | `connection open` / `connection close` | *(new)* handleConn | remote, conn_id, duration |
| DEBUG | `auth attempt` | *(new)* authenticate | result — **no password, ever** |

---

## 3. Traces

Spans are **server-originated roots** — RESP carries no trace-context
headers (unlike HTTP/gRPC), so there is no inbound propagation; toykv
starts each connection's tree itself. The command span context threads
`dispatch → store op → aof append → fsync` (M16 adds a `context.Context`
carried on `connState` / passed through `dispatch`).

```
connection                         (root, per accepted conn — handleConn)
├─ event: auth.success | auth.failure
└─ command                         (child, per dispatched command — dispatch)
   ├─ store.<op>                    (Get/Set/LPush/HSet/Scan/… — store call)
   └─ aof.append                    (mutating cmd only, persistence on)
      └─ aof.fsync                  (fsync per policy — durability latency)

aof.rewrite                         (async, from BGREWRITEAOF — links to the
├─ aof.snapshot                      triggering command span)
└─ aof.finalize                     (fold side buffer → rename → dir fsync → fd swap)

aof.replay                          (startup, before Accept — not under a connection)
sweeper.tick                        (per sweep pass, low sampling — sampled/evicted)
```

| Span | Parent | Key attributes | Status=Error when |
|---|---|---|---|
| `connection` | root | `net.peer.address`, `connection.id`, `client.protocol` (after HELLO) | conn dropped on protocol error |
| `command` | connection | `db.operation`=`<CMD>`, `db.system`=`toykv`, `argc`, `resp.proto`, `authenticated`, `reply.kind`, `error.kind` | reply is `-ERR…` |
| `store.<op>` | command | `type` (string\|list\|hash), `hit`(bool for reads); key hashed & opt-in | store returns `ErrWrongType`/error |
| `aof.append` | command | `bytes`, `policy` | append error (durability breach) |
| `aof.fsync` | aof.append | `policy` | fsync error |
| `aof.rewrite` | link→command | `old_size`, `new_size` | rewrite fails |
| `aof.snapshot` / `aof.finalize` | aof.rewrite | `keys`, `bytes` | finalize (rename/fsync) fails |
| `aof.replay` | root | `records`, `bytes` | replay parse failure (server aborts start) |
| `sweeper.tick` | root | `sampled`, `evicted` | — |

Sampling: `command`/`store`/`aof` spans use a parent-based ratio sampler
(configurable; default low, e.g. 1–5%) so the hot path isn't trace-bound;
`aof.rewrite`, `aof.replay`, and any span with `error.kind` are
**always-sampled** (record-and-sample on error) so failures are never
dropped.

---

## Cardinality & privacy

- **No key names or values** in metric labels, ever — they are unbounded
  user data and would blow up Mimir cardinality. `command`, `status`,
  `policy`, `kind`, `result`, `proto` are the only label sets, all fixed
  and small.
- **Span key capture is opt-in and hashed.** With `-otel-capture-keys`
  off (default) `store.<op>` records no key; on, it records a salted hash,
  never the plaintext key or value.
- **Passwords never appear** in any log, span, or metric — the `auth
  attempt` log records only `result`, mirroring the M12 no-oracle rule.
- **Telemetry failure is never a client-visible failure** — a dead OTLP
  endpoint logs `otel export failed (dropped)` and drops the batch; the
  command still succeeds. This is an M16 owned-risk-test assertion.

## As-built deviations (M16 shipped 2026-07-18)

This inventory was the design target; a few calls were made during
implementation and are recorded here (and in [ADR-0017](./adr/0017-opentelemetry-signal-model-and-otlp-export.md)):

- **`store.<op>` spans are created at the server→store boundary**, not by
  threading `context.Context` through the store package. The store keeps its
  ctx-free API; the emitted trace tree (`command → store.<op>`) is identical.
  Consequently the store package is untouched by tracing and its metrics
  (§1.3 keyspace hits/misses at the server read handlers; keys.expired /
  sweeper counters in the store) are the store-side coverage.
- **`aof.append` covers append+fsync as one span** — the separate `aof.fsync`
  child (§3 tree) is **not** emitted; fsync latency is the
  `toykv.aof.fsync.duration` metric (§1.5). The append span is a pure outer
  wrapper, so the mutate→append→fsync→reply order is untouched.
- **`aof.rewrite` and `aof.replay` are root spans**; `aof.snapshot` /
  `aof.finalize` sub-spans are **not** emitted (they would need context threaded
  into the rewriter). The "link→triggering command" on rewrite is dropped
  (BGREWRITEAOF is async and replies before the rewrite runs).
- **Log events**: the trace-correlated subset shipped (`aof append failed`,
  `aof rewrite failed`, `bgrewriteaof started`/`completed`, `auth attempt`, and
  the exporter `otel export failed (dropped)` WARN) use `…Context` logging so
  they carry the active trace id. The remaining §2 rows (e.g. `slow fsync`,
  per-connection open/close) remain console `slog` without OTLP-context wiring.

## Coverage check (every surface accounted for)

| Surface | Metrics | Logs | Traces |
|---|---|---|---|
| Command dispatch (all ~38) | RED §1.1 | debug on error | `command` span |
| Connection lifecycle | §1.2 | open/close, EMFILE | `connection` span |
| Auth / TLS | §1.2 | auth attempt, tls | `auth.*` events |
| Keyspace / store ops | §1.3 | — | `store.<op>` span |
| TTL sweeper | §1.4 | — | `sweeper.tick` span |
| AOF append + fsync | §1.5 | append-fail ERROR | `aof.append`/`aof.fsync` |
| AOF rewrite (BGREWRITEAOF) | §1.5 | start/complete/fail | `aof.rewrite` tree |
| AOF replay (startup) | §1.6 | `aof ready` | `aof.replay` span |
| Process / runtime | §1.7 | init/run failures | — |
| Telemetry pipeline itself | — | export-failed WARN | — |
