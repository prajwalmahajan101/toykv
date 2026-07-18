package telemetry

import (
	"context"
	"log/slog"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

// TestSpanProviders builds a Providers with a real, always-sampling tracer
// backed by an in-memory SpanRecorder (metrics are no-ops), for tests that
// assert emitted spans. The recorder exposes Ended() for inspection.
func TestSpanProviders() (*Providers, *tracetest.SpanRecorder) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	return &Providers{
		Enabled:     true,
		Tracer:      tp.Tracer(scopeName),
		Meter:       metricnoop.NewMeterProvider().Meter(scopeName),
		Metrics:     NoopMetrics(),
		CaptureKeys: true, // exercise the key-hash path in span tests
		log:         slog.Default(),
		shutdownFns: []func(context.Context) error{tp.Shutdown},
	}, sr
}
