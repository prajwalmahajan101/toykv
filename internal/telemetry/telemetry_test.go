package telemetry

import (
	"context"
	"testing"
)

// TestInit_Disabled is the no-op-when-off contract: an empty endpoint must
// yield non-nil, usable Tracer/Meter (so the hot path never nil-checks) and
// a shutdown that succeeds — with Enabled reported false.
func TestInit_Disabled(t *testing.T) {
	p, err := Init(context.Background(), Config{ServiceName: "toykv", Version: "test"})
	if err != nil {
		t.Fatalf("Init(disabled): unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("Init(disabled): nil Providers")
	}
	if p.Enabled {
		t.Error("Init(disabled): Enabled = true, want false")
	}
	if p.Tracer == nil {
		t.Error("Init(disabled): Tracer is nil; callers must never nil-check")
	}
	if p.Meter == nil {
		t.Error("Init(disabled): Meter is nil; callers must never nil-check")
	}

	// A no-op tracer must still hand back a usable (non-recording) span so
	// span-wrapping code runs unconditionally.
	_, span := p.Tracer.Start(context.Background(), "probe")
	if span.SpanContext().IsSampled() {
		t.Error("disabled tracer produced a sampled span")
	}
	span.End()

	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown(disabled): unexpected error: %v", err)
	}
}

// TestInit_CaptureKeysPropagates guards that the privacy flag reaches the
// Providers so the store-span key helper can honor it.
func TestInit_CaptureKeysPropagates(t *testing.T) {
	p, err := Init(context.Background(), Config{CaptureKeys: true})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !p.CaptureKeys {
		t.Error("CaptureKeys did not propagate to Providers")
	}
}
