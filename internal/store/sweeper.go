package store

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Sweeper periodically samples the store for expired keys and evicts
// them. Modelled on Redis's "expire random sample" — sample N keys, if
// more than threshold% were expired, loop again in the same tick to
// keep up with bursts of expirations.
//
// Lazy expiry handles the read path; the sweeper exists for keys that
// expire and are never read again — without it they would accumulate
// in the map indefinitely.
type Sweeper struct {
	store     *Store
	interval  time.Duration // tick period (default 1s).
	batch     int           // keys sampled per pass (default 20).
	threshold float64       // expired-fraction above which a tick loops.
	maxLoops  int           // hard cap on per-tick passes; bounds lock-hold time.
	tracer    trace.Tracer  // sweeper.tick span; nil ⇒ no span (M16 §3).
}

// SetTracer installs the tracer for the sweeper.tick span. Called by the
// server after construction; nil (the default) means no span is emitted.
func (sw *Sweeper) SetTracer(t trace.Tracer) { sw.tracer = t }

// SweeperOptions configures a Sweeper. Zero values fall back to the
// LLD §3.3 defaults: 1s tick, 20-key sample, 0.25 threshold, 16 max
// loops per tick.
type SweeperOptions struct {
	Interval  time.Duration
	Batch     int
	Threshold float64
	MaxLoops  int
}

// NewSweeper returns a Sweeper bound to s. Call Run to start it.
func NewSweeper(s *Store, opts SweeperOptions) *Sweeper {
	sw := &Sweeper{
		store:     s,
		interval:  opts.Interval,
		batch:     opts.Batch,
		threshold: opts.Threshold,
		maxLoops:  opts.MaxLoops,
	}
	if sw.interval <= 0 {
		sw.interval = time.Second
	}
	if sw.batch <= 0 {
		sw.batch = 20
	}
	if sw.threshold <= 0 {
		sw.threshold = 0.25
	}
	if sw.maxLoops <= 0 {
		sw.maxLoops = 16
	}
	return sw
}

// Run drives the sweeper until ctx is cancelled. It blocks; callers
// typically launch it in a goroutine. Safe to call once per Sweeper.
func (sw *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(sw.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sw.tick(sw.store.nowFunc())
		}
	}
}

// tick performs one scheduled sweep: sample → evict → repeat while the
// expired fraction stays above threshold. The maxLoops cap bounds the
// total mutex hold time per scheduled tick.
func (sw *Sweeper) tick(now time.Time) (totalSampled, totalEvicted int) {
	start := time.Now()
	var span trace.Span
	if sw.tracer != nil {
		_, span = sw.tracer.Start(context.Background(), "sweeper.tick")
	}
	// Record §1.4 sweeper metrics once per tick, regardless of which return
	// fires. keys.expired{sweeper} shares the counter with the lazy path.
	defer func() {
		ctx := context.Background()
		m := sw.store.metrics
		m.SweeperPasses.Add(ctx, 1)
		m.SweeperSampled.Add(ctx, int64(totalSampled))
		m.SweeperEvicted.Add(ctx, int64(totalEvicted))
		m.SweeperDuration.Record(ctx, time.Since(start).Seconds())
		if totalEvicted > 0 {
			m.KeysExpired.Add(ctx, int64(totalEvicted), sweeperExpiryAttr)
		}
		if span != nil {
			span.SetAttributes(
				attribute.Int("sampled", totalSampled),
				attribute.Int("evicted", totalEvicted),
			)
			span.End()
		}
	}()
	for i := 0; i < sw.maxLoops; i++ {
		sampled, evicted := sw.store.sweepOnce(now, sw.batch)
		totalSampled += sampled
		totalEvicted += evicted
		if sampled == 0 {
			return
		}
		if float64(evicted)/float64(sampled) <= sw.threshold {
			return
		}
	}
	return
}
