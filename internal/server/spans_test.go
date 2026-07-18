package server

import (
	"io"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// spanServer builds a server whose telemetry records spans into an in-memory
// recorder, for trace assertions.
func spanServer(t *testing.T) (*Server, *tracetest.SpanRecorder) {
	t.Helper()
	tp, sr := telemetry.TestSpanProviders()
	s, err := New(Config{
		Addr:      "127.0.0.1:0",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:     store.New(),
		Telemetry: tp,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, sr
}

func spanByName(spans []sdktrace.ReadOnlySpan, name string) (sdktrace.ReadOnlySpan, bool) {
	for _, sp := range spans {
		if sp.Name() == name {
			return sp, true
		}
	}
	return nil, false
}

func spanAttr(sp sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range sp.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

// TestCommandSpan verifies a dispatched command produces a "command" span
// whose parent is the connection span, with the expected attributes, and
// that an error reply sets the span's error status + error.kind.
func TestCommandSpan(t *testing.T) {
	s, sr := spanServer(t)

	// Root a connection span manually (as handleConn would), then dispatch
	// through observeCommand so the command span nests under it.
	connCtx, connSpan := s.tel.Tracer.Start(t.Context(), "connection")
	cs := newConnState(1, true)
	cs.ctx = connCtx

	s.observeCommand(cs, cmd("SET", "k", "v"))   // ok
	s.observeCommand(cs, cmd("LPUSH", "k", "x")) // WRONGTYPE
	connSpan.End()

	spans := sr.Ended()
	cmdSpans := 0
	var errSpan sdktrace.ReadOnlySpan
	for _, sp := range spans {
		if sp.Name() == "command" {
			cmdSpans++
			if v, _ := spanAttr(sp, "error.kind"); v == "wrongtype" {
				errSpan = sp
			}
		}
	}
	if cmdSpans != 2 {
		t.Fatalf("command spans = %d, want 2", cmdSpans)
	}

	ok, found := spanByName(spans, "command")
	if !found {
		t.Fatal("no command span recorded")
	}
	if v, _ := spanAttr(ok, "db.system"); v != "toykv" {
		t.Errorf("db.system = %q, want toykv", v)
	}
	if v, _ := spanAttr(ok, "db.operation"); v == "" {
		t.Error("db.operation attr missing")
	}

	if errSpan == nil {
		t.Fatal("no command span with error.kind=wrongtype")
	}
	if errSpan.Status().Code.String() != "Error" {
		t.Errorf("error span status = %v, want Error", errSpan.Status().Code)
	}
	// The command span must be a child of the connection span.
	connSp, ok2 := spanByName(spans, "connection")
	if !ok2 {
		t.Fatal("no connection span recorded")
	}
	if errSpan.Parent().SpanID() != connSp.SpanContext().SpanID() {
		t.Errorf("command span parent = %v, want connection span %v",
			errSpan.Parent().SpanID(), connSp.SpanContext().SpanID())
	}
}
