package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/client"
	"github.com/prajwalmahajan101/toykv/internal/cluster"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Generous timings keep the leader stable for the duration of a routing
// assertion, so a role check is not invalidated by an election mid-test. Mirrors
// the M19 linearizability harness.
const (
	routeElectionMin = 2 * time.Second
	routeElectionMax = 4 * time.Second
	routeHeartbeat   = 300 * time.Millisecond
)

// routeFreeAddr returns a currently-free 127.0.0.1 host:port (loopback, so
// protected mode allows the unauthenticated bind). Same probe-and-close pattern
// as the cluster package's freeAddr.
func routeFreeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// routeCluster is n full Servers (each with its own RESP listener and Raft
// node), wired so every peer advertises its client address — the setup a
// ClusterClient needs to follow redirects.
type routeCluster struct {
	servers []*Server
	caddrs  []string // client-facing addresses, indexed like servers
}

func newRouteCluster(t *testing.T, n int) *routeCluster {
	t.Helper()

	names := []string{"n1", "n2", "n3", "n4", "n5"}
	caddrs := make([]string, n)
	raddrs := make([]string, n)
	peers := make([]cluster.Peer, n)
	for i := range n {
		caddrs[i] = routeFreeAddr(t)
		raddrs[i] = routeFreeAddr(t)
		peers[i] = cluster.Peer{ID: raft.NodeID(names[i]), Addr: raddrs[i], ClientAddr: caddrs[i]}
	}

	rc := &routeCluster{caddrs: caddrs}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	for i := range n {
		s, err := New(Config{
			Addr:               caddrs[i],
			Store:              store.New(),
			Log:                discard,
			Replicate:          true,
			NodeID:             names[i],
			Peers:              peers,
			RaftAddr:           raddrs[i],
			RaftDir:            t.TempDir(),
			ElectionTimeoutMin: routeElectionMin,
			ElectionTimeoutMax: routeElectionMax,
			HeartbeatInterval:  routeHeartbeat,
		})
		if err != nil {
			t.Fatalf("New(%s): %v", names[i], err)
		}
		rc.servers = append(rc.servers, s)
		go func() { _ = s.Run(ctx) }()
		t.Cleanup(func() { _ = s.Close() })
	}

	// Wait until every node accepts clients and one is serving as leader: a
	// keyed read on a follower answers NOTLEADER, on the leader it answers a
	// (nil) value. Poll until exactly that split appears.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if rc.leaderIndex(t) >= 0 {
			return rc
		}
		if time.Now().After(deadline) {
			t.Fatal("no leader ready within deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// leaderIndex probes each node with a bare (non-redirecting) client and returns
// the index of the one that serves a keyed read locally (the leader), or -1 if
// none is ready yet.
func (rc *routeCluster) leaderIndex(t *testing.T) int {
	t.Helper()
	for i, addr := range rc.caddrs {
		c, err := client.DialTimeout(addr, time.Second)
		if err != nil {
			continue
		}
		v, err := c.Do("GET", "__probe__")
		_ = c.Close()
		if err != nil {
			continue
		}
		if !isNotLeaderReply(v) {
			return i
		}
	}
	return -1
}

func isNotLeaderReply(v resp.Value) bool {
	return v.Kind == resp.KindError && len(v.Str) >= 9 && v.Str[:9] == "NOTLEADER"
}

// TestClusterClientRoutesWritesAndReads is M20's owned-risk test: a client that
// connects to any node completes every write via redirect and reads
// consistently from the leader; a READONLY connection serves follower-local
// state; a READWRITE read on a follower redirects.
func TestClusterClientRoutesWritesAndReads(t *testing.T) {
	rc := newRouteCluster(t, 3)
	leader := rc.leaderIndex(t)
	if leader < 0 {
		t.Fatal("leader vanished")
	}

	// 1. A write issued to EVERY node — including the two followers — must land
	//    via redirect, and the client must end up connected to the leader.
	for i, entry := range rc.caddrs {
		cc, err := client.DialClusterTimeout(entry, 2*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", entry, err)
		}
		v, err := cc.Do("SET", "k", "v")
		if err != nil {
			t.Fatalf("SET via node %d: %v", i, err)
		}
		if v.Kind != resp.KindSimpleString || v.Str != "OK" {
			t.Fatalf("SET via node %d = %+v, want +OK", i, v)
		}
		if got := cc.Addr(); got != rc.caddrs[leader] {
			t.Fatalf("after redirect client at %s, want leader %s", got, rc.caddrs[leader])
		}
		if _, err := cc.Do("INCR", "counter"); err != nil {
			t.Fatalf("INCR via node %d: %v", i, err)
		}
		_ = cc.Close()
	}

	// 2. A leader-model read from any entry node returns the converged value.
	cc, err := client.DialClusterTimeout(rc.caddrs[(leader+1)%3], 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	v, err := cc.Do("GET", "k")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if v.Kind != resp.KindBulkString || string(v.Bytes) != "v" {
		t.Fatalf("GET k = %+v, want bulk \"v\"", v)
	}

	// 3. On a follower: a default (READWRITE) keyed read redirects, while a
	//    READONLY connection serves the value from local, converged state.
	follower := (leader + 1) % 3
	fc, err := client.DialTimeout(rc.caddrs[follower], 2*time.Second)
	if err != nil {
		t.Fatalf("dial follower: %v", err)
	}
	defer fc.Close()

	rw, err := fc.Do("GET", "k")
	if err != nil {
		t.Fatalf("follower READWRITE GET: %v", err)
	}
	if !isNotLeaderReply(rw) {
		t.Fatalf("follower READWRITE GET = %+v, want a NOTLEADER redirect", rw)
	}

	if ok, err := fc.Do("READONLY"); err != nil || ok.Kind != resp.KindSimpleString {
		t.Fatalf("READONLY = %+v, err=%v; want +OK", ok, err)
	}
	// The follower applies asynchronously after commit; poll until it holds "v".
	roDeadline := time.Now().Add(5 * time.Second)
	for {
		ro, err := fc.Do("GET", "k")
		if err != nil {
			t.Fatalf("follower READONLY GET: %v", err)
		}
		if ro.Kind == resp.KindBulkString && string(ro.Bytes) == "v" {
			break // served locally, no redirect
		}
		if isNotLeaderReply(ro) {
			t.Fatalf("READONLY GET redirected (%+v); a READONLY conn must read locally", ro)
		}
		if time.Now().After(roDeadline) {
			t.Fatalf("follower never converged to \"v\" for a READONLY read (last %+v)", ro)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
