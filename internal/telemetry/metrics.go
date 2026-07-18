package telemetry

import (
	"errors"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// NoopMetrics returns a fully no-op Metrics handle for packages (store, aof)
// that want a non-nil handle by default and receive the live one via a
// setter/option only when the server wires telemetry. Recording into it is
// a no-op with no allocation of exported measurements.
func NoopMetrics() *Metrics {
	mx, _ := newMetrics(metricnoop.NewMeterProvider().Meter(scopeName))
	return mx
}

// Metrics holds every pre-created instrument handle toykv records into. It
// is built once from the meter (real or no-op) so the hot path never
// creates instruments per-call. Handles are safe to use concurrently.
//
// The struct grows across M16's metric tasks (§1.1 here; connections,
// keyspace, sweeper, AOF, replay, process follow).
type Metrics struct {
	// Command RED — §1.1, recorded at the dispatch chokepoint.
	Commands         metric.Int64Counter       // toykv.commands{command,status}
	CommandDuration  metric.Float64Histogram   // toykv.command.duration (s){command}
	CommandErrors    metric.Int64Counter       // toykv.command.errors{command,kind}
	CommandsInflight metric.Int64UpDownCounter // toykv.commands.inflight{command}

	// Connections / auth / TLS / protocol — §1.2.
	Connections         metric.Int64Counter       // toykv.connections
	ConnectionsActive   metric.Int64UpDownCounter // toykv.connections.active
	ConnectionDuration  metric.Float64Histogram   // toykv.connection.duration (s)
	ConnectionsRejected metric.Int64Counter       // toykv.connections.rejected{reason}
	ProtocolErrors      metric.Int64Counter       // toykv.protocol.errors
	AuthAttempts        metric.Int64Counter       // toykv.auth.attempts{result}
	TLSHandshakes       metric.Int64Counter       // toykv.tls.handshakes{result}
	ClientsByProtocol   metric.Int64UpDownCounter // toykv.clients.by_protocol{proto}

	// Keyspace & store — §1.3.
	KeyspaceHits   metric.Int64Counter // toykv.keyspace.hits
	KeyspaceMisses metric.Int64Counter // toykv.keyspace.misses
	KeysExpired    metric.Int64Counter // toykv.keys.expired{path}

	// TTL sweeper — §1.4.
	SweeperPasses   metric.Int64Counter     // toykv.sweeper.passes
	SweeperSampled  metric.Int64Counter     // toykv.sweeper.sampled
	SweeperEvicted  metric.Int64Counter     // toykv.sweeper.evicted
	SweeperDuration metric.Float64Histogram // toykv.sweeper.duration (s)

	// AOF / persistence — §1.5 (the observable gauges aof.size and
	// aof.rewrite.in_progress are registered server-side, not here).
	AOFAppends         metric.Int64Counter     // toykv.aof.appends
	AOFAppendBytes     metric.Int64Counter     // toykv.aof.append.bytes
	AOFFsyncs          metric.Int64Counter     // toykv.aof.fsyncs{policy}
	AOFFsyncDuration   metric.Float64Histogram // toykv.aof.fsync.duration (s){policy}
	AOFAppendErrors    metric.Int64Counter     // toykv.aof.append.errors
	AOFRewrites        metric.Int64Counter     // toykv.aof.rewrites{result}
	AOFRewriteDuration metric.Float64Histogram // toykv.aof.rewrite.duration (s)

	// AOF replay — §1.6 (recorded once at startup).
	AOFReplayRecords  metric.Int64Counter     // toykv.aof.replay.records
	AOFReplayBytes    metric.Int64Counter     // toykv.aof.replay.bytes
	AOFReplayDuration metric.Float64Histogram // toykv.aof.replay.duration (s)
}

// newMetrics creates all instrument handles from m. On a no-op meter this
// never errors; on a real meter an error means an invalid instrument
// definition (a build-time bug), surfaced at startup.
func newMetrics(m metric.Meter) (*Metrics, error) {
	b := &builder{m: m}
	mx := &Metrics{
		Commands: b.counter("toykv.commands",
			"Total commands dispatched, by command and status."),
		CommandDuration: b.histogram("toykv.command.duration", "s",
			"Command dispatch latency (enter→reply)."),
		CommandErrors: b.counter("toykv.command.errors",
			"Commands that produced an error reply, by command and error kind."),
		CommandsInflight: b.updown("toykv.commands.inflight",
			"Commands currently executing."),

		Connections: b.counter("toykv.connections",
			"Total connections accepted."),
		ConnectionsActive: b.updown("toykv.connections.active",
			"Connections currently being served."),
		ConnectionDuration: b.histogram("toykv.connection.duration", "s",
			"Connection lifetime (accept→close)."),
		ConnectionsRejected: b.counter("toykv.connections.rejected",
			"Connections rejected before serving, by reason."),
		ProtocolErrors: b.counter("toykv.protocol.errors",
			"RESP protocol / framing errors that dropped a connection."),
		AuthAttempts: b.counter("toykv.auth.attempts",
			"AUTH attempts, by result."),
		TLSHandshakes: b.counter("toykv.tls.handshakes",
			"TLS handshakes, by result."),
		ClientsByProtocol: b.updown("toykv.clients.by_protocol",
			"Connected clients by negotiated wire protocol."),

		KeyspaceHits: b.counter("toykv.keyspace.hits",
			"Read commands that found a live key."),
		KeyspaceMisses: b.counter("toykv.keyspace.misses",
			"Read commands that found no live key (absent or expired)."),
		KeysExpired: b.counter("toykv.keys.expired",
			"Keys evicted due to expiry, by path (lazy|sweeper)."),

		SweeperPasses: b.counter("toykv.sweeper.passes",
			"TTL sweeper tick passes."),
		SweeperSampled: b.counter("toykv.sweeper.sampled",
			"Keys sampled by the TTL sweeper."),
		SweeperEvicted: b.counter("toykv.sweeper.evicted",
			"Keys evicted by the TTL sweeper."),
		SweeperDuration: b.histogram("toykv.sweeper.duration", "s",
			"TTL sweeper tick wall time."),

		AOFAppends: b.counter("toykv.aof.appends",
			"AOF records appended."),
		AOFAppendBytes: b.counter("toykv.aof.append.bytes",
			"Bytes appended to the AOF."),
		AOFFsyncs: b.counter("toykv.aof.fsyncs",
			"AOF fsyncs, by policy."),
		AOFFsyncDuration: b.histogram("toykv.aof.fsync.duration", "s",
			"AOF fsync latency, by policy — the durability-latency signal."),
		AOFAppendErrors: b.counter("toykv.aof.append.errors",
			"AOF append failures — a durability breach."),
		AOFRewrites: b.counter("toykv.aof.rewrites",
			"BGREWRITEAOF completions, by result."),
		AOFRewriteDuration: b.histogram("toykv.aof.rewrite.duration", "s",
			"BGREWRITEAOF wall time (start→finalize)."),

		AOFReplayRecords: b.counter("toykv.aof.replay.records",
			"Records applied during startup AOF replay."),
		AOFReplayBytes: b.counter("toykv.aof.replay.bytes",
			"Bytes read during startup AOF replay."),
		AOFReplayDuration: b.histogram("toykv.aof.replay.duration", "s",
			"Startup AOF replay wall time."),
	}
	return mx, b.err()
}

// builder accumulates instrument-creation errors so newMetrics reads as a
// flat list of definitions rather than error-checking every line.
type builder struct {
	m    metric.Meter
	errs []error
}

func (b *builder) counter(name, desc string) metric.Int64Counter {
	c, err := b.m.Int64Counter(name, metric.WithDescription(desc))
	b.record(err)
	return c
}

func (b *builder) updown(name, desc string) metric.Int64UpDownCounter {
	c, err := b.m.Int64UpDownCounter(name, metric.WithDescription(desc))
	b.record(err)
	return c
}

func (b *builder) histogram(name, unit, desc string) metric.Float64Histogram {
	h, err := b.m.Float64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc))
	b.record(err)
	return h
}

func (b *builder) record(err error) {
	if err != nil {
		b.errs = append(b.errs, err)
	}
}

func (b *builder) err() error { return errors.Join(b.errs...) }
