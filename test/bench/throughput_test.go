//go:build bench

// Package bench holds throughput measurements that are deliberately excluded
// from the normal `go test ./...` run (build tag `bench`). Run it with:
//
//	make bench-throughput
//	go test -tags bench -run TestThroughputTable ./test/bench -v [-n=20000 -c=50 -valsize=64]
//
// TestThroughputTable is a test, not a Benchmark, so it can drive a custom
// concurrent load and print a rendered box table via t.Log. It measures
// toykv's own in-process SET throughput in two modes: a standalone server
// (no AOF, so it isolates dispatch cost) and a replicated 3-node cluster
// (so every SET pays a real Raft Propose->commit->Apply round trip).
package bench

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/cluster"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

var (
	nOps        = flag.Int("n", 20000, "total SET operations per mode")
	concurrency = flag.Int("c", 50, "concurrent connections (workers) per mode")
	valSize     = flag.Int("valsize", 64, "SET value size in bytes")
	redisAddr   = flag.String("redis-addr", "", "if set, add a 'redis' row driven against this RESP2 endpoint (host:port) with the same load model")
)

// Cluster timings. Heartbeat is ToyRaft's default (50ms) — its driver tick
// period equals HeartbeatInterval, so a wider heartbeat directly slows commit
// (the routing harness's 300ms inflated replicated latency for no benefit in a
// throughput bench). Election window stays wide enough (heartbeat*3 <= min) that
// the leader is stable for the whole load run.
const (
	clusterElectionMin = 500 * time.Millisecond
	clusterElectionMax = 1 * time.Second
	clusterHeartbeat   = 50 * time.Millisecond
)

// doer is the shared client surface: a plain Client (standalone) and a
// ClusterClient (replicated, redirect-following) both satisfy it.
type doer interface {
	Do(argv ...string) (resp.Value, error)
	Close() error
}

// stats is one mode's measured result.
type stats struct {
	mode       string
	ops        int
	throughput float64 // ops/sec over wall-clock
	p50        time.Duration
	p95        time.Duration
	p99        time.Duration
}

func TestThroughputTable(t *testing.T) {
	value := string(make([]byte, *valSize))

	rows := []stats{
		runStandalone(t, *nOps, *concurrency, value),
		runReplicated(t, *nOps, *concurrency, value),
	}
	if *redisAddr != "" {
		// Same load model, external RESP2 target (real redis/valkey): a
		// baseline for the standalone row. Dial once to fail fast if it's down.
		if c, err := client.DialTimeout(*redisAddr, 2*time.Second); err != nil {
			t.Fatalf("redis dial %s: %v", *redisAddr, err)
		} else {
			_ = c.Close()
		}
		addr := *redisAddr
		rows = append(rows, runLoad(t, "redis", *nOps, *concurrency, value, func() (doer, error) {
			return client.Dial(addr)
		}))
	}

	t.Log("\n" + renderTable(rows))
}

