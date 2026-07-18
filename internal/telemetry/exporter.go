package telemetry

import (
	"context"
	"fmt"
	"strings"

	contribruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// initEnabled builds the real OTLP-backed tracer/meter/logger providers,
// installs them as the OTel globals, and starts process-runtime metric
// collection. Only a malformed configuration (unknown protocol, exporter
// construction failure) errors here — a merely unreachable endpoint does
// not, because the OTLP exporters connect lazily and report send failures
// asynchronously to the global error handler (which logs + drops them).
func initEnabled(ctx context.Context, cfg Config) (*Providers, error) {
	// Route asynchronous export failures to the log as a dropped batch —
	// never a client-visible error. This is the exporter-down-resilience
	// hook (M16 owned-risk test): a dead collector logs and drops.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		cfg.Log.Warn("otel export failed (dropped)", "err", err)
	}))

	res, err := newResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	p := &Providers{Enabled: true, CaptureKeys: cfg.CaptureKeys, log: cfg.Log}

	// Traces.
	traceExp, err := newTraceExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(newSampler(cfg.Sampling)),
		sdktrace.WithBatcher(traceExp),
	)
	otel.SetTracerProvider(tp)
	p.shutdownFns = append(p.shutdownFns, tp.Shutdown)

	// Metrics.
	metricExp, err := newMetricExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)
	otel.SetMeterProvider(mp)
	p.shutdownFns = append(p.shutdownFns, mp.Shutdown)

	// Logs.
	logExp, err := newLogExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	global.SetLoggerProvider(lp)
	p.shutdownFns = append(p.shutdownFns, lp.Shutdown)

	// Process/runtime metrics (process.runtime.go.*).
	if err := contribruntime.Start(contribruntime.WithMeterProvider(mp)); err != nil {
		return nil, fmt.Errorf("telemetry: runtime instrumentation: %w", err)
	}

	p.Tracer = otel.Tracer(scopeName)
	p.Meter = otel.Meter(scopeName)
	return p, nil
}

// newSampler returns the parent-based ratio sampler for command/store/aof
// spans. A parent's decision always wins (so a whole command tree samples
// or drops together); rooted spans sample at ratio. Errors and
// rewrite/replay spans are forced on at span-creation time by the tracing
// code, independent of this base ratio. Ratio is clamped to [0,1]; a
// non-positive ratio means "root spans never self-sample" (children still
// follow a sampled parent).
func newSampler(ratio float64) sdktrace.Sampler {
	switch {
	case ratio >= 1:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case ratio <= 0:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}

func newTraceExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch normalizeProtocol(cfg.Protocol) {
	case "http":
		return otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(hostPort(cfg.Endpoint)),
			otlptracehttp.WithInsecure())
	case "grpc":
		return otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(hostPort(cfg.Endpoint)),
			otlptracegrpc.WithInsecure())
	default:
		return nil, fmt.Errorf("unknown -otel-protocol %q (want grpc|http)", cfg.Protocol)
	}
}

func newMetricExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	switch normalizeProtocol(cfg.Protocol) {
	case "http":
		return otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpoint(hostPort(cfg.Endpoint)),
			otlpmetrichttp.WithInsecure())
	case "grpc":
		return otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(hostPort(cfg.Endpoint)),
			otlpmetricgrpc.WithInsecure())
	default:
		return nil, fmt.Errorf("unknown -otel-protocol %q (want grpc|http)", cfg.Protocol)
	}
}

func newLogExporter(ctx context.Context, cfg Config) (sdklog.Exporter, error) {
	switch normalizeProtocol(cfg.Protocol) {
	case "http":
		return otlploghttp.New(ctx,
			otlploghttp.WithEndpoint(hostPort(cfg.Endpoint)),
			otlploghttp.WithInsecure())
	case "grpc":
		return otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(hostPort(cfg.Endpoint)),
			otlploggrpc.WithInsecure())
	default:
		return nil, fmt.Errorf("unknown -otel-protocol %q (want grpc|http)", cfg.Protocol)
	}
}

// normalizeProtocol defaults an empty protocol to grpc and lower-cases it.
func normalizeProtocol(p string) string {
	if p == "" {
		return "grpc"
	}
	return strings.ToLower(p)
}

// hostPort strips any scheme from an endpoint so the OTLP WithEndpoint
// option (which wants a bare host:port) accepts a URL-ish value too.
func hostPort(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i >= 0 {
		endpoint = endpoint[i+3:]
	}
	return strings.TrimSuffix(endpoint, "/")
}
