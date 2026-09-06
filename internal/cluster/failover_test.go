package cluster

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"
	"github.com/prajwalmahajan101/toyraft/pkg/storage/memory"
	httptransport "github.com/prajwalmahajan101/toyraft/pkg/transport/http"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// M19.2 — failover + partition correctness.
//
// These tests inject real network failures into a running multi-node cluster and
// prove the three exit conditions: a leader kill loses no acked write, a
// partition heals to the majority's history, and no two leaders ever hold the
// same term (split-brain guard).
//
// Failure injection is done with chaosTransport, a wrapper around ToyRaft's real
// pkg/transport/http that drops messages crossing a partition cut. It wraps the
// *real* transport (not a simulation) so the tests exercise the production
// consensus path over HTTP. ToyRaft's own inproc.Hub — the natural home for this
// chaos — is not externally constructible at v1.0.0-rc.2 (HubConfig.Clock is typed
// on internal/clock with no public constructor); see docs/TOYRAFT-MIGRATION-REPORT.md.

// ---------------------------------------------------------------------------
// chaosTransport — a partition-injecting wrapper over a real raft.Transport.
// ---------------------------------------------------------------------------

// partitionState is a shared, mutable set of symmetric network cuts between node
// pairs. Test goroutines toggle cuts via partition/heal; each node's
// chaosTransport consults it on every message. It is mutex-guarded because raft
// tick loops call Send concurrently with the test driver.
type partitionState struct {
	mu  sync.RWMutex
	cut map[[2]raft.NodeID]bool // keyed by the canonical (lo, hi) ordering
}

func newPartitionState() *partitionState {
	return &partitionState{cut: make(map[[2]raft.NodeID]bool)}
}

func cutKey(a, b raft.NodeID) [2]raft.NodeID {
	if a > b {
		a, b = b, a
	}
	return [2]raft.NodeID{a, b}
}

func (p *partitionState) partition(a, b raft.NodeID) {
	p.mu.Lock()
	p.cut[cutKey(a, b)] = true
	p.mu.Unlock()
}

func (p *partitionState) heal(a, b raft.NodeID) {
	p.mu.Lock()
	delete(p.cut, cutKey(a, b))
	p.mu.Unlock()
}

func (p *partitionState) blocked(a, b raft.NodeID) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cut[cutKey(a, b)]
}

// chaosTransport wraps a real raft.Transport and silently drops any message that
// crosses a partition cut — in both directions: outbound (Send to a cut peer) and
// inbound (a delivered message from a cut peer). Dropping mirrors a real network
// partition; ToyRaft's heartbeat-retry model already treats Send as best-effort,
// so a dropped message is indistinguishable from a lost packet.
type chaosTransport struct {
	self  raft.NodeID
	inner raft.Transport
	part  *partitionState
}

func (c *chaosTransport) Send(ctx context.Context, msg raft.Message) error {
	if c.part.blocked(c.self, msg.To) {
		return nil // partitioned: drop outbound
	}
	return c.inner.Send(ctx, msg)
}

func (c *chaosTransport) Register(step func(ctx context.Context, msg raft.Message) error) {
	c.inner.Register(func(ctx context.Context, msg raft.Message) error {
		if c.part.blocked(c.self, msg.From) {
			return nil // partitioned: drop inbound
		}
		return step(ctx, msg)
	})
}

func (c *chaosTransport) Close() error { return c.inner.Close() }

// ---------------------------------------------------------------------------
// hubCluster — an in-process N-node cluster over chaos-wrapped HTTP transports.
// ---------------------------------------------------------------------------

type hubCluster struct {
	nodes  []*Node
	stores []*store.Store
	ids    []raft.NodeID
	part   *partitionState
}

