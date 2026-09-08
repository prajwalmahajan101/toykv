package server

import (
	"strconv"
	"time"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// cmdWait implements WAIT numreplicas timeout_ms — Redis-faithful durability
// acknowledgement. It blocks until at least numreplicas followers have
// acknowledged (MatchIndex >= CommitIndex at call time), or the timeout
// expires, and returns the actual count.
//
// On a follower: NOTLEADER redirect (MatchIndex is nil on followers).
// On standalone or single-node: returns 0 immediately (no followers).
// Timeout = 0: wait indefinitely (nil channel, never fires in select).
func cmdWait(s *Server, cs *connState, argv [][]byte) resp.Value {
	n, err := strconv.Atoi(string(argv[1]))
	if err != nil || n < 0 {
		return resp.Error("ERR value is not an integer or out of range")
	}
	timeoutMs, err := strconv.ParseInt(string(argv[2]), 10, 64)
	if err != nil || timeoutMs < 0 {
		return resp.Error("ERR timeout is not an integer or out of range")
	}

	if !s.replicated {
		return resp.Int(0)
	}

	st := s.cluster.Status()
	if st.Role != raft.Leader {
		return s.notLeaderReply(st.LeaderHint)
	}

	target := st.CommitIndex
	// ToyRaft includes the leader's own ID in MatchIndex (matchIndex[self] =
	// leader's locally-replicated index, used for quorum counting). Redis WAIT
	// counts only remote replicas, so exclude self. The leader is always ahead
	// of its own CommitIndex, so excluding it never under-counts.
	selfID := s.cluster.NodeID()

	// Fast path: already satisfied (or asking for zero replicas).
	if count := countReplicas(st.MatchIndex, target, selfID); count >= n {
		return resp.Int(int64(count))
	}

	// Slow path: poll until satisfied or deadline.
	var deadline <-chan time.Time // nil channel never fires → wait indefinitely
	if timeoutMs > 0 {
		t := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		defer t.Stop()
		deadline = t.C
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			st = s.cluster.Status()
			count := countReplicas(st.MatchIndex, target, selfID)
			if count >= n {
				return resp.Int(int64(count))
			}
		case <-deadline:
			st = s.cluster.Status()
			return resp.Int(int64(countReplicas(st.MatchIndex, target, selfID)))
		case <-cs.context().Done():
			return resp.Error("ERR client disconnected")
		}
	}
}

// countReplicas counts how many remote replicas in matchIndex have >= target.
// selfID is excluded: ToyRaft includes the leader's own ID in MatchIndex for
// quorum accounting, but Redis WAIT counts only remote followers.
func countReplicas(matchIndex map[raft.NodeID]raft.Index, target raft.Index, selfID raft.NodeID) int {
	count := 0
	for id, idx := range matchIndex {
		if id != selfID && idx >= target {
			count++
		}
	}
	return count
}