// runStandalone starts a single server with AOF disabled and drives the load
// through a plain (non-redirecting) client per worker.
func runStandalone(t *testing.T, n, c int, value string) stats {
	t.Helper()
	s, err := server.New(server.Config{
		Addr:  "127.0.0.1:0",
		Store: store.New(),
		Log:   discardLogger(),
		Dir:   "", // no AOF: isolate dispatch + store cost
	})
	if err != nil {
		t.Fatalf("standalone New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	t.Cleanup(func() { _ = s.Close() })
	addr := waitAddr(t, s)

	return runLoad(t, "standalone", n, c, value, func() (doer, error) {
		return client.Dial(addr)
	})
}

// runReplicated starts a 3-node cluster over the real HTTP peer transport,
// waits for a leader, then drives the load through a ClusterClient per worker
// so NOTLEADER redirects are followed transparently.
func runReplicated(t *testing.T, n, c int, value string) stats {
	t.Helper()
	const nodes = 3
	names := []string{"n1", "n2", "n3"}
	caddrs := make([]string, nodes)
	raddrs := make([]string, nodes)
	peers := make([]cluster.Peer, nodes)
	for i := range nodes {
		caddrs[i] = freeAddr(t)
		raddrs[i] = freeAddr(t)
		peers[i] = cluster.Peer{ID: raft.NodeID(names[i]), Addr: raddrs[i], ClientAddr: caddrs[i]}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := range nodes {
		s, err := server.New(server.Config{
			Addr:               caddrs[i],
			Store:              store.New(),
			Log:                discardLogger(),
			Replicate:          true,
			NodeID:             names[i],
			Peers:              peers,
			RaftAddr:           raddrs[i],
			RaftDir:            t.TempDir(),
			ElectionTimeoutMin: clusterElectionMin,
			ElectionTimeoutMax: clusterElectionMax,
			HeartbeatInterval:  clusterHeartbeat,
		})
		if err != nil {
			t.Fatalf("replicated New(%s): %v", names[i], err)
		}
		go func() { _ = s.Run(ctx) }()
		t.Cleanup(func() { _ = s.Close() })
	}
	waitLeader(t, caddrs)

	return runLoad(t, "replicated 3-node", n, c, value, func() (doer, error) {
		return client.DialCluster(caddrs[0])
	})
}

// runLoad spreads n SETs across c workers (each its own conn), records every
// op latency, and reduces to throughput + percentiles.
func runLoad(t *testing.T, mode string, n, c int, value string, dial func() (doer, error)) stats {
	t.Helper()
	if c > n {
		c = n
	}
	lat := make([]time.Duration, n)
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	start := time.Now()
	for w := 0; w < c; w++ {
		lo, hi := partition(n, c, w)
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			d, err := dial()
			if err != nil {
				setErr(fmt.Errorf("worker %d dial: %w", w, err))
				return
			}
			defer func() { _ = d.Close() }()
			for i := lo; i < hi; i++ {
				key := fmt.Sprintf("k:%d:%d", w, i)
				t0 := time.Now()
				v, err := d.Do("SET", key, value)
				lat[i] = time.Since(t0)
				if err != nil {
					setErr(fmt.Errorf("worker %d SET: %w", w, err))
					return
				}
				if v.Kind == resp.KindError {
					setErr(fmt.Errorf("worker %d SET reply: %s", w, v.Str))
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if firstErr != nil {
		t.Fatalf("%s load: %v", mode, firstErr)
	}

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	return stats{
		mode:       mode,
		ops:        n,
		throughput: float64(n) / elapsed.Seconds(),
		p50:        percentile(lat, 0.50),
		p95:        percentile(lat, 0.95),
		p99:        percentile(lat, 0.99),
	}
}

// partition splits [0,n) into c near-equal contiguous ranges; returns the
// [lo,hi) owned by worker w.
func partition(n, c, w int) (int, int) {
	base := n / c
	rem := n % c
	lo := w*base + min(w, rem)
	size := base
	if w < rem {
		size++
	}
	return lo, lo + size
}

// percentile returns the p-quantile (0..1) of an already-sorted slice using
// nearest-rank; sorted must be non-empty.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ---- server bring-up helpers (exported-API only; this is an external pkg) ----

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitAddr(t *testing.T, s *server.Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server listener not ready within deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.Addr()
}

// waitLeader blocks until one node serves a keyed read locally (leader) rather
// than answering NOTLEADER, mirroring the routing harness's leader probe.
func waitLeader(t *testing.T, caddrs []string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, addr := range caddrs {
			c, err := client.DialTimeout(addr, time.Second)
			if err != nil {
				continue
			}
			v, err := c.Do("GET", "__probe__")
			_ = c.Close()
			if err == nil && !isNotLeaderReply(v) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no leader ready within deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func isNotLeaderReply(v resp.Value) bool {
	return v.Kind == resp.KindError && len(v.Str) >= 9 && v.Str[:9] == "NOTLEADER"
}

// ---- table rendering ----

func renderTable(rows []stats) string {
	headers := []string{"Mode", "Throughput", "p50", "p95", "p99"}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{
			r.mode,
			fmt.Sprintf("%.0f msg/s", r.throughput),
			fmtDur(r.p50),
			fmtDur(r.p95),
			fmtDur(r.p99),
		}
	}

	widths := make([]int, len(headers))
	for c, h := range headers {
		widths[c] = utf8.RuneCountInString(h)
	}
	for _, row := range cells {
		for c, v := range row {
			if n := utf8.RuneCountInString(v); n > widths[c] {
				widths[c] = n
			}
		}
	}

	var b []byte
	b = append(b, border(widths, "┌", "┬", "┐")...)
	b = append(b, dataRow(headers, widths)...)
	b = append(b, border(widths, "├", "┼", "┤")...)
	for _, row := range cells {
		b = append(b, dataRow(row, widths)...)
	}
	b = append(b, border(widths, "└", "┴", "┘")...)
	return string(b)
}

func border(widths []int, left, mid, right string) string {
	s := left
	for i, w := range widths {
		s += repeat("─", w+2)
		if i < len(widths)-1 {
			s += mid
		}
	}
	return s + right + "\n"
}

func dataRow(cols []string, widths []int) string {
	s := "│"
	for i, w := range widths {
		v := ""
		if i < len(cols) {
			v = cols[i]
		}
		s += " " + v + repeat(" ", w-utf8.RuneCountInString(v)) + " │"
	}
	return s + "\n"
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d >= time.Microsecond:
		return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
	default:
		return d.String()
	}
}
