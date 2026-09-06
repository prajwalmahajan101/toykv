package cluster

import (
	"fmt"
	"net"
	"strings"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"
)

// Peer is one member of the Raft cluster: a stable node id and the host:port
// its peer (consensus-plane) transport listens on. This is distinct from the
// client-facing -addr; the Raft transport always binds a separate port so
// consensus traffic never shares the client listener.
type Peer struct {
	ID   raft.NodeID
	Addr string // host:raftport
}

// ParsePeers turns a "-peers" spec — a comma-separated list of "id@host:port"
// entries — into the full cluster membership, self included. It is the single
// place the id@host:port grammar is defined.
//
// An empty spec yields a nil slice (the caller treats that as single-node /
// standalone). A non-empty spec must have an odd number of members (ToyRaft
// requires odd N so a quorum can never split) and no duplicate ids.
func ParsePeers(spec string) ([]Peer, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	parts := strings.Split(spec, ",")
	peers := make([]Peer, 0, len(parts))
	seen := make(map[raft.NodeID]struct{}, len(parts))
	for _, raw := range parts {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, fmt.Errorf("cluster: empty peer entry in %q (want id@host:port)", spec)
		}
		id, addr, ok := strings.Cut(entry, "@")
		if !ok {
			return nil, fmt.Errorf("cluster: malformed peer %q (want id@host:port)", entry)
		}
		id, addr = strings.TrimSpace(id), strings.TrimSpace(addr)
		if id == "" {
			return nil, fmt.Errorf("cluster: peer %q has an empty id (want id@host:port)", entry)
		}
		// Validate host:port shape now so a typo fails at startup, not mid-election.
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("cluster: peer %q has a bad address %q: %w", id, addr, err)
		}
		nid := raft.NodeID(id)
		if _, dup := seen[nid]; dup {
			return nil, fmt.Errorf("cluster: duplicate peer id %q", id)
		}
		seen[nid] = struct{}{}
		peers = append(peers, Peer{ID: nid, Addr: addr})
	}

	if len(peers)%2 == 0 {
		return nil, fmt.Errorf("cluster: need an odd number of peers, got %d (an even cluster can split quorum)", len(peers))
	}
	return peers, nil
}
