package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"
	filestorage "github.com/prajwalmahajan101/toyraft/pkg/storage/file"
	"github.com/prajwalmahajan101/toyraft/pkg/storage/memory"
	httptransport "github.com/prajwalmahajan101/toyraft/pkg/transport/http"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Transport/backoff timings for the peer HTTP transport. Kept short so a dead
// or frozen peer can't stall a whole tick (and thus election progress): the
// tick loop calls Send synchronously per outbound message, and the next
// heartbeat — not this loop — is the real retry. Mirrors ToyRaft's own
// reference daemon (cmd/toyraftd).
const (
	peerSendTimeout     = 150 * time.Millisecond
	peerBackoffBase     = 20 * time.Millisecond
	peerBackoffFactor   = 2
	peerBackoffAttempts = 1
	peerShutdownTimeout = 5 * time.Second
)

// noopTransport is a raft.Transport that ships nothing. A single-node cluster
// (Peers=[self]) never fans out vote or heartbeat messages — ToyRaft's
// candidate path excludes self and its single-node fast path (ELEC-04) makes
// the self-vote an immediate quorum — so no real transport is needed.
type noopTransport struct{}

func (noopTransport) Send(context.Context, raft.Message) error                   { return nil }
func (noopTransport) Register(func(ctx context.Context, msg raft.Message) error) {}
func (noopTransport) Close() error                                               { return nil }

// Config builds a cluster Node. A single-member Peers (or nil) selects the M18
// single-node path (in-memory Raft log, no transport); more than one member
// wires the real ToyRaft HTTP transport + file-backed Raft log for M19.
type Config struct {
	NodeID   string
	Peers    []Peer // full membership incl self; len<=1 => single-node
	RaftAddr string // self's peer-transport listen bind; defaults to the self peer's Addr
	RaftDir  string // file-storage dir for the Raft log; required when multi-node
	Apply    ApplyFunc
	Snapshot func() []store.SnapshotEntry
	Logger   *slog.Logger

	// Election/heartbeat timing (multi-node only). Zero values are passed through
	// to ToyRaft, which applies its LLD defaults (150ms/300ms election, 50ms
	// heartbeat) — so leaving these unset preserves M19 behaviour exactly.
	// Operators (and the linearizability/routing harnesses) widen them to damp
	// leader churn under load or the race detector.
	ElectionTimeoutMin time.Duration
	ElectionTimeoutMax time.Duration
	HeartbeatInterval  time.Duration
}

// Node wraps a raft.Node plus the toykv StateMachine, exposing the
// Propose→reply path the server needs. On the multi-node path it also owns the
// peer transport and the file-backed Raft log, closed in Stop after the raft
// node drains.
type Node struct {
	raft    raft.Node
	sm      *StateMachine
	closers []io.Closer // transport, storage — closed (in order) after raft.Stop

	// clientAddrs maps each member's node id to its advertised client-facing
	// address (the "/host:clientport" suffix of -peers). It lets a follower turn
	// a LeaderHint node id into a dialable redirect target (M20). A member that
	// did not advertise a client addr is absent; single-node clusters, which
	// never redirect, leave this nil.
	clientAddrs map[raft.NodeID]string
}

// New builds a cluster node from cfg. The node is not started — call Start.
//
// Single-node (len(Peers) <= 1): in-memory Raft log + no-op transport, byte-
// identical to M18. Durable state is re-derived from the AOF before Start, so
// an empty Raft log on boot is expected (AOF remains the durability source).
//
// Multi-node: a file-backed Raft log (pkg/storage/file) is the replication
// source of truth, and pkg/transport/http carries consensus RPCs on a port
// distinct from the client listener. Requires RaftDir and an odd, self-including
// membership.
func New(cfg Config) (*Node, error) {
	sm := NewStateMachine(cfg.Apply, cfg.Snapshot)

	var (
		node *Node
		err  error
	)
	if len(cfg.Peers) <= 1 {
		node, err = newSingleNode(cfg, sm)
	} else {
		node, err = newMultiNode(cfg, sm)
	}
	if err != nil {
		return nil, err
	}
	node.clientAddrs = clientAddrMap(cfg.Peers)
	return node, nil
}

// clientAddrMap indexes advertised client addresses by node id. Members without
// a "/host:clientport" suffix are omitted, so a lookup miss means "not an
// auto-redirect target". Returns nil for a single-member (or nil) membership.
func clientAddrMap(peers []Peer) map[raft.NodeID]string {
	if len(peers) <= 1 {
		return nil
	}
	m := make(map[raft.NodeID]string, len(peers))
	for _, p := range peers {
		if p.ClientAddr != "" {
			m[p.ID] = p.ClientAddr
		}
	}
	return m
}

