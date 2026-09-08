package server

import (
	"fmt"
	"net"
	"strings"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/cluster"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// cmdInfo implements INFO [section] — a Redis-faithful server-introspection
// blob of "# Section\nkey:value\n" lines. It is returned as a verbatim
// string on RESP3 and auto-downgraded to a bulk string on RESP2 (the
// resp writer handles the downgrade), so go-redis .Info() and redis-cli
// info both parse it on either protocol. Read-only.
//
// With no argument the default section set is returned; a single section
// name (case-insensitive) filters to that section, and the meta-sections
// "default" / "all" / "everything" return everything. An unrecognised
// section yields an empty body, matching Redis.
func cmdInfo(s *Server, _ *connState, argv [][]byte) resp.Value {
	section := ""
	if len(argv) == 2 {
		section = strings.ToLower(string(argv[1]))
	}

	var b strings.Builder
	want := func(name string) bool {
		switch section {
		case "", "default", "all", "everything":
			return true
		default:
			return section == name
		}
	}

	if want("server") {
		fmt.Fprintf(&b, "# Server\r\n")
		fmt.Fprintf(&b, "redis_version:%s\r\n", serverVersion)
		fmt.Fprintf(&b, "tcp_port:%s\r\n", s.tcpPort())
		uptime := max(int64(s.now().Sub(s.startTime).Seconds()), 0)
		fmt.Fprintf(&b, "uptime_in_seconds:%d\r\n", uptime)
		fmt.Fprintf(&b, "uptime_in_days:%d\r\n", uptime/86400)
		b.WriteString("\r\n")
	}

	if want("clients") {
		fmt.Fprintf(&b, "# Clients\r\n")
		fmt.Fprintf(&b, "connected_clients:%d\r\n", s.clientCount.Load())
		b.WriteString("\r\n")
	}

	if want("persistence") {
		aofEnabled := 0
		var aofSize int64
		if s.aof != nil {
			aofEnabled = 1
			if n, err := s.aof.Size(); err == nil {
				aofSize = n
			}
		}
		s.rewriteMu.Lock()
		rewriteInProgress := 0
		if s.rewriteInFlight {
			rewriteInProgress = 1
		}
		s.rewriteMu.Unlock()

		fmt.Fprintf(&b, "# Persistence\r\n")
		fmt.Fprintf(&b, "loading:0\r\n")
		fmt.Fprintf(&b, "aof_enabled:%d\r\n", aofEnabled)
		fmt.Fprintf(&b, "appendfsync:%s\r\n", s.cfg.FsyncPolicy.String())
		fmt.Fprintf(&b, "aof_current_size:%d\r\n", aofSize)
		fmt.Fprintf(&b, "aof_rewrite_in_progress:%d\r\n", rewriteInProgress)
		b.WriteString("\r\n")
	}

	if want("stats") {
		fmt.Fprintf(&b, "# Stats\r\n")
		fmt.Fprintf(&b, "aof_replay_records:%d\r\n", s.replayStats.Records)
		fmt.Fprintf(&b, "aof_replay_bytes:%d\r\n", s.replayStats.Bytes)
		b.WriteString("\r\n")
	}

	if want("keyspace") {
		fmt.Fprintf(&b, "# Keyspace\r\n")
		if n := s.store.DBSize(); n > 0 {
			fmt.Fprintf(&b, "db0:keys=%d\r\n", n)
		}
		b.WriteString("\r\n")
	}

	if s.replicated && want("replication") {
		s.infoReplication(&b)
	}

	return resp.Verbatim("txt", []byte(b.String()))
}

// infoReplication appends the # Replication section to b. Only called when
// s.replicated is true. Fields mirror Redis's INFO replication output so that
// redis-cli and go-redis parse them correctly.
func (s *Server) infoReplication(b *strings.Builder) {
	st := s.cluster.Status()

	role := "slave"
	if st.Role == raft.Leader {
		role = "master"
	}

	fmt.Fprintf(b, "# Replication\r\n")
	fmt.Fprintf(b, "role:%s\r\n", role)

	if st.Role == raft.Leader {
		// Per-replica info is only available on the leader (MatchIndex is nil on followers).
		peers := replicaPeers(s.peers, s.cfg.NodeID)
		fmt.Fprintf(b, "connected_slaves:%d\r\n", len(peers))
		for i, p := range peers {
			host, port := splitHostPort(p.ClientAddr)
			offset := uint64(st.MatchIndex[p.ID])
			lag := uint64(0)
			if uint64(st.CommitIndex) > offset {
				lag = uint64(st.CommitIndex) - offset
			}
			fmt.Fprintf(b, "slave%d:ip=%s,port=%s,state=online,offset=%d,lag=%d\r\n",
				i, host, port, offset, lag)
		}
		fmt.Fprintf(b, "master_repl_offset:%d\r\n", uint64(st.CommitIndex))
	} else {
		fmt.Fprintf(b, "connected_slaves:0\r\n")
		fmt.Fprintf(b, "master_repl_offset:%d\r\n", uint64(st.ApplyIndex))
	}

	b.WriteString("\r\n")
}

// replicaPeers returns the peers that are not this node (i.e., the replicas as
// seen from the leader). Peers without a ClientAddr are included but will show
// ip= and port= as empty strings, matching behaviour for un-migrated -peers entries.
func replicaPeers(peers []cluster.Peer, selfID string) []cluster.Peer {
	out := make([]cluster.Peer, 0, len(peers))
	for _, p := range peers {
		if string(p.ID) != selfID {
			out = append(out, p)
		}
	}
	return out
}

// splitHostPort splits a host:port string into its parts, returning empty
// strings on parse failure (e.g. a peer with no advertised client address).
func splitHostPort(addr string) (host, port string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", ""
	}
	return host, port
}

// tcpPort returns the port the server is listening on, derived from the
// bound listener (falls back to the configured Addr before bind). Returns
// "0" if no port can be determined.
func (s *Server) tcpPort() string {
	addr := s.Addr()
	if addr == "" {
		addr = s.cfg.Addr
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "0"
}
