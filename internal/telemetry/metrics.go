package telemetry

import (
	"errors"

	"go.opentelemetry.io/otel/metric"
)

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