// newHubCluster stands up an n-node (n odd) cluster over the real HTTP transport,
// each transport wrapped in a chaosTransport sharing one partitionState. It
// mirrors newTestCluster (cluster_test.go) but builds each *Node directly so the
// wrapped transport and an in-memory Raft log can be injected — the reply-capture
// path (Propose→Apply→TakeResult) is identical to the production constructor.
func newHubCluster(t *testing.T, n int) *hubCluster {
	t.Helper()

	names := []string{"n1", "n2", "n3", "n4", "n5"}
	ids := make([]raft.NodeID, n)
	addrs := make([]string, n)
	for i := range n {
		ids[i] = raft.NodeID(names[i])
		addrs[i] = freeAddr(t) // defined in cluster_test.go
	}
	allIDs := append([]raft.NodeID(nil), ids...)

	hc := &hubCluster{ids: ids, part: newPartitionState()}
	for i := range n {
		peerURLs := make(map[raft.NodeID]string, n-1)
		for j := range n {
			if j != i {
				peerURLs[ids[j]] = "http://" + addrs[j]
			}
		}
		httpT, err := httptransport.New(httptransport.Config{
			NodeID:      ids[i],
			ListenAddr:  addrs[i],
			PeerURLs:    peerURLs,
			SendTimeout: peerSendTimeout,
			Backoff: httptransport.BackoffConfig{
				Base:        peerBackoffBase,
				Factor:      peerBackoffFactor,
				MaxAttempts: peerBackoffAttempts,
			},
			ShutdownTimeout: peerShutdownTimeout,
		})
		if err != nil {
			t.Fatalf("httptransport.New(%s): %v", ids[i], err)
		}
		tr := &chaosTransport{self: ids[i], inner: httpT, part: hc.part}

		s := store.New()
		sm := NewStateMachine(storeApply(s), func() []store.SnapshotEntry { return s.Snapshot() })
		rn, err := raft.New(raft.Config{
			NodeID:       ids[i],
			Peers:        allIDs,
			Storage:      memory.New(),
			Transport:    tr,
			StateMachine: sm,
		})
		if err != nil {
			_ = tr.Close()
			t.Fatalf("raft.New(%s): %v", ids[i], err)
		}
		hc.nodes = append(hc.nodes, &Node{raft: rn, sm: sm, closers: []io.Closer{tr}})
		hc.stores = append(hc.stores, s)
	}

	for i, node := range hc.nodes {
		if err := node.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", ids[i], err)
		}
	}
	t.Cleanup(func() {
		for _, node := range hc.nodes {
			_ = node.Stop()
		}
	})

	for i, node := range hc.nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := node.WaitLeader(ctx); err != nil {
			cancel()
			t.Fatalf("WaitLeader(%s): %v", ids[i], err)
		}
		cancel()
	}
	return hc
}

