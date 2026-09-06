package cluster

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// freeAddr returns a currently-free 127.0.0.1 host:port. There is a small
// window between closing the probe listener and the transport rebinding, but it
// is more than sufficient for an in-process test and avoids hard-coding ports.
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

// testCluster is a set of in-process cluster Nodes wired over the real
// pkg/transport/http, each backed by its own store.
type testCluster struct {
	nodes  []*Node
	stores []*store.Store
	ids    []raft.NodeID
}

// newTestCluster stands up an n-node cluster (n odd) over HTTP on localhost,
// starts every node, and waits for a leader to emerge. It registers cleanup.
func newTestCluster(t *testing.T, n int) *testCluster {
	t.Helper()

	ids := make([]raft.NodeID, n)
	peers := make([]Peer, n)
	for i := range n {
		ids[i] = raft.NodeID([]string{"n1", "n2", "n3", "n4", "n5"}[i])
		peers[i] = Peer{ID: ids[i], Addr: freeAddr(t)}
	}

	tc := &testCluster{ids: ids}
	for i := range n {
		s := store.New()
		node, err := New(Config{
			NodeID:   string(ids[i]),
			Peers:    peers,
			RaftAddr: peers[i].Addr,
			RaftDir:  t.TempDir(),
			Apply:    storeApply(s),
			Snapshot: func() []store.SnapshotEntry { return s.Snapshot() },
			// Generous election/heartbeat timings (as newStableCluster uses) keep
			// the leader from churning mid-Propose on slow CI runners under -race —
			// the happy-path replication tests assert convergence, not election
			// speed. Reuses the M19.3 stable-harness constants.
			ElectionTimeoutMin: linElectionMin,
			ElectionTimeoutMax: linElectionMax,
			HeartbeatInterval:  linHeartbeat,
		})
		if err != nil {
			t.Fatalf("New(%s): %v", ids[i], err)
		}
		tc.nodes = append(tc.nodes, node)
		tc.stores = append(tc.stores, s)
	}

	for i, node := range tc.nodes {
		if err := node.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", ids[i], err)
		}
	}
	t.Cleanup(func() {
		for _, node := range tc.nodes {
			_ = node.Stop()
		}
	})

	// Every node must observe a leader before the test proceeds.
	for i, node := range tc.nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := node.WaitLeader(ctx); err != nil {
			cancel()
			t.Fatalf("WaitLeader(%s): %v", ids[i], err)
		}
		cancel()
	}
	return tc
}

// leader returns the index of the node currently reporting the Leader role,
// polling briefly because a node's role can settle just after WaitLeader.
func (tc *testCluster) leader(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for i, node := range tc.nodes {
			if node.Role() == raft.Leader {
				return i
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no leader found")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClusterReplicatesToAllFollowers is M19.1's owned happy-path risk test: a
// leader-acked write appears on every follower. Three nodes over the real HTTP
// transport; the leader proposes a stream of mutating commands; each node's
// store must converge to the same state. No failure injection — that is M19.2.
func TestClusterReplicatesToAllFollowers(t *testing.T) {
	tc := newTestCluster(t, 3)
	leader := tc.leader(t)

	propose := func(argv ...string) {
		bs := make([][]byte, len(argv))
		for i, a := range argv {
			bs[i] = []byte(a)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := tc.nodes[leader].Propose(ctx, bs); err != nil {
			t.Fatalf("Propose(%v) on leader: %v", argv, err)
		}
	}

	propose("SET", "k", "v")
	propose("INCR", "c")
	propose("INCR", "c")
	propose("RPUSH", "l", "a", "b", "c")

	// Followers apply asynchronously after commit, so poll for convergence.
	want := func(s *store.Store) bool {
		if v, ok, _ := s.Get("k"); !ok || string(v) != "v" {
			return false
		}
		if v, ok, _ := s.Get("c"); !ok || string(v) != "2" {
			return false
		}
		if n, err := s.LLen("l"); err != nil || n != 3 {
			return false
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		converged := true
		for _, s := range tc.stores {
			if !want(s) {
				converged = false
				break
			}
		}
		if converged {
			break
		}
		if time.Now().After(deadline) {
			for i, s := range tc.stores {
				v, _, _ := s.Get("c")
				t.Logf("node %s: c=%q", tc.ids[i], v)
			}
			t.Fatal("cluster did not converge on all nodes within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFollowerProposeReturnsNotLeader proves a write proposed to a follower is
// rejected with ErrNotLeader carrying a leader hint — the signal M20's client
// redirect will act on. It is never silently dropped.
func TestFollowerProposeReturnsNotLeader(t *testing.T) {
	tc := newTestCluster(t, 3)
	leader := tc.leader(t)
	follower := (leader + 1) % len(tc.nodes)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := tc.nodes[follower].Propose(ctx, [][]byte{[]byte("SET"), []byte("k"), []byte("v")})
	if err == nil {
		t.Fatal("Propose on follower succeeded; want ErrNotLeader")
	}
	notLeader, ok := errors.AsType[*raft.ErrNotLeader](err)
	if !ok {
		t.Fatalf("Propose error = %v; want *raft.ErrNotLeader", err)
	}
	if notLeader.LeaderHint == "" {
		t.Fatal("ErrNotLeader carried no leader hint")
	}
}
