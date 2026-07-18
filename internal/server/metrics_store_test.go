package server

import (
	"io"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// gaugeInt64 returns the value of an observable Int64 gauge data point
// matching filter (nil ⇒ first), or -1 if absent.
func gaugeInt64(rm metricdata.ResourceMetrics, name string, filter map[string]string) int64 {
	m, ok := findMetric(rm, name)
	if !ok {
		return -1
	}
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		return -1
	}
	for _, dp := range g.DataPoints {
		if attrsMatch(dp.Attributes, filter) {
			return dp.Value
		}
	}
	return -1
}

// TestStoreAOFMetrics covers §1.3 keyspace, §1.5 AOF append/fsync, and the
// §1.3/§1.7 observable gauges in one AOF-backed server.
func TestStoreAOFMetrics(t *testing.T) {
	dir := t.TempDir()
	reader := sdkmetric.NewManualReader()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.New(),
		Dir:         dir,
		FsyncPolicy: aof.FsyncAlways,
		Telemetry:   telemetry.TestProviders(reader),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()

	cs := &connState{authenticated: true}
	s.dispatch(cs, cmd("SET", "k", "v"))
	s.dispatch(cs, cmd("GET", "k"))       // hit
	s.dispatch(cs, cmd("GET", "missing")) // miss

	rm := collect(t, reader)

	if got := sumInt64(rm, "toykv.keyspace.hits", nil); got != 1 {
		t.Errorf("keyspace.hits = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.keyspace.misses", nil); got != 1 {
		t.Errorf("keyspace.misses = %d, want 1", got)
	}
	if got := sumInt64(rm, "toykv.aof.appends", nil); got < 1 {
		t.Errorf("aof.appends = %d, want >= 1", got)
	}
	if got := sumInt64(rm, "toykv.aof.fsyncs", map[string]string{"policy": "always"}); got < 1 {
		t.Errorf("aof.fsyncs{always} = %d, want >= 1", got)
	}
	if got := sumInt64(rm, "toykv.aof.append.bytes", nil); got <= 0 {
		t.Errorf("aof.append.bytes = %d, want > 0", got)
	}
	if got := gaugeInt64(rm, "toykv.keys", nil); got != 1 {
		t.Errorf("observable toykv.keys = %d, want 1", got)
	}
	if got := gaugeInt64(rm, "toykv.build.info", map[string]string{"version": serverVersion}); got != 1 {
		t.Errorf("observable build.info = %d, want 1", got)
	}
}

// TestKeysExpiredLazyMetric proves the §1.3 keys.expired{lazy} counter fires
// when a read evicts an expired key.
func TestKeysExpiredLazyMetric(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	reader := sdkmetric.NewManualReader()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store.NewWithClock(fc.now),
		NowFunc:     fc.now,
		SweeperOpts: store.SweeperOptions{Interval: time.Hour}, // sweeper out of the way
		Telemetry:   telemetry.TestProviders(reader),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cs := &connState{authenticated: true}
	s.dispatch(cs, cmd("SET", "k", "v", "PX", "10"))
	fc.advance(50 * time.Millisecond)
	s.dispatch(cs, cmd("GET", "k")) // expired → lazy eviction

	rm := collect(t, reader)
	if got := sumInt64(rm, "toykv.keys.expired", map[string]string{"path": "lazy"}); got != 1 {
		t.Errorf("keys.expired{lazy} = %d, want 1", got)
	}
}

// TestReplayMetrics proves the one-shot §1.6 replay counters are recorded
// when a second server replays an existing AOF.
func TestReplayMetrics(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First server writes a couple of records, then closes.
	s1, err := New(Config{Addr: "127.0.0.1:0", Log: log, Store: store.New(), Dir: dir, FsyncPolicy: aof.FsyncAlways})
	if err != nil {
		t.Fatalf("New s1: %v", err)
	}
	cs := &connState{authenticated: true}
	s1.dispatch(cs, cmd("SET", "a", "1"))
	s1.dispatch(cs, cmd("SET", "b", "2"))
	_ = s1.Close()

	// Second server replays that AOF under a telemetry reader.
	reader := sdkmetric.NewManualReader()
	s2, err := New(Config{Addr: "127.0.0.1:0", Log: log, Store: store.New(), Dir: dir, FsyncPolicy: aof.FsyncAlways, Telemetry: telemetry.TestProviders(reader)})
	if err != nil {
		t.Fatalf("New s2: %v", err)
	}
	defer func() { _ = s2.Close() }()

	rm := collect(t, reader)
	if got := sumInt64(rm, "toykv.aof.replay.records", nil); got < 2 {
		t.Errorf("aof.replay.records = %d, want >= 2", got)
	}
	if got := sumInt64(rm, "toykv.aof.replay.bytes", nil); got <= 0 {
		t.Errorf("aof.replay.bytes = %d, want > 0", got)
	}
}
