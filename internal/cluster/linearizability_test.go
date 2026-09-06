package cluster

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
	"github.com/prajwalmahajan101/toyraft/pkg/raft"
	"github.com/prajwalmahajan101/toyraft/pkg/storage/memory"
	httptransport "github.com/prajwalmahajan101/toyraft/pkg/transport/http"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// M19.3 — linearizability harness.
//
// This is the final correctness proof of the M19 distributed core: under
// concurrent clients, the replicated register behaves as if every operation took
// effect atomically at a single instant between its call and its return — i.e.
// the observable history is linearizable. We drive SET/GET/INCR concurrently
// against a running N-node cluster (over the real HTTP transport, in-process so
// -race covers the whole consensus+apply path), record each operation's call and
// return timestamps, and hand the history to Porcupine's linearizability checker
// against a single-integer-register model.
//
// Scope is happy-path concurrency with a stable leader — M19.2 owns failover
// correctness, and failover-linearizability needs M20's client redirect. See the
// M19.3 plan and docs/ROADMAP.md.

// regInput is one recorded operation against the single register "x".
type regInput struct {
	op  string // "set" | "get" | "incr"
	val int    // set argument (ignored for get/incr)
}

// registerModel is the sequential specification Porcupine linearizes against: a
// single integer register, initial value 0 (an absent key reads as 0). SET
// overwrites; INCR returns the new value; GET returns the current value.
var registerModel = porcupine.Model{
	Init: func() any { return 0 },
	Step: func(st, in, out any) (bool, any) {
		s := st.(int)
		i := in.(regInput)
		o := out.(int)
		switch i.op {
		case "set":
			return true, i.val
		case "incr":
			return o == s+1, s + 1
		case "get":
			return o == s, s
		}
		return false, s
	},
	// Equal defaults to shallowEqual (ints compare correctly); Partition unused.
}

// TestLinearizableRegister proves the replicated register is linearizable under
// concurrent load at cluster sizes N = 3 and N = 5.
func TestLinearizableRegister(t *testing.T) {
	for _, n := range []int{3, 5} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			checkLinearizable(t, n)
		})
	}
}

const (
	linClients  = 4
	linOpsEach  = 40
	linAttempts = 8 // stable-leader windows to try before giving up
)

// Generous consensus timeouts for the linearizability cluster. The M19.1/M19.2
// harnesses use ToyRaft's aggressive defaults (150–300ms election, 50ms
// heartbeat), which churn leadership constantly under -race on a loaded CI
// runner — fine for tests that tolerate/expect failover, fatal for one that
// needs a *stable* leader. A 2–4s election window with a 300ms heartbeat
// (300*3 ≤ 2000, satisfying ToyRaft's HeartbeatInterval*3 ≤ ElectionTimeoutMin
// invariant) lets the leader ride out -race scheduling hiccups.
const (
	linElectionMin = 2 * time.Second
	linElectionMax = 4 * time.Second
	linHeartbeat   = 300 * time.Millisecond
)

func checkLinearizable(t *testing.T, n int) {
	t.Helper()
	tc := newStableCluster(t, n)

	// M19.3 scope is happy-path concurrency with a *stable* leader: leader-local
	// GETs are only linearizable while one node stays leader (ToyRaft v1 has no
	// ReadIndex — a follower or freshly-elected leader can serve a stale read;
	// that is M20). Under -race on a loaded CI runner the cluster can spuriously
	// re-elect (heartbeats delayed past the election timeout), so we only ever
	// hand Porcupine a window we have *verified* ran under a single leadership:
	// if a Propose errors or the leader/term changes mid-run, we discard the
	// window and retry against a freshly-resolved leader.
	for attempt := 1; attempt <= linAttempts; attempt++ {
		history, stable := recordStableWindow(t, tc)
		if !stable {
			t.Logf("N=%d: leadership churned mid-window (attempt %d/%d); retrying", n, attempt, linAttempts)
			continue
		}
		if porcupine.CheckOperations(registerModel, history) {
			return // linearizable
		}
		// Genuinely non-linearizable — a real correctness failure, not churn.
		res, _ := porcupine.CheckOperationsVerbose(registerModel, history, 10*time.Second)
		t.Fatalf("history is not linearizable at N=%d (verbose result: %v)", n, res)
	}
	t.Fatalf("N=%d: no stable-leader window in %d attempts (cluster kept re-electing)", n, linAttempts)
}

