package telemetry

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	otellogglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// logRecorder is an in-memory sdklog.Exporter that captures emitted records.
type logRecorder struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (r *logRecorder) Export(_ context.Context, recs []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, recs...)
	return nil
}
func (r *logRecorder) Shutdown(context.Context) error   { return nil }
func (r *logRecorder) ForceFlush(context.Context) error { return nil }

func (r *logRecorder) anyWithTraceID() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.records {
		if r.records[i].TraceID().IsValid() {
			return true
		}
	}
	return false
}

// TestLogRecordCarriesTraceID is M16 owned-risk test (3), signal-correctness
// slice: a log emitted inside a span must carry the active trace id via the
// OTLP bridge, so a Grafana trace links to its logs.
func TestLogRecordCarriesTraceID(t *testing.T) {
	rec := &logRecorder{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(rec)))
	otellogglobal.SetLoggerProvider(lp) // the otelslog bridge reads the global provider
	t.Cleanup(func() {
		_ = lp.Shutdown(context.Background())
		otellogglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
	})

	tp, _ := TestSpanProviders()
	log := slog.New(NewSlogHandler(slog.NewJSONHandler(io.Discard, nil), tp))

	ctx, span := tp.Tracer.Start(context.Background(), "op")
	log.InfoContext(ctx, "inside span")
	span.End()

	if err := lp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if !rec.anyWithTraceID() {
		t.Error("no OTLP log record carried a valid trace id")
	}
}
