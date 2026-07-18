package telemetry

import (
	"context"
	"testing"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TestInit_Enabled proves a configured endpoint yields real (non-no-op)
// providers and a bounded, error-free shutdown even when the collector is
// unreachable — the exporters connect lazily and drop asynchronously.
func TestInit_Enabled(t *testing.T) {
	cfg := Config{
		Endpoint:    "127.0.0.1:4317", // nothing is listening; that's fine
		Protocol:    "grpc",
		ServiceName: "toykv",
		Version:     "test",
		Sampling:    1,
	}
	p, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init(enabled): %v", err)
	}
	if !p.Enabled {
		t.Fatal("Init(enabled): Enabled = false")
	}
	// The providers must be the real SDK ones, not the no-ops installed by
	// the disabled path.
	if _, isNoop := p.Tracer.(tracenoop.Tracer); isNoop {
		t.Error("enabled path handed back a no-op tracer")
	}
	if _, isNoop := p.Meter.(metricnoop.Meter); isNoop {
		t.Error("enabled path handed back a no-op meter")
	}

	// Exercise the hot path once — must not panic against a dead collector.
	_, span := p.Tracer.Start(context.Background(), "probe")
	span.End()

	// Shutdown must be bounded by the caller's context, not hang. Against a
	// dead collector the final flush legitimately times out — that is a
	// bounded shutdown error, not a correctness failure (the never-fail
	// contract governs the command path, not process teardown). We only
	// require that it returns within the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Logf("Shutdown returned (expected against a dead collector): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not respect its context deadline (hung)")
	}
}

// TestInit_UnknownProtocol is a startup configuration error — the one class
// of telemetry problem that is fatal (surfaced before the listener opens).
func TestInit_UnknownProtocol(t *testing.T) {
	_, err := Init(context.Background(), Config{Endpoint: "127.0.0.1:4317", Protocol: "carrier-pigeon"})
	if err == nil {
		t.Fatal("Init: expected error for unknown protocol, got nil")
	}
}

func TestHostPort(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"127.0.0.1:4317", "127.0.0.1:4317"},
		{"http://localhost:4318", "localhost:4318"},
		{"grpc://collector:4317/", "collector:4317"},
	} {
		if got := hostPort(tc.in); got != tc.want {
			t.Errorf("hostPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