func newSingleNode(cfg Config, sm *StateMachine) (*Node, error) {
	id := raft.NodeID(cfg.NodeID)
	rn, err := raft.New(raft.Config{
		NodeID:       id,
		Peers:        []raft.NodeID{id}, // self is the whole cluster
		Storage:      memory.New(),      // in-memory Raft log; AOF is the durability source
		Transport:    noopTransport{},
		StateMachine: sm,
		Logger:       cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: build raft node: %w", err)
	}
	return &Node{raft: rn, sm: sm}, nil
}

func newMultiNode(cfg Config, sm *StateMachine) (*Node, error) {
	selfID := raft.NodeID(cfg.NodeID)

	allIDs := make([]raft.NodeID, 0, len(cfg.Peers))
	peerURLs := make(map[raft.NodeID]string, len(cfg.Peers)-1)
	selfAddr := cfg.RaftAddr
	selfFound := false
	for _, p := range cfg.Peers {
		allIDs = append(allIDs, p.ID)
		if p.ID == selfID {
			selfFound = true
			if selfAddr == "" {
				selfAddr = p.Addr
			}
			continue
		}
		peerURLs[p.ID] = "http://" + p.Addr
	}
	if !selfFound {
		return nil, fmt.Errorf("cluster: node-id %q not present in -peers", cfg.NodeID)
	}
	if cfg.RaftDir == "" {
		return nil, fmt.Errorf("cluster: -raft-dir is required for a multi-node cluster")
	}

	storage, err := filestorage.New(cfg.RaftDir)
	if err != nil {
		return nil, fmt.Errorf("cluster: open raft log %q: %w", cfg.RaftDir, err)
	}

	// Clock is left unset — pkg/transport/http defaults it to the real clock
	// (ToyRaft v1.0.0-rc.2; internal/clock is not externally constructible).
	transport, err := httptransport.New(httptransport.Config{
		NodeID:      selfID,
		ListenAddr:  selfAddr,
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
		_ = storage.Close()
		return nil, fmt.Errorf("cluster: build peer transport on %q: %w", selfAddr, err)
	}

	rn, err := raft.New(raft.Config{
		NodeID:             selfID,
		Peers:              allIDs,
		Storage:            storage,
		Transport:          transport,
		StateMachine:       sm,
		Logger:             cfg.Logger,
		ElectionTimeoutMin: cfg.ElectionTimeoutMin,
		ElectionTimeoutMax: cfg.ElectionTimeoutMax,
		HeartbeatInterval:  cfg.HeartbeatInterval,
	})
	if err != nil {
		_ = transport.Close()
		_ = storage.Close()
		return nil, fmt.Errorf("cluster: build raft node: %w", err)
	}
	// Close order after raft drains: transport (stop serving peers) then storage.
	return &Node{raft: rn, sm: sm, closers: []io.Closer{transport, storage}}, nil
}

// Start brings the Raft node up. A single-node cluster wins its first election
// immediately; a multi-node cluster begins campaigning/heartbeating.
func (n *Node) Start(ctx context.Context) error {
	if err := n.raft.Start(ctx); err != nil {
		return fmt.Errorf("cluster: start raft node: %w", err)
	}
	return nil
}

// WaitLeader blocks until the cluster has a leader — this node or a peer — or
// ctx is cancelled. The server calls this at startup so a client's first
// command never races the election. A single-node cluster elects itself within
// one election timeout; a follower unblocks as soon as it learns a leader
// (LeaderHint), then serves reads locally and redirects writes (M20).
func (n *Node) WaitLeader(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		st := n.raft.Status()
		if st.Role == raft.Leader || st.LeaderHint != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster: waiting for leadership: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Role reports this node's current Raft role (Follower/Candidate/Leader).
func (n *Node) Role() raft.Role { return n.raft.Status().Role }

// LeaderHint returns the best-known current leader id, or "" if unknown.
func (n *Node) LeaderHint() raft.NodeID { return n.raft.LeaderHint() }

// LeaderClientAddr resolves the current leader to its advertised client-facing
// address for a NOTLEADER redirect (M20). It returns "" when there is no known
// leader or the leader did not advertise a client address (an un-migrated
// -peers entry) — the caller then falls back to a non-dialable, operator-
// readable hint rather than a redirect.
func (n *Node) LeaderClientAddr() string {
	hint := n.raft.LeaderHint()
	if hint == "" {
		return ""
	}
	return n.clientAddrs[hint]
}

// Status exposes the full ToyRaft status (role, term, indices, per-replica
// match) for cluster observability (M21).
func (n *Node) Status() raft.Status { return n.raft.Status() }

// Stop drains and shuts down the Raft node, then closes the peer transport and
// Raft-log storage (multi-node only). The first error is returned; remaining
// closers are still attempted.
func (n *Node) Stop() error {
	err := n.raft.Stop()
	if err != nil {
		err = fmt.Errorf("cluster: stop raft node: %w", err)
	}
	for _, c := range n.closers {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("cluster: close: %w", cerr)
		}
	}
	return err
}

// Propose replicates a mutating command and returns the reply produced when it
// is applied. It encodes argv, proposes it, and — because Propose blocks until
// the entry is applied (SC3) — collects the reply the StateMachine stashed
// under the returned log index. A propose error (not leader, leadership lost,
// node stopped) surfaces to the caller.
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
