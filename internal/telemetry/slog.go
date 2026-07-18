package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

// NewSlogHandler wraps base so that, when telemetry is enabled, every log
// record is ALSO emitted as an OTLP log record via the global LoggerProvider
// — carrying service.name/version and, when the record is logged with a
// span-bearing context (…Context methods), the active trace_id/span_id for
// one-click trace↔log correlation. When telemetry is disabled it returns
// base unchanged, so console output keeps its exact shape and cost.
func NewSlogHandler(base slog.Handler, p *Providers) slog.Handler {
	if p == nil || !p.Enabled {
		return base
	}
	return fanout{handlers: []slog.Handler{base, otelslog.NewHandler(scopeName)}}
}

// fanout dispatches each record to several slog handlers. The console handler
// keeps its native format; the OTLP handler ships records to Loki.
type fanout struct {
	handlers []slog.Handler
}

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			// Clone so one handler's attribute mutations can't affect another.
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return fanout{handlers: next}
}

func (f fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanout{handlers: next}
}
