package cluster

import (
	"fmt"
	"sync"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// ApplyFunc executes a decoded mutating command against the local store and
// returns the reply the client should see. It is the server's replicated-
// execute path (Server.applyReplicated), which re-enters dispatch with a
// connState marked applying+authenticated so the handler mutates the store and
// writes the AOF exactly once, without re-proposing.
type ApplyFunc func(argv [][]byte) resp.Value

// StateMachine adapts toykv onto raft.StateMachine. Raft calls Apply for every
// committed entry, in index order, from a single goroutine (API-05); Apply
// decodes the envelope and runs it through ApplyFunc.
//
// Reply delivery. ToyRaft's Node.Propose returns (Index, Term, error) and
// discards Apply's result value at the Node boundary (only the error is
// surfaced). toykv replies carry data computed inside Apply — INCR's new value,
// LPUSH's length, DEL's count — that a follow-up read cannot reconstruct, so
// Apply stashes each reply under its log index and Node.Propose fetches it by
// the index Propose returned. This is race-free because Propose blocks until
// the entry is applied (SC3), so the stash is always present when Propose
// returns.
type StateMachine struct {
	apply ApplyFunc

	// snapshot, if set, materialises live store state for the forward-compat
	// serializer (see snapshot.go). M18 leaves Snapshot/Restore as the v1
	// ErrSnapshotUnsupported stubs; wiring real compaction on ToyRaft v2 is
	// then a one-line swap in Snapshot() (SerializeSnapshot(sm.snapshot())).
	snapshot func() []store.SnapshotEntry

	mu      sync.Mutex
	results map[raft.Index]resp.Value
}

// NewStateMachine builds a StateMachine over apply. snapshot may be nil (M18
// single-node has no compaction); it exists only to seed the forward-compat
// serializer for ToyRaft v2.
func NewStateMachine(apply ApplyFunc, snapshot func() []store.SnapshotEntry) *StateMachine {
	return &StateMachine{
		apply:    apply,
		snapshot: snapshot,
		results:  make(map[raft.Index]resp.Value),
	}
}

// Apply decodes and executes one committed entry, stashing its reply under the
// entry's index for Node.Propose to collect. A decode failure is fatal: a
// committed entry the state machine cannot parse means the replicated log is
// corrupt, and silently skipping it would diverge this replica's state.
//
// An application-level error reply (e.g. WRONGTYPE) is a normal outcome, not a
// Raft error: it is stashed like any other reply and returned as (reply, nil)
// so the entry stays applied and the client still sees the error.
func (sm *StateMachine) Apply(entry raft.Entry) (any, error) {
	argv, err := Decode(entry.Data)
	if err != nil {
		return nil, fmt.Errorf("cluster: apply index %d: %w", entry.Index, err)
	}
	reply := sm.apply(argv)
	sm.mu.Lock()
	sm.results[entry.Index] = reply
	sm.mu.Unlock()
	return reply, nil
}

// TakeResult removes and returns the reply stashed for idx. ok is false if no
// reply is present (the entry was applied on a node that did not propose it —
// a multi-node M19 case; on M18's single node every applied entry was proposed
// locally and is always taken).
func (sm *StateMachine) TakeResult(idx raft.Index) (resp.Value, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	v, ok := sm.results[idx]
	delete(sm.results, idx)
	return v, ok
}

// Snapshot implements the raft.StateMachine v1 contract: snapshots are
// unsupported until ToyRaft v2. The real serialization is built and tested in
// snapshot.go so promoting this is a one-line change once v2 lands.
func (sm *StateMachine) Snapshot() ([]byte, raft.Index, error) {
	return nil, 0, raft.ErrSnapshotUnsupported
}

// Restore implements the raft.StateMachine v1 contract (see Snapshot).
func (sm *StateMachine) Restore([]byte) error {
	return raft.ErrSnapshotUnsupported
}
