package server

import (
	"io"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// collectUntil polls the reader until cond holds or a short deadline passes,
// returning the last snapshot. Connection-close instrumentation runs in the
// connection goroutine's defers, so it lands slightly after the client sees
// the socket close — hence the bounded poll rather than a single Collect.
func collectUntil(t *testing.T, reader *sdkmetric.ManualReader, cond func(metricdata.ResourceMetrics) bool) metricdata.ResourceMetrics {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rm := collect(t, reader)
		if cond(rm) || time.Now().After(deadline) {
			return rm
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestConnectionMetrics exercises the §1.2 connection lifecycle: an accepted
// connection that serves a command then closes must leave the active and
// by-protocol gauges balanced at zero and record exactly one lifetime sample.
func TestConnectionMetrics(t *testing.T) {
	s, reader := metricServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	writeCmd(t, w, "PING")
	readReply(t, r)
	_ = c.Close()

	rm := collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool {
		return sumInt64(rm, "toykv.connections.active", nil) == 0 &&
			histCount(rm, "toykv.connection.duration") >= 1
	})

	if got := sumInt64(rm, "toykv.connections", nil); got < 1 {
		t.Errorf("toykv.connections = %d, want >= 1", got)
	}
	if got := sumInt64(rm, "toykv.connections.active", nil); got != 0 {
		t.Errorf("connections.active net = %d, want 0", got)
	}
	// Balanced across accept(+resp2) → close(-resp2), no leak.
	if got := sumInt64(rm, "toykv.clients.by_protocol", nil); got != 0 {
		t.Errorf("clients.by_protocol net = %d, want 0", got)
	}
	if got := histCount(rm, "toykv.connection.duration"); got < 1 {
		t.Errorf("connection.duration count = %d, want >= 1", got)
	}
}

// TestHelloProtocolGauge verifies the §1.2 by-protocol gauge is flipped on a
// HELLO upgrade: the connection moves out of the resp2 bucket and into resp3.
func TestHelloProtocolGauge(t *testing.T) {
	s, reader := metricServer(t)
	cs := newConnState(1, true) // pre-authed, starts at RESP2
	if reply := s.dispatch(cs, cmd("HELLO", "3")); reply.Kind == resp.KindError {
		t.Fatalf("HELLO 3: %v", reply.Str)
	}
	if cs.proto != resp.Proto3 {
		t.Fatalf("cs.proto = %d after HELLO 3, want %d", cs.proto, resp.Proto3)
	}
	rm := collect(t, reader)
	if got := sumInt64(rm, "toykv.clients.by_protocol", map[string]string{"proto": "resp3"}); got != 1 {
		t.Errorf("by_protocol{resp3} = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.clients.by_protocol", map[string]string{"proto": "resp2"}); got != -1 {
		t.Errorf("by_protocol{resp2} = %d, want -1", got)
	}
}

// TestAuthMetrics asserts §1.2 auth.attempts is recorded by result for both a
// wrong and a correct password.
func TestAuthMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		RequirePass: "s3cret",
		Telemetry:   telemetry.TestProviders(reader),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, r, w := dial(t, s.Addr())
	defer c.Close()
	writeCmd(t, w, "AUTH", "wrong")
	readReply(t, r) // WRONGPASS
	writeCmd(t, w, "AUTH", "s3cret")
	readReply(t, r) // OK

	rm := collect(t, reader)
	if got := sumInt64(rm, "toykv.auth.attempts", map[string]string{"result": "wrongpass"}); got != 1 {
		t.Errorf("auth.attempts{wrongpass} = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.auth.attempts", map[string]string{"result": "success"}); got != 1 {
		t.Errorf("auth.attempts{success} = %d, want 1", got)
	}
}
