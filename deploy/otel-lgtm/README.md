# Local LGTM stack for toykv observability (M16)

Point toykv's OpenTelemetry output at a local **Grafana LGTM** stack — **L**oki
(logs), **G**rafana (dashboards), **T**empo (traces), **M**imir (metrics) — to
see traces, metrics, and logs correlate by trace ID.

Everything runs from the single [`grafana/otel-lgtm`](https://github.com/grafana/docker-otel-lgtm)
image: an OpenTelemetry Collector fanning out to Loki/Tempo/Mimir, plus Grafana
with the datasources pre-wired.

## 1. Start the stack

```sh
docker compose -f deploy/otel-lgtm/compose.yaml up -d
```

Ports:

| Port | Purpose |
|---|---|
| `3000` | Grafana UI (anonymous admin — local only) |
| `4317` | OTLP/gRPC ingest (default `-otel-protocol grpc`) |
| `4318` | OTLP/HTTP ingest (`-otel-protocol http`) |

## 2. Run toykv pointed at it

Telemetry is **off unless `-otel-endpoint` is set** — with it unset, toykv
behaves and benchmarks exactly as before (the M16 no-op contract).

```sh
go run ./cmd/toykv -otel-endpoint localhost:4317
# or with a higher trace sample rate + hashed key capture while exploring:
go run ./cmd/toykv -otel-endpoint localhost:4317 -otel-sampling 1.0 -otel-capture-keys
```

Relevant flags (see `toykv -h` for the full list):

| Flag | Default | Notes |
|---|---|---|
| `-otel-endpoint` | `""` | OTLP collector `host:port`; empty disables all telemetry |
| `-otel-protocol` | `grpc` | `grpc` (4317) or `http` (4318) |
| `-otel-service-name` | `toykv` | `service.name` in every signal |
| `-otel-sampling` | `0.05` | trace ratio `[0,1]`; errors are always sampled |
| `-otel-capture-keys` | `false` | record a **salted hash** of the key on store spans — never the plaintext |

## 3. Drive some traffic

```sh
redis-cli -p 6390 set foo bar
redis-cli -p 6390 get foo
redis-cli -p 6390 lpush mylist a b c
redis-cli -p 6390 bgrewriteaof
```

## 4. Explore in Grafana

Open <http://localhost:3000>:

- **Explore → Tempo** — a `connection` span per client with `command` children,
  each with `store.<op>` and (for writes) `aof.append` siblings. A slow fsync
  shows up as `aof.append` span latency.
- **Explore → Mimir/Prometheus** — RED metrics per command
  (`toykv_command_duration_seconds`, `toykv_commands_total`), plus
  `toykv_keys`, `toykv_aof_size_bytes`, `toykv_connections_active`, and the
  `toykv_aof_fsync_duration_seconds` durability signal.
- **Explore → Loki** — structured logs; a log emitted inside a span (e.g.
  `aof append failed`, `bgrewriteaof completed`) carries the `trace_id`, so you
  can jump straight from a log line to its trace.

## Tear down

```sh
docker compose -f deploy/otel-lgtm/compose.yaml down
```

> **Note:** the anonymous-admin Grafana and open OTLP ports are for local
> viewing only. Never expose this container beyond localhost.
