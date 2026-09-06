package cluster

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"

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
	Init: func() interface{} { return 0 },
	Step: func(st, in, out interface{}) (bool, interface{}) {
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

func checkLinearizable(t *testing.T, n int) {
	t.Helper()
	tc := newTestCluster(t, n)
	leader := tc.leader(t)
	leaderStore := tc.stores[leader]

	const (
		clients = 4
		opsEach = 40
	)

	// A single monotonic time source shared by all clients. time.Since uses the
	// monotonic clock, so successive reads are non-decreasing across goroutines —
	// exactly what Porcupine's closed-interval [Call, Return] model expects.
	base := time.Now()
	now := func() int64 { return int64(time.Since(base)) }

	// One op executed and recorded. Writes go through the leader's Propose (the
	// real replicated path); the read reads the leader's applied state locally.
	do := func(clientID int, in regInput) porcupine.Operation {
		call := now()
		var out int
		switch in.op {
		case "set":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := tc.nodes[leader].Propose(ctx, [][]byte{[]byte("SET"), []byte("x"), []byte(strconv.Itoa(in.val))})
			cancel()
			if err != nil {
				t.Fatalf("client %d SET(%d): %v", clientID, in.val, err)
			}
		case "incr":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			reply, err := tc.nodes[leader].Propose(ctx, [][]byte{[]byte("INCR"), []byte("x")})
			cancel()
			if err != nil {
				t.Fatalf("client %d INCR: %v", clientID, err)
			}
			out = int(reply.Int) // INCR's applied new value (the reply captured by index)
		case "get":
			out = readRegister(t, leaderStore)
		}
		return porcupine.Operation{ClientId: clientID, Input: in, Call: call, Output: out, Return: now()}
	}

	// Each client runs a deterministic, client-specific op sequence (a per-client
	// LCG — no shared/global RNG, no wall-clock seeding), writing into its own
	// slice so there is no shared-slice contention to record around.
	perClient := make([][]porcupine.Operation, clients)
	var wg sync.WaitGroup
	for c := range clients {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			ops := make([]porcupine.Operation, 0, opsEach)
			r := uint64(c*2654435761 + 1) // distinct nonzero seed per client
			for range opsEach {
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
				ops = append(ops, do(c, in))
			}
			perClient[c] = ops
		}(c)
	}
	wg.Wait()

	history := make([]porcupine.Operation, 0, clients*opsEach)
	for _, ops := range perClient {
		history = append(history, ops...)
	}

	if !porcupine.CheckOperations(registerModel, history) {
		// Re-run verbosely for a definitive result within a bounded time budget.
		res, _ := porcupine.CheckOperationsVerbose(registerModel, history, 10*time.Second)
		t.Fatalf("history is not linearizable at N=%d (verbose result: %v)", n, res)
	}
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
