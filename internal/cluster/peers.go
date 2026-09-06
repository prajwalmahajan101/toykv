package cluster

import (
	"fmt"
	"net"
	"strings"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"
)

// Peer is one member of the Raft cluster: a stable node id, the host:port its
// peer (consensus-plane) transport listens on, and — optionally — the
// client-facing host:port other nodes redirect writes to (M20). Addr (the raft
// transport) is distinct from ClientAddr (the -addr listener): the Raft
// transport always binds a separate port so consensus traffic never shares the
// client listener.
type Peer struct {
	ID         raft.NodeID
	Addr       string // host:raftport (consensus plane)
	ClientAddr string // host:clientport a client can dial; "" if not advertised
}

// ParsePeers turns a "-peers" spec — a comma-separated list of
// "id@host:raftport[/host:clientport]" entries — into the full cluster
// membership, self included. It is the single place the peer grammar is defined.
//
// The optional "/host:clientport" suffix advertises the member's client-facing
// address so followers can emit a dialable NOTLEADER redirect (M20). It is
// optional purely for M19 back-compat: an entry without it parses fine but that
// member cannot be an auto-redirect target ("/" never appears in a host:port, so
// it is an unambiguous delimiter).
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
		id, rest, ok := strings.Cut(entry, "@")
		if !ok {
			return nil, fmt.Errorf("cluster: malformed peer %q (want id@host:raftport[/host:clientport])", entry)
		}
		id, rest = strings.TrimSpace(id), strings.TrimSpace(rest)
		if id == "" {
			return nil, fmt.Errorf("cluster: peer %q has an empty id (want id@host:raftport[/host:clientport])", entry)
		}
		// The optional "/host:clientport" suffix is split off before validation so
		// the raft addr is checked in isolation.
		addr, clientAddr, hasClient := strings.Cut(rest, "/")
		addr, clientAddr = strings.TrimSpace(addr), strings.TrimSpace(clientAddr)
		// Validate host:port shape now so a typo fails at startup, not mid-election.
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("cluster: peer %q has a bad raft address %q: %w", id, addr, err)
		}
		if hasClient {
			if _, _, err := net.SplitHostPort(clientAddr); err != nil {
				return nil, fmt.Errorf("cluster: peer %q has a bad client address %q: %w", id, clientAddr, err)
			}
		}
		nid := raft.NodeID(id)
		if _, dup := seen[nid]; dup {
			return nil, fmt.Errorf("cluster: duplicate peer id %q", id)
		}
		seen[nid] = struct{}{}
		peers = append(peers, Peer{ID: nid, Addr: addr, ClientAddr: clientAddr})
	}

	if len(peers)%2 == 0 {
		return nil, fmt.Errorf("cluster: need an odd number of peers, got %d (an even cluster can split quorum)", len(peers))
	}
	return peers, nil
}
