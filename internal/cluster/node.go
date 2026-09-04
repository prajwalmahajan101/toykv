package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"
	"github.com/prajwalmahajan101/toyraft/pkg/storage/memory"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// noopTransport is a raft.Transport that ships nothing. A single-node cluster
// (Peers=[self]) never fans out vote or heartbeat messages — ToyRaft's
// candidate path excludes self and its single-node fast path (ELEC-04) makes
// the self-vote an immediate quorum — so no real transport is needed. This
// also sidesteps pkg/transport/inproc, whose required HubConfig.Clock is typed
// on toyraft's internal/clock package and is therefore not constructible from
// an external module. Real peer transport (pkg/transport/http) arrives in M19.
type noopTransport struct{}

func (noopTransport) Send(context.Context, raft.Message) error                   { return nil }
func (noopTransport) Register(func(ctx context.Context, msg raft.Message) error) {}
func (noopTransport) Close() error                                               { return nil }

// Node wraps a single-node raft.Node plus the toykv StateMachine, exposing the
// Propose→reply path the server needs. The in-memory Raft log starts empty on
// every boot; durable state is re-derived from the AOF before the node starts
// (M18 decision: AOF remains the durability source).
type Node struct {
	raft raft.Node
	sm   *StateMachine
}

// New builds a single-node cluster with nodeID as the sole peer. apply is the
// server's replicated-execute path; snapshot (nil-able) seeds the forward-compat
// serializer only. The node is not started — call Start.
func New(nodeID string, apply ApplyFunc, snapshot func() []store.SnapshotEntry, logger *slog.Logger) (*Node, error) {
	sm := NewStateMachine(apply, snapshot)
	id := raft.NodeID(nodeID)
	rn, err := raft.New(raft.Config{
		NodeID:       id,
		Peers:        []raft.NodeID{id}, // self is the whole cluster in M18
		Storage:      memory.New(),      // in-memory Raft log; AOF is the durability source
		Transport:    noopTransport{},
		StateMachine: sm,
		Logger:       logger,
		// Clock left nil → real clock; election/heartbeat timings default.
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: build raft node: %w", err)
	}
	return &Node{raft: rn, sm: sm}, nil
}

// Start brings the Raft node up. On a single-node cluster it wins the first
// election immediately and becomes leader.
func (n *Node) Start(ctx context.Context) error {
	if err := n.raft.Start(ctx); err != nil {
		return fmt.Errorf("cluster: start raft node: %w", err)
	}
	return nil
}

// WaitLeader blocks until this node has won its election and become leader, or
// ctx is cancelled. A single-node cluster elects itself within one election
// timeout (~150–300ms on the real clock); the server calls this at startup so a
// client's first write never races the election into a spurious NOTLEADER.
func (n *Node) WaitLeader(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if n.raft.Status().Role == raft.Leader {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster: waiting for leadership: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stop drains and shuts down the Raft node.
func (n *Node) Stop() error {
	if err := n.raft.Stop(); err != nil {
		return fmt.Errorf("cluster: stop raft node: %w", err)
	}
	return nil
}

// Propose replicates a mutating command and returns the reply produced when it
// is applied. It encodes argv, proposes it, and — because Propose blocks until
// the entry is applied (SC3) — collects the reply the StateMachine stashed
// under the returned log index. A propose error (leadership lost, node stopped)
// surfaces to the caller.
func (n *Node) Propose(ctx context.Context, argv [][]byte) (resp.Value, error) {
	idx, _, err := n.raft.Propose(ctx, Encode(argv))
	if err != nil {
		return resp.Value{}, fmt.Errorf("cluster: propose: %w", err)
	}
	reply, ok := n.sm.TakeResult(idx)
	if !ok {
		// Propose returned success, so Apply ran for idx and stashed a reply.
		// A miss would mean an ordering bug in the reply-capture contract.
		return resp.Value{}, fmt.Errorf("cluster: no reply captured for applied index %d", idx)
	}
	return reply, nil
}
