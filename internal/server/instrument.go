package server

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

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
	ctx := context.Background() // T8 threads the per-connection span context

	cmdAttr := attribute.String("command", name)
	m.CommandsInflight.Add(ctx, 1, metric.WithAttributes(cmdAttr))

	// Real wall-clock latency (monotonic via time.Since); independent of the
	// injectable TTL clock, which must not distort measured duration.
	start := time.Now()
	reply := s.dispatch(cs, argv)
	elapsed := time.Since(start)

	m.CommandsInflight.Add(ctx, -1, metric.WithAttributes(cmdAttr))
	m.CommandDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(cmdAttr))

	status := "ok"
	if reply.Kind == resp.KindError {
		status = "error"
		m.CommandErrors.Add(ctx, 1, metric.WithAttributes(
			cmdAttr, attribute.String("kind", errorKind(reply.Str)),
		))
	}
	m.Commands.Add(ctx, 1, metric.WithAttributes(
		cmdAttr, attribute.String("status", status),
	))
	return reply
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
