package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// metricServer builds a server whose telemetry records into a manual reader.
func metricServer(t *testing.T) (*Server, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	s, err := New(Config{
		Addr:      "127.0.0.1:0",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:     store.New(),
		Telemetry: telemetry.TestProviders(reader),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, reader
}

// TestObserveCommand_RED asserts the §1.1 RED instruments record the right
// counts, statuses, and error kinds at the dispatch chokepoint.
func TestObserveCommand_RED(t *testing.T) {
	s, reader := metricServer(t)
	cs := &connState{authenticated: true}

	// SET k v → ok; GET k → ok; LPUSH k x → WRONGTYPE; BOGUS → unknown.
	s.observeCommand(cs, cmd("SET", "k", "v"))
	s.observeCommand(cs, cmd("GET", "k"))
	s.observeCommand(cs, cmd("LPUSH", "k", "x"))
	s.observeCommand(cs, cmd("BOGUS", "x"))

	rm := collect(t, reader)

	if got := sumInt64(rm, "toykv.commands", nil); got != 4 {
		t.Errorf("toykv.commands total = %d, want 4", got)
	}
	if got := sumInt64(rm, "toykv.commands", map[string]string{"command": "SET", "status": "ok"}); got != 1 {
		t.Errorf("commands{SET,ok} = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.commands", map[string]string{"command": "LPUSH", "status": "error"}); got != 1 {
		t.Errorf("commands{LPUSH,error} = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.command.errors", map[string]string{"command": "LPUSH", "kind": "wrongtype"}); got != 1 {
		t.Errorf("command.errors{LPUSH,wrongtype} = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.command.errors", map[string]string{"command": "UNKNOWN", "kind": "unknown"}); got != 1 {
		t.Errorf("command.errors{UNKNOWN,unknown} = %d, want 1", got)
	}
	// In-flight must net to zero after all commands complete.
	if got := sumInt64(rm, "toykv.commands.inflight", nil); got != 0 {
		t.Errorf("commands.inflight net = %d, want 0", got)
	}
	// Duration histogram must have recorded one sample per command.
	if got := histCount(rm, "toykv.command.duration"); got != 4 {
		t.Errorf("command.duration count = %d, want 4", got)
	}
}

// TestErrorKind covers the reply-prefix→kind mapping directly.
func TestErrorKind(t *testing.T) {
	cases := map[string]string{
		"WRONGTYPE Operation against a key holding the wrong kind of value": "wrongtype",
		"NOAUTH Authentication required.":                                   "noauth",
		"NOPROTO sorry, this protocol version is not supported.":            "noproto",
		"ERR wrong number of arguments for 'get'":                           "arity",
		"ERR unknown command 'bogus'":                                       "unknown",
		"ERR value is not an integer or out of range":                       "notinteger",
		"ERR increment or decrement would overflow":                         "overflow",
		"ERR bad pattern":             "badpattern",
		"ERR invalid cursor":          "invalid_cursor",
		"ERR syntax error":            "syntax",
		"ERR Protocol error":          "protocol",
		"ERR no such key":             "nosuchkey",
		"ERR persistence is disabled": "other",
	}
	for msg, want := range cases {
		if got := errorKind(msg); got != want {
			t.Errorf("errorKind(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestCommandLabel(t *testing.T) {
	if got := commandLabel(cmd("get", "k")); got != "GET" {
		t.Errorf("commandLabel(get) = %q, want GET", got)
	}
	if got := commandLabel(cmd("bogus")); got != "UNKNOWN" {
		t.Errorf("commandLabel(bogus) = %q, want UNKNOWN", got)
	}
	if got := commandLabel(nil); got != "UNKNOWN" {
		t.Errorf("commandLabel(nil) = %q, want UNKNOWN", got)
	}
}

// --- metric assertion helpers ---

func cmd(parts ...string) [][]byte {
	argv := make([][]byte, len(parts))
	for i, p := range parts {
		argv[i] = []byte(p)
	}
	return argv
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// sumInt64 totals an Int64 Sum instrument's data points matching filter
// (nil ⇒ all). Returns -1 if the metric is absent or not an Int64 sum.
func sumInt64(rm metricdata.ResourceMetrics, name string, filter map[string]string) int64 {
	m, ok := findMetric(rm, name)
	if !ok {
		return -1
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return -1
	}
	var total int64
	for _, dp := range sum.DataPoints {
		if attrsMatch(dp.Attributes, filter) {
			total += dp.Value
		}
	}
	return total
}

func histCount(rm metricdata.ResourceMetrics, name string) uint64 {
	m, ok := findMetric(rm, name)
	if !ok {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	var total uint64
	for _, dp := range h.DataPoints {
		total += dp.Count
	}
	return total
}

func attrsMatch(set attribute.Set, filter map[string]string) bool {
	for k, v := range filter {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}
