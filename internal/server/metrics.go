package server

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/aof"
)

// recordReplayStats emits the one-shot §1.6 AOF replay metrics from the
// stats captured during startup. Called once, before the listener opens.
func (s *Server) recordReplayStats(stats aof.ReplayStats) {
	ctx := context.Background()
	m := s.tel.Metrics
	m.AOFReplayRecords.Add(ctx, int64(stats.Records))
	m.AOFReplayBytes.Add(ctx, stats.Bytes)
	m.AOFReplayDuration.Record(ctx, stats.Duration.Seconds())
}

// registerObservableGauges wires the §1.3/§1.5/§1.7 observable gauges whose
// values are read on demand at collection time from live server state. On a
// no-op meter these registrations are themselves no-ops. Callbacks take the
// appropriate locks and never block on I/O beyond a stat().
func (s *Server) registerObservableGauges() error {
	meter := s.tel.Meter
	var errs []error
	reg := func(name, desc, unit string, cb metric.Int64Callback) {
		if _, err := meter.Int64ObservableGauge(name,
			metric.WithDescription(desc),
			metric.WithUnit(unit),
			metric.WithInt64Callback(cb),
		); err != nil {
			errs = append(errs, err)
		}
	}

	reg("toykv.keys", "Number of keys currently in the keyspace.", "",
		func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(s.store.DBSize()))
			return nil
		})

	reg("toykv.aof.size", "Canonical AOF size in bytes (incl. buffered).", "By",
		func(_ context.Context, o metric.Int64Observer) error {
			// Grab the writer under s.mu (Close may nil it), then call Size()
			// outside that lock — Size takes the writer's own mutex.
			s.mu.Lock()
			w := s.aof
			s.mu.Unlock()
			if w != nil {
				if sz, err := w.Size(); err == nil {
					o.Observe(sz)
				}
			}
			return nil
		})

	reg("toykv.aof.rewrite.in_progress", "1 while a BGREWRITEAOF runs, else 0.", "",
		func(_ context.Context, o metric.Int64Observer) error {
			s.rewriteMu.Lock()
			inflight := s.rewriteInFlight
			s.rewriteMu.Unlock()
			o.Observe(boolToInt64(inflight))
			return nil
		})

	reg("toykv.uptime", "Server uptime in seconds.", "s",
		func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(s.now().Sub(s.startTime).Seconds()))
			return nil
		})

	reg("toykv.build.info", "Build info; value is always 1, version in the attribute.", "",
		func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, metric.WithAttributes(attribute.String("version", serverVersion)))
			return nil
		})

	// Cluster observability (M21, ADR-0021): leader role and per-replica lag
	// gauges. Registered only when replication is active; on no-op meter these
	// are themselves no-ops. Callbacks take a single Status() snapshot — fast,
	// non-blocking, copy-under-lock inside ToyRaft.
	if s.replicated {
		reg("toykv.raft.is_leader", "1 when this node is the Raft leader, else 0.", "",
			func(_ context.Context, o metric.Int64Observer) error {
				st := s.cluster.Status()
				o.Observe(boolToInt64(st.Role == raft.Leader))
				return nil
			})

		reg("toykv.raft.replication_lag", "Entries behind CommitIndex for each replica peer.", "",
			func(_ context.Context, o metric.Int64Observer) error {
				st := s.cluster.Status()
				// MatchIndex is non-nil only on the leader; skip on followers.
				for peerID, matchIdx := range st.MatchIndex {
					lag := max(int64(st.CommitIndex)-int64(matchIdx), 0)
					o.Observe(lag, metric.WithAttributes(attribute.String("peer", string(peerID))))
				}
				return nil
			})
	}

	return errors.Join(errs...)
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
