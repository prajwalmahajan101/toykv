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

	// Fast path: already satisfied (or asking for zero replicas).
	if count := countReplicas(st.MatchIndex, target); count >= n {
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
			count := countReplicas(st.MatchIndex, target)
			if count >= n {
				return resp.Int(int64(count))
			}
		case <-deadline:
			st = s.cluster.Status()
			return resp.Int(int64(countReplicas(st.MatchIndex, target)))
		case <-cs.context().Done():
			return resp.Error("ERR client disconnected")
		}
	}
}

// countReplicas counts how many entries in matchIndex are >= target. A nil
// map (follower path, should not reach here normally) returns 0.
func countReplicas(matchIndex map[raft.NodeID]raft.Index, target raft.Index) int {
	count := 0
	for _, idx := range matchIndex {
		if idx >= target {
			count++
		}
	}
	return count
}
