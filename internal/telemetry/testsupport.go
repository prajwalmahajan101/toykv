package telemetry

import (
	"context"
	"log/slog"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TestProviders builds a Providers whose metric instruments record into
// reader (a manual/periodic SDK reader), with a no-op tracer, for tests in
// embedding packages that assert emitted metrics. It does NOT install any
// OTel globals, so it is safe to use in parallel tests. Shutdown flushes the
// backing meter provider.
func TestProviders(reader sdkmetric.Reader) *Providers {
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter(scopeName)
	mx, _ := newMetrics(meter)
	return &Providers{
		Enabled:     true,
		Tracer:      tracenoop.NewTracerProvider().Tracer(scopeName),
		Meter:       meter,
		Metrics:     mx,
		log:         slog.Default(),
		shutdownFns: []func(context.Context) error{mp.Shutdown},
	}
}
