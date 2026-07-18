# ADR-0017: OpenTelemetry signal model & OTLP export → LGTM

- Status: Accepted
- Date: 2026-07-18
- Milestone: M16
- PR: _feat/observability_

## Context

The v2 goal is a "deployable, safe-by-default, **observable** single-node KV."
M10–M15 delivered every command / connection / persistence surface; M16 makes
the goal-word *observable* true by instrumenting that surface with the three
OpenTelemetry signals — logs, metrics, traces — exported over **OTLP** to a
Grafana **LGTM** stack (Loki / Tempo / Mimir, viewed in Grafana). The complete
instrument inventory is [`docs/M16-observability.md`](../M16-observability.md);
this ADR records the *design calls* that inventory implies.

Constraints the milestone imposed on itself:

- **Additive, off by default.** RESP3, AOF v3, and AUTH/TLS were all opt-in, so
  M15's protected mode is what earns the `2.0.0` major (ADR-0016). Observability
  must not move the semver needle — with no endpoint configured it has to be a
  true no-op, byte- and benchmark-identical to the pre-M16 binary.
- **Telemetry never fails a command.** A dead collector must never turn a
  successful command into a client error.
- **Durability ordering is sacred.** Instrumentation must not reorder
  mutate → append → fsync → reply; the M3/M11 crash-durability suite must pass
  unchanged with spans compiled in.
- **Cardinality & privacy.** No key names or values in metric labels; passwords
  never in any signal.

## Decision

**One `internal/telemetry` package owns the OTel wiring; when no
`-otel-endpoint` is set the global providers are the SDK no-ops, so
instrumentation is created unconditionally and costs nothing on the hot path —
no `if enabled` guard anywhere. Signals export over OTLP to a single
`grafana/otel-lgtm` image. Spans are server-originated roots; the store package
stays context-free and its `store.<op>` spans are created at the server→store
boundary.**

### No-op by construction

`telemetry.Init` installs the SDK no-op `TracerProvider`/`MeterProvider`/
`LoggerProvider` as the OTel globals when the endpoint is empty. Instrument
handles (`telemetry.Metrics`) and span-openers are built against those globals
and called unconditionally; a no-op span/measurement is the disabled cost.
`Providers.Tracer`/`Meter` are always non-nil, so the hot path never nil-checks.

### Server-originated spans, context on `connState`

RESP carries no inbound trace-context headers (unlike HTTP/gRPC), so toykv
*originates* each connection's span tree — a root `connection` span per
`handleConn`, a child `command` span per dispatch. The command context is
carried on `connState.ctx` (accessor `context()` defaults to
`context.Background()` so replay and tests are safe with a bare `connState{}`).

### Store spans at the server boundary (the churn call)

The inventory shows `store.<op>` spans as children of `command`. The faithful
literal reading — thread `context.Context` into all ~30 store methods — would
touch ~250 call sites, almost all in store unit tests, for *identical* emitted
telemetry. **Decision: create the `store.<op>` span at the server→store
boundary** (`observeCommand`), parented to the command span, reading `hit`/error
attributes from the reply. The store package keeps its clean, ctx-free API. The
trace tree is byte-for-byte what the inventory specifies; only the span's
creation site moves.

### AOF spans wrap, never reorder

`aof.append` is a single command-child span wrapping the whole `appendIfLive` →
`Writer.Append` call (which itself does append **then** fsync). A slow fsync
still surfaces as `aof.append` span latency, so the durability-latency trace
signal is preserved **without** touching the writer's internal ordering — the
span is a pure outer wrapper. `appendIfLive` gained a `ctx` parameter, threaded
through the mutating handlers/helpers, so the span nests under the command.
`aof.rewrite` and `aof.replay` are **root** spans (rewrite is async — BGREWRITEAOF
replies before it finishes, so there is no live command span to link; replay
runs before `Accept`). `sweeper.tick` gets a span via a tracer handed to the
sweeper.

### Export & resilience

