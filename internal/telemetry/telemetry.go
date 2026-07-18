package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// scopeName is the instrumentation-scope name stamped on every span and
// instrument toykv emits. It doubles as the module path so a backend can
// attribute signals to this codebase.
const scopeName = "github.com/prajwalmahajan101/toykv"

// Config controls telemetry setup. The zero value (empty Endpoint) yields
// a fully disabled, no-op pipeline.
type Config struct {
	// Endpoint is the OTLP collector address (host:port). Empty disables
	// telemetry entirely: the providers become SDK no-ops.
	Endpoint string
	// Protocol selects the OTLP transport: "grpc" (default) or "http".
	Protocol string
	// ServiceName / Version populate the OTel resource (service.name /
	// service.version), used by every backend to group signals.
	ServiceName string
	Version     string
	// Sampling is the parent-based trace sampling ratio in [0,1]. Errors
	// and rewrite/replay spans are always sampled regardless (see traces).
	Sampling float64
	// CaptureKeys opts into salted-hash key capture on store spans. Off by
	// default; even on, the plaintext key never appears in any signal.
	CaptureKeys bool
	// Log receives telemetry-internal diagnostics (exporter failures, etc).
	// nil ⇒ slog.Default().
	Log *slog.Logger
}

// Providers is the initialized telemetry surface handed to the server. When
// telemetry is disabled the providers are no-ops and Enabled is false, but
// Tracer and Meter are always non-nil so the hot path never needs a nil
// check — a no-op span/instrument is the disabled cost.
type Providers struct {
	// Enabled reports whether an OTLP endpoint was configured. Callers use
	// it for coarse decisions (e.g. installing the slog bridge), never as a
	// per-signal guard.
	Enabled bool
	// Tracer / Meter are scoped to scopeName; real when enabled, no-op when
	// not.
	Tracer trace.Tracer
	Meter  metric.Meter
	// Metrics holds the pre-created instrument handles. Never nil.
	Metrics *Metrics
	// CaptureKeys mirrors Config.CaptureKeys for the store-span key helper.
	CaptureKeys bool

	log         *slog.Logger
	shutdownFns []func(context.Context) error
}

// Init builds the telemetry pipeline from cfg and installs the resulting
// providers as the OTel globals. It never returns a live-traffic-fatal
// error for exporter problems — only a genuinely malformed configuration
// fails here (surfaced before the listener opens). Call Providers.Shutdown
// on exit to flush and close exporters.
func Init(ctx context.Context, cfg Config) (*Providers, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}

	if cfg.Endpoint == "" {
		return newDisabled(cfg), nil
	}
	return initEnabled(ctx, cfg)
}

// newDisabled installs explicit no-op providers as the globals and returns
// a disabled Providers. Setting the no-ops is belt-and-suspenders — the
// OTel globals default to no-op — but it makes the disabled state explicit
// and immune to any other package having set a global first.
func newDisabled(cfg Config) *Providers {
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
	global.SetLoggerProvider(lognoop.NewLoggerProvider())
	meter := otel.Meter(scopeName)
	mx, _ := newMetrics(meter) // no-op meter never errors
	return &Providers{
		Enabled:     false,
		Tracer:      otel.Tracer(scopeName),
		Meter:       meter,
		Metrics:     mx,
		CaptureKeys: cfg.CaptureKeys,
		log:         cfg.Log,
	}
}

// Disabled returns a no-op Providers for callers that never run Init
// (tests, embedders). Unlike the disabled path of Init it does NOT touch
// the OTel globals, so it is safe to construct many times concurrently.
// Tracer/Meter are non-nil no-ops so the hot path never nil-checks.
func Disabled() *Providers {
	meter := metricnoop.NewMeterProvider().Meter(scopeName)
	mx, _ := newMetrics(meter) // no-op meter never errors
	return &Providers{
		Tracer:  tracenoop.NewTracerProvider().Tracer(scopeName),
		Meter:   meter,
		Metrics: mx,
		log:     slog.Default(),
	}
}

// Shutdown flushes and closes every registered provider/exporter, bounded
// by ctx. Errors are joined; a shutdown error never affects correctness of
// already-served commands. Safe to call on a disabled Providers (no-op).
func (p *Providers) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, fn := range p.shutdownFns {
		if err := fn(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// newResource builds the OTel resource shared by all three signals. It is
// used by the enabled path (T2); the disabled path needs no resource.
func newResource(cfg Config) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.Version),
		),
	)
}
