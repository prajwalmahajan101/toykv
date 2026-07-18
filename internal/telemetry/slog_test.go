package telemetry

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestSlogHandler_TeesToBase: when telemetry is enabled the wrapped handler
// still delivers every record to the base (console) handler unchanged.
func TestSlogHandler_TeesToBase(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	tp, _ := TestSpanProviders() // Enabled == true
	log := slog.New(NewSlogHandler(base, tp))

	log.Info("hello", "k", "v")

	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("base handler did not receive the record; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"k":"v"`) {
		t.Errorf("base handler lost attributes; got %q", buf.String())
	}
}

// TestSlogHandler_DisabledIsIdentity: with telemetry off the base handler is
// returned unchanged, so console log shape and cost are untouched.
func TestSlogHandler_DisabledIsIdentity(t *testing.T) {
	base := slog.NewJSONHandler(io.Discard, nil)
	if got := NewSlogHandler(base, Disabled()); got != base {
		t.Error("disabled NewSlogHandler must return the base handler unchanged")
	}
	if got := NewSlogHandler(base, nil); got != base {
		t.Error("nil providers must return the base handler unchanged")
	}
}

// TestSlogHandler_WithAttrsPropagates ensures WithAttrs/WithGroup fan out so a
// logger derived via slog.With still tees correctly.
func TestSlogHandler_WithAttrsPropagates(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	tp, _ := TestSpanProviders()
	log := slog.New(NewSlogHandler(base, tp)).With("component", "aof")

	log.Warn("slow fsync")

	out := buf.String()
	if !strings.Contains(out, "slow fsync") || !strings.Contains(out, `"component":"aof"`) {
		t.Errorf("derived logger lost message or attr; got %q", out)
	}
}