OTLP exporters (gRPC on 4317 / HTTP on 4318, `-otel-protocol`) connect lazily
and batch asynchronously. A global `otel.ErrorHandler` logs export failures as
`otel export failed (dropped)` and drops them — a dead collector never reaches a
command. Only a *malformed* config (unknown protocol) is fatal, surfaced before
the listener opens. A parent-based ratio sampler (`-otel-sampling`, default
0.05) keeps the hot path off the trace fast-path; errors are recorded.

### Logs bridge & privacy

An `slog.Handler` (`telemetry.NewSlogHandler`) fans out to the existing console
handler **and** an `otelslog` OTLP handler when enabled; disabled, it returns
the base handler unchanged (console shape untouched). A record logged with a
span context carries `trace_id`/`span_id` for one-click trace↔log correlation.
Key capture on store spans is **opt-in** (`-otel-capture-keys`) and records a
**salted SHA-256** truncated hex, never the plaintext; metric labels are a fixed
low-cardinality set (`command`, `status`, `kind`, `policy`, `result`, `proto`);
passwords never appear in any signal.

## Consequences

**Positive.** "Observable" is now backed: RED metrics per command, a
connection→command→{store,aof} trace tree, and trace-correlated logs, all in a
one-command local LGTM stack. Off-by-default is a true no-op (benchmark parity is
a release gate), so the semver story is unchanged — M15 still earns the major.
The store package stayed clean, so the tracing work added ~0 churn to store unit
tests. Durability ordering is provably untouched (crash + e2e suites green with
instrumentation in).

**Negative / neutral.** The `aof.fsync`, `aof.snapshot`, and `aof.finalize`
sub-spans in the inventory tree are **not** emitted — `aof.append` covers
append+fsync as one span (fsync latency is also the `aof.fsync.duration`
metric), and the rewrite sub-spans would need context threaded into the
rewriter. `aof.rewrite`/`aof.replay` are roots, not command-linked. `store.<op>`
spans are leaf spans whose duration spans the whole handler (arg-parse included),
a minor imprecision. The OTel SDK adds a real dependency tree (~20 modules). Even
no-op, each command builds a small bounded attribute set (~29 allocs/op measured
on the disabled path) — accepted, and guarded by the parity benchmark.

## Alternatives considered

- **Thread `ctx` into every store method** (the literal inventory reading).
  Rejected: ~250 call-site edits, overwhelmingly in tests, for identical emitted
  telemetry. The server-boundary span produces the same tree with none of the
  store-package churn.
- **`if telemetryEnabled` guards on the hot path.** Rejected: OTel's own no-op
  providers make the disabled path free without scattering conditionals; the
  parity benchmark is the backstop.
- **Nested `aof.fsync` span inside the writer.** Rejected for M16: would require
  a `ctx` on `Writer.Append` (10+ aof test call sites) and touch the durability
  path; the append span's latency + the fsync-duration metric already carry the
  signal.
- **Prometheus `/metrics` scrape endpoint** (the v2.x backlog item). Folded into
  OTLP → Mimir (push); a pull endpoint stays optional/deferred.
- **A map-shaped `INFO`.** Already rejected in ADR-0014; RED metrics now ship the
  same gauge sources over OTLP without touching `INFO`'s byte-compat.

## References

- ROADMAP.md §M16 (observability), §"Breaking risk" (M16 adds none)
- [`docs/M16-observability.md`](../M16-observability.md) — the full instrument inventory + as-built deviations
- [`deploy/otel-lgtm/`](../../deploy/otel-lgtm/) — the local LGTM stack + walkthrough
- SECURITY.md (telemetry-never-fails, key-capture privacy, local-stack note)
- Related ADRs: [[0011]] (per-connection protocol state — the `connState` the
  span context rides on), [[0012]] (AOF v3 — the append path `aof.append` wraps),
  [[0014]] (INFO wire form — the gauge sources reused as metrics), [[0016]] (the
  protected-mode break that earns `2.0.0`; M16 adds no semver weight)
