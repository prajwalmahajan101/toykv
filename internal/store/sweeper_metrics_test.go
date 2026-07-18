package store

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// TestSweeperMetrics covers §1.4: one sweeper tick over an expired key must
// record a pass, the sample/evict counts, and keys.expired{path=sweeper}.
func TestSweeperMetrics(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	reader := sdkmetric.NewManualReader()
	s := NewWithClock(func() time.Time { return base })
	s.SetMetrics(telemetry.TestProviders(reader).Metrics)

	// A key already past its deadline at `base`.
	s.Set("k", []byte("v"), SetOpts{ExpireAt: base.Add(-time.Second)})

	sw := NewSweeper(s, SweeperOptions{Batch: 20})
	sw.tick(base)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := sweepSum(rm, "toykv.sweeper.passes", nil); got != 1 {
		t.Errorf("sweeper.passes = %d, want 1", got)
	}
	if got := sweepSum(rm, "toykv.sweeper.evicted", nil); got < 1 {
		t.Errorf("sweeper.evicted = %d, want >= 1", got)
	}
	if got := sweepSum(rm, "toykv.keys.expired", map[string]string{"path": "sweeper"}); got < 1 {
		t.Errorf("keys.expired{sweeper} = %d, want >= 1", got)
	}
}

func sweepSum(rm metricdata.ResourceMetrics, name string, filter map[string]string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				return -1
			}
			var total int64
			for _, dp := range sum.DataPoints {
				match := true
				for k, v := range filter {
					got, ok := dp.Attributes.Value(attribute.Key(k))
					if !ok || got.AsString() != v {
						match = false
						break
					}
				}
				if match {
					total += dp.Value
				}
			}
			return total
		}
	}
	return -1
}