// leader returns the index of the node currently reporting Leader, polling
// briefly because the role can settle just after WaitLeader.
func (hc *hubCluster) leader(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for i, node := range hc.nodes {
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

// waitLeaderAmong polls for a node in group reporting Leader at a term strictly
// above minTerm. Both restrictions matter: an isolated old leader keeps Role
// Leader at its stale term until it sees a higher term, so a cluster-wide scan or
// a missing term floor would latch onto the wrong (partitioned) node.
func (hc *hubCluster) waitLeaderAmong(t *testing.T, minTerm raft.Term, group []int, timeout time.Duration) (int, raft.Term) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, i := range group {
			st := hc.nodes[i].Status()
			if st.Role == raft.Leader && st.Term > minTerm {
				return i, st.Term
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no leader elected in group %v at term > %d within %s", group, minTerm, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertNoTwoLeadersSameTerm fails if two nodes report Leader at the SAME term.
// Two Role==Leader nodes at DIFFERENT terms is legal and expected during a
// partition (the isolated old leader is stale); two at one term is split-brain.
func (hc *hubCluster) assertNoTwoLeadersSameTerm(t *testing.T) {
	t.Helper()
	seen := make(map[raft.Term]raft.NodeID)
	for i, node := range hc.nodes {
		st := node.Status()
		if st.Role != raft.Leader {
			continue
		}
		if other, ok := seen[st.Term]; ok {
			t.Fatalf("split-brain: two leaders at term %d: %s and %s", st.Term, other, hc.ids[i])
		}
		seen[st.Term] = hc.ids[i]
	}
}

// isolate cuts node idx off from every other node (a minority of one).
func (hc *hubCluster) isolate(idx int) {
	for j := range hc.nodes {
		if j != idx {
			hc.part.partition(hc.ids[idx], hc.ids[j])
		}
	}
}

// partitionGroups cuts every pair spanning groups a and b.
func (hc *hubCluster) partitionGroups(a, b []int) {
	for _, i := range a {
		for _, j := range b {
			hc.part.partition(hc.ids[i], hc.ids[j])
		}
	}
}

// healAll removes every partition cut.
func (hc *hubCluster) healAll() {
	for i := range hc.nodes {
		for j := i + 1; j < len(hc.nodes); j++ {
			hc.part.heal(hc.ids[i], hc.ids[j])
		}
	}
}

// proposeErr proposes a command to node and returns only the error (the reply
// value is irrelevant to these correctness tests).
func (hc *hubCluster) proposeErr(node int, argv ...string) error {
	bs := make([][]byte, len(argv))
	for i, a := range argv {
		bs[i] = []byte(a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := hc.nodes[node].Propose(ctx, bs)
	return err
}

// indicesExcept returns [0,n) without ex.
func indicesExcept(n, ex int) []int {
	g := make([]int, 0, n-1)
	for i := range n {
		if i != ex {
			g = append(g, i)
		}
	}
	return g
}

// complement returns [0,n) minus the indices in subset.
func complement(n int, subset []int) []int {
	in := make(map[int]bool, len(subset))
	for _, i := range subset {
		in[i] = true
	}
	out := make([]int, 0, n-len(subset))
	for i := range n {
		if !in[i] {
			out = append(out, i)
		}
	}
	return out
}

// storesAt returns the stores at the given node indices.
func (hc *hubCluster) storesAt(idx []int) []*store.Store {
	ss := make([]*store.Store, 0, len(idx))
	for _, i := range idx {
		ss = append(ss, hc.stores[i])
	}
	return ss
}

// waitConverged polls until every store satisfies pred, or fails after timeout.
func waitConverged(t *testing.T, stores []*store.Store, pred func(*store.Store) bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		converged := true
		for _, s := range stores {
			if !pred(s) {
				converged = false
				break
			}
		}
		if converged {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not converge within %s: %s", timeout, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// The three owned-risk tests.
// ---------------------------------------------------------------------------

// TestLeaderKillNoAckedWriteLoss proves exit condition 1: after the leader is
// killed (isolated), the majority elects a new leader and every acked write
// survives. Then the old leader rejoins on heal and re-converges.
func TestLeaderKillNoAckedWriteLoss(t *testing.T) {
	hc := newHubCluster(t, 3)
	leader := hc.leader(t)
	oldTerm := hc.nodes[leader].Status().Term

	// Stream acked writes: each successful Propose is committed and applied.
	const acks = 20
	for i := range acks {
		if err := hc.proposeErr(leader, "INCR", "c"); err != nil {
			t.Fatalf("acked write %d on leader: %v", i, err)
		}
	}
	if err := hc.proposeErr(leader, "SET", "k", "v"); err != nil {
		t.Fatalf("acked SET on leader: %v", err)
	}

	// Kill the leader: isolate it from every peer.
	hc.isolate(leader)

	// The connected majority must elect a new leader at a higher term.
	majority := indicesExcept(len(hc.nodes), leader)
	newLeader, _ := hc.waitLeaderAmong(t, oldTerm, majority, 5*time.Second)

	// A new leader does not commit prior-term entries by replica count (ToyRaft
	// REPL-06 / Figure-8 guard): the 20 acked INCRs sit replicated-but-uncommitted
	// until a current-term entry above them commits. So proposing one new write
	// flushes the whole acked prefix — and if any acked write had been lost, this
	// INCR would not land on 21.
	if err := hc.proposeErr(newLeader, "INCR", "c"); err != nil {
		t.Fatalf("post-failover write on new leader: %v", err)
	}
	majStores := hc.storesAt(majority)
	waitConverged(t, majStores, func(s *store.Store) bool {
		if v, ok, _ := s.Get("c"); !ok || string(v) != "21" {
			return false
		}
		v, ok, _ := s.Get("k")
		return ok && string(v) == "v"
	}, 5*time.Second, "majority missing acked writes after leader kill")

	// Heal: the old leader rejoins and reconciles to the winning history.
	hc.healAll()
	waitConverged(t, hc.stores, func(s *store.Store) bool {
		v, ok, _ := s.Get("c")
		return ok && string(v) == "21"
	}, 5*time.Second, "old leader did not re-converge after heal")
}

// TestPartitionHealReconciliation proves exit condition 2: a partition splits the
// cluster; the majority commits writes while the isolated old leader cannot; on
// heal every node converges to the majority's committed history and the old
// leader's uncommitted tail leaves no trace in the store.
func TestPartitionHealReconciliation(t *testing.T) {
	hc := newHubCluster(t, 5)
	leader := hc.leader(t)
	oldTerm := hc.nodes[leader].Status().Term

	// Baseline committed on the original 5-node cluster.
	if err := hc.proposeErr(leader, "SET", "committed", "1"); err != nil {
		t.Fatalf("baseline write: %v", err)
	}

	// Partition: old leader + one buddy = a 2-node minority; the other 3 = majority.
	buddy := (leader + 1) % len(hc.nodes)
	minority := []int{leader, buddy}
	majority := complement(len(hc.nodes), minority)
	hc.partitionGroups(minority, majority)

	// The majority elects a new leader and commits writes.
	newLeader, _ := hc.waitLeaderAmong(t, oldTerm, majority, 5*time.Second)
	if err := hc.proposeErr(newLeader, "SET", "majoritywrite", "yes"); err != nil {
		t.Fatalf("majority write: %v", err)
	}
	if err := hc.proposeErr(newLeader, "INCR", "committed"); err != nil {
		t.Fatalf("majority INCR: %v", err)
	}

	// A write to the isolated old leader cannot reach quorum, so it never commits
	// and is never applied to its store.
	if err := hc.proposeErr(leader, "SET", "minoritywrite", "orphan"); err == nil {
		t.Fatal("write to isolated old leader committed; want failure (no quorum)")
	}
	if _, ok, _ := hc.stores[leader].Get("minoritywrite"); ok {
		t.Fatal("isolated leader applied an uncommitted write to its store")
	}

	// Heal: the whole cluster reconciles to the majority's committed history; the
	// orphan write exists nowhere.
	hc.healAll()
	waitConverged(t, hc.stores, func(s *store.Store) bool {
		if v, ok, _ := s.Get("majoritywrite"); !ok || string(v) != "yes" {
			return false
		}
		if v, ok, _ := s.Get("committed"); !ok || string(v) != "2" {
			return false
		}
		_, orphan, _ := s.Get("minoritywrite")
		return !orphan
	}, 10*time.Second, "cluster did not reconcile to majority history after heal")
}

// TestNoSplitBrainDoubleLeader proves exit condition 3: while the leader is
// partitioned into a minority, no two nodes ever hold Leader at the same term,
// and the isolated leader's CommitIndex never advances without quorum.
func TestNoSplitBrainDoubleLeader(t *testing.T) {
	hc := newHubCluster(t, 5)
	leader := hc.leader(t)
	oldTerm := hc.nodes[leader].Status().Term
	oldCommit := hc.nodes[leader].Status().CommitIndex

	// Isolate the leader (minority of one); the 4-node majority elects a successor.
	hc.isolate(leader)
	majority := indicesExcept(len(hc.nodes), leader)
	hc.waitLeaderAmong(t, oldTerm, majority, 5*time.Second)

	// Across a window: never two leaders at one term, and the isolated leader
	// cannot advance its commit index (no quorum => no commit).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hc.assertNoTwoLeadersSameTerm(t)
		if c := hc.nodes[leader].Status().CommitIndex; c != oldCommit {
			t.Fatalf("isolated leader advanced CommitIndex %d -> %d without quorum", oldCommit, c)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
