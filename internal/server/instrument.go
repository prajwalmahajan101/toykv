package server

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// recordKeyspace records a §1.3 keyspace hit or miss for a read command
// (GET / HGET / LINDEX). A miss covers both an absent and an expired key.
func (s *Server) recordKeyspace(hit bool) {
	if hit {
		s.tel.Metrics.KeyspaceHits.Add(context.Background(), 1)
	} else {
		s.tel.Metrics.KeyspaceMisses.Add(context.Background(), 1)
	}
}

// resultAttr / protoAttr build the small bounded attribute sets shared by
// the §1.2 connection/auth/TLS instruments, so call sites stay terse.
func resultAttr(result string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("result", result))
}

func protoAttr(p resp.Proto) metric.MeasurementOption {
	name := "resp2"
	if p == resp.Proto3 {
		name = "resp3"
	}
	return metric.WithAttributes(attribute.String("proto", name))
}

// observeCommand is the live-traffic RED chokepoint (§1.1). It wraps a
// single dispatch with the command counter, latency histogram, error
// counter, and in-flight gauge, then returns the reply unchanged. Startup
// replay calls s.dispatch directly and is intentionally NOT metered — those
// are not live client commands.
//
// Instruments are no-ops when telemetry is disabled, so this adds only a
// label build and a few no-op records to the hot path.
func (s *Server) observeCommand(cs *connState, argv [][]byte) resp.Value {
	m := s.tel.Metrics
	name := commandLabel(argv)

	// Command span, child of the connection span on cs.ctx. Swap the command
	// context onto cs for the dispatch so any store/aof spans nest under this
	// command rather than directly under the connection; restore on return.
	cmdCtx, span := s.tel.Tracer.Start(cs.context(), "command")
	defer span.End()
	span.SetAttributes(
		attribute.String("db.system", "toykv"),
		attribute.String("db.operation", name),
		attribute.Int("argc", len(argv)),
	)
	prevCtx := cs.ctx
	cs.ctx = cmdCtx
	defer func() { cs.ctx = prevCtx }()

	cmdAttr := attribute.String("command", name)
	m.CommandsInflight.Add(cmdCtx, 1, metric.WithAttributes(cmdAttr))

	// Real wall-clock latency (monotonic via time.Since); independent of the
	// injectable TTL clock, which must not distort measured duration.
	start := time.Now()
	reply := s.dispatch(cs, argv)
	elapsed := time.Since(start)

	m.CommandsInflight.Add(cmdCtx, -1, metric.WithAttributes(cmdAttr))
	m.CommandDuration.Record(cmdCtx, elapsed.Seconds(), metric.WithAttributes(cmdAttr))

	status := "ok"
	if reply.Kind == resp.KindError {
		status = "error"
		kind := errorKind(reply.Str)
		m.CommandErrors.Add(cmdCtx, 1, metric.WithAttributes(
			cmdAttr, attribute.String("kind", kind),
		))
		span.SetAttributes(attribute.String("error.kind", kind))
		span.SetStatus(codes.Error, reply.Str)
	}
	m.Commands.Add(cmdCtx, 1, metric.WithAttributes(
		cmdAttr, attribute.String("status", status),
	))
	// Post-dispatch attrs capture the connection's state after commands that
	// mutate it (HELLO → proto, AUTH → authenticated).
	span.SetAttributes(
		attribute.Int("resp.proto", int(cs.proto)),
		attribute.Bool("authenticated", cs.authenticated),
		attribute.String("reply.kind", replyKindName(reply.Kind)),
	)
	return reply
}

// replyKindName maps a RESP frame kind to a bounded, human-readable label
// for the command span's reply.kind attribute.
func replyKindName(k resp.Kind) string {
	switch k {
	case resp.KindSimpleString:
		return "simple"
	case resp.KindError:
		return "error"
	case resp.KindInteger:
		return "integer"
	case resp.KindBulkString:
		return "bulk"
	case resp.KindArray:
		return "array"
	case resp.KindMap:
		return "map"
	case resp.KindSet:
		return "set"
	case resp.KindNull:
		return "null"
	case resp.KindDouble:
		return "double"
	case resp.KindBoolean:
		return "boolean"
	case resp.KindVerbatim:
		return "verbatim"
	case resp.KindPush:
		return "push"
	default:
		return "other"
	}
}

// protoName is the bounded label for a wire protocol version.
func protoName(p resp.Proto) string {
	if p == resp.Proto3 {
		return "resp3"
	}
	return "resp2"
}

// commandLabel returns the bounded metric label for a command: the
// upper-cased verb when it exists in the dispatch table, else "UNKNOWN".
// Bounding to the table (~38 verbs) means a flood of junk verbs cannot
// explode metric cardinality.
func commandLabel(argv [][]byte) string {
	if len(argv) == 0 {
		return "UNKNOWN"
	}
	name := upperASCII(argv[0])
	if _, ok := commands[name]; ok {
		return name
	}
	return "UNKNOWN"
}

// errorKind classifies an error reply string into a bounded label set for
// toykv.command.errors. It reads the RESP error prefix / message; the value
// is always one of a fixed set (no user data), so cardinality stays bounded.
func errorKind(msg string) string {
	switch {
	case strings.HasPrefix(msg, "WRONGTYPE"):
		return "wrongtype"
	case strings.HasPrefix(msg, "NOAUTH"):
		return "noauth"
	case strings.HasPrefix(msg, "NOPROTO"):
		return "noproto"
	}
	// Remaining kinds are all "ERR …" replies, matched by message content.
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "wrong number of arguments"):
		return "arity"
	case strings.Contains(low, "unknown command"):
		return "unknown"
	case strings.Contains(low, "not an integer"):
		return "notinteger"
	case strings.Contains(low, "overflow"):
		return "overflow"
	case strings.Contains(low, "bad pattern"):
		return "badpattern"
	case strings.Contains(low, "invalid cursor"):
		return "invalid_cursor"
	case strings.Contains(low, "syntax error"):
		return "syntax"
	case strings.Contains(low, "protocol error"):
		return "protocol"
	case strings.Contains(low, "no such key"):
		return "nosuchkey"
	default:
		return "other"
	}
}