// recordStableWindow drives linClients×linOpsEach concurrent SET/GET/INCR ops
// against the current leader and returns the recorded history. stable is false
// if leadership was not held for the whole window (a Propose error, or the
// leader/term changed) — in which case the history is discarded by the caller.
func recordStableWindow(t *testing.T, tc *testCluster) (history []porcupine.Operation, stable bool) {
	t.Helper()
	leader := tc.leader(t)
	startTerm := tc.nodes[leader].Status().Term

	// Reset the register so the model's Init:0 matches actual state at the start
	// of every attempt (a prior discarded window may have left x non-zero).
	resetCtx, resetCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, resetErr := tc.nodes[leader].Propose(resetCtx, [][]byte{[]byte("SET"), []byte("x"), []byte("0")})
	resetCancel()
	if resetErr != nil {
		return nil, false
	}

	// A single monotonic time source shared by all clients. time.Since uses the
	// monotonic clock, so successive reads are non-decreasing across goroutines —
	// exactly what Porcupine's closed-interval [Call, Return] model expects.
	base := time.Now()
	now := func() int64 { return int64(time.Since(base)) }

	// Cancelled on the first sign of leadership loss so the remaining clients bail
	// promptly instead of each blocking a full Propose timeout on a dead leader.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var churned atomic.Bool

	// One op executed and recorded. Writes go through the leader's Propose (the
	// real replicated path); the read reads the leader's applied state locally.
	// ok=false signals leadership loss (abort the window).
	do := func(clientID int, in regInput) (porcupine.Operation, bool) {
		call := now()
		var out int
		switch in.op {
		case "set":
			opCtx, opCancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := tc.nodes[leader].Propose(opCtx, [][]byte{[]byte("SET"), []byte("x"), []byte(strconv.Itoa(in.val))})
			opCancel()
			if err != nil {
				return porcupine.Operation{}, false
			}
		case "incr":
			opCtx, opCancel := context.WithTimeout(ctx, 2*time.Second)
			reply, err := tc.nodes[leader].Propose(opCtx, [][]byte{[]byte("INCR"), []byte("x")})
			opCancel()
			if err != nil {
				return porcupine.Operation{}, false
			}
			out = int(reply.Int) // INCR's applied new value (the reply captured by index)
		case "get":
			out = readRegister(t, tc.stores[leader])
		}
		return porcupine.Operation{ClientId: clientID, Input: in, Call: call, Output: out, Return: now()}, true
	}

	// Each client runs a deterministic, client-specific op sequence (a per-client
	// LCG — no shared/global RNG, no wall-clock seeding), writing into its own
	// slice so there is no shared-slice contention to record around.
	perClient := make([][]porcupine.Operation, linClients)
	var wg sync.WaitGroup
	for c := range linClients {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			ops := make([]porcupine.Operation, 0, linOpsEach)
			r := uint64(c*2654435761 + 1) // distinct nonzero seed per client
			for range linOpsEach {
				if ctx.Err() != nil {
					return // another client already saw leadership loss
				}
				r = r*6364136223846793005 + 1442695040888963407 // LCG step
				var in regInput
				switch (r >> 33) % 3 {
				case 0:
					in = regInput{op: "set", val: int((r >> 40) % 100)}
				case 1:
					in = regInput{op: "incr"}
				default:
					in = regInput{op: "get"}
				}
				op, ok := do(c, in)
				if !ok {
					churned.Store(true)
					cancel()
					return
				}
				ops = append(ops, op)
			}
			perClient[c] = ops
		}(c)
	}
	wg.Wait()

	// The window is only valid if no op failed AND the same node held leadership
	// at the same term throughout.
	if churned.Load() {
		return nil, false
	}
	st := tc.nodes[leader].Status()
	if st.Role != raft.Leader || st.Term != startTerm {
		return nil, false
	}

	history = make([]porcupine.Operation, 0, linClients*linOpsEach)
	for _, ops := range perClient {
		history = append(history, ops...)
	}
	return history, true
}

// newStableCluster stands up an n-node cluster over the real HTTP transport,
// mirroring newTestCluster (cluster_test.go) but with generous election/heartbeat
// timeouts so one node holds leadership for the whole test even under -race. Nodes
// are built directly (in-package) to inject the tuned raft.Config; an in-memory
// Raft log is sufficient (no restart/recovery is exercised here).
func newStableCluster(t *testing.T, n int) *testCluster {
	t.Helper()

	names := []string{"n1", "n2", "n3", "n4", "n5"}
	ids := make([]raft.NodeID, n)
	addrs := make([]string, n)
	for i := range n {
		ids[i] = raft.NodeID(names[i])
		addrs[i] = freeAddr(t) // defined in cluster_test.go
	}
	allIDs := append([]raft.NodeID(nil), ids...)

	tc := &testCluster{ids: ids}
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

		s := store.New()
		sm := NewStateMachine(storeApply(s), func() []store.SnapshotEntry { return s.Snapshot() })
		rn, err := raft.New(raft.Config{
			NodeID:             ids[i],
			Peers:              allIDs,
			Storage:            memory.New(),
			Transport:          httpT,
			StateMachine:       sm,
			ElectionTimeoutMin: linElectionMin,
			ElectionTimeoutMax: linElectionMax,
			HeartbeatInterval:  linHeartbeat,
		})
		if err != nil {
			_ = httpT.Close()
			t.Fatalf("raft.New(%s): %v", ids[i], err)
		}
		tc.nodes = append(tc.nodes, &Node{raft: rn, sm: sm, closers: []io.Closer{httpT}})
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

	// A larger election window means a slower first election — wait generously.
	for i, node := range tc.nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := node.WaitLeader(ctx); err != nil {
			cancel()
			t.Fatalf("WaitLeader(%s): %v", ids[i], err)
		}
		cancel()
	}
	return tc
}

// readRegister reads the current integer value of key "x" from a store; an absent
// or empty key reads as 0.
func readRegister(t *testing.T, s *store.Store) int {
	t.Helper()
	b, ok, err := s.Get("x")
	if err != nil {
		t.Fatalf("Get(x): %v", err)
	}
	if !ok || len(b) == 0 {
		return 0
	}
	v, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("register value %q not an integer: %v", b, err)
	}
	return v
}
