package cluster

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/prajwalmahajan101/toyraft/pkg/raft"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// storeApply is a minimal, deterministic apply router over a real store — a
// stand-in for the server's dispatch used to exercise the StateMachine and the
// snapshot serializer without pulling in the server package. It covers exactly
// the commands the tests below propose.
func storeApply(s *store.Store) ApplyFunc {
	return func(argv [][]byte) resp.Value {
		switch string(argv[0]) {
		case "SET":
			s.Set(string(argv[1]), argv[2], store.SetOpts{})
			return resp.OK()
		case "INCR":
			n, err := s.Incr(string(argv[1]))
			if err != nil {
				return resp.Error("ERR " + err.Error())
			}
			return resp.Int(n)
		case "RPUSH":
			n, _ := s.RPush(string(argv[1]), argv[2:]...)
			return resp.Int(int64(n))
		case "HSET":
			n, _ := s.HSet(string(argv[1]), argv[2:]...)
			return resp.Int(int64(n))
		default:
			return resp.Error("ERR unhandled " + string(argv[0]))
		}
	}
}

func applyStream(sm *StateMachine, stream [][][]byte) []resp.Value {
	replies := make([]resp.Value, len(stream))
	for i, argv := range stream {
		idx := raft.Index(i + 1)
		if _, err := sm.Apply(raft.Entry{Index: idx, Data: Encode(argv)}); err != nil {
			panic(err)
		}
		reply, ok := sm.TakeResult(idx)
		if !ok {
			panic(fmt.Sprintf("no result stashed for index %d", idx))
		}
		replies[i] = reply
	}
	return replies
}

func TestApplyStashesAndTakesResult(t *testing.T) {
	s := store.New()
	sm := NewStateMachine(storeApply(s), nil)

	res, err := sm.Apply(raft.Entry{Index: 7, Data: Encode([][]byte{[]byte("INCR"), []byte("c")})})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := res.(resp.Value); got.Kind != resp.KindInteger || got.Int != 1 {
		t.Fatalf("Apply returned %+v, want integer 1", got)
	}
	reply, ok := sm.TakeResult(7)
	if !ok || reply.Int != 1 {
		t.Fatalf("TakeResult(7) = %+v, %v; want integer 1, true", reply, ok)
	}
	// Second take is empty — the stash is consumed.
	if _, ok := sm.TakeResult(7); ok {
		t.Fatal("TakeResult(7) second call should be empty")
	}
}

func TestApplyRejectsCorruptEntry(t *testing.T) {
	sm := NewStateMachine(storeApply(store.New()), nil)
	if _, err := sm.Apply(raft.Entry{Index: 1, Data: []byte{0xFF, 0x00}}); err == nil {
		t.Fatal("Apply of a corrupt (undecodable) entry must return an error")
	}
}

func TestApplyErrorReplyIsNotRaftError(t *testing.T) {
	sm := NewStateMachine(storeApply(store.New()), nil)
	// INCR on a value that is not an integer → application error reply, but the
	// entry is still applied (nil raft error) and the reply is stashed.
	_ = applyStream(sm, [][][]byte{{[]byte("SET"), []byte("k"), []byte("notint")}})
	res, err := sm.Apply(raft.Entry{Index: 2, Data: Encode([][]byte{[]byte("INCR"), []byte("k")})})
	if err != nil {
		t.Fatalf("Apply of an app-error command must return nil raft error, got %v", err)
	}
	if got := res.(resp.Value); got.Kind != resp.KindError {
		t.Fatalf("expected an error reply, got %+v", got)
	}
}

// TestApplyDeterminism is the M18 determinism check: the same committed stream
// applied to two independent state machines yields byte-identical store state.
func TestApplyDeterminism(t *testing.T) {
	stream := [][][]byte{
		{[]byte("SET"), []byte("a"), []byte("1")},
		{[]byte("INCR"), []byte("ctr")},
		{[]byte("INCR"), []byte("ctr")},
		{[]byte("RPUSH"), []byte("list"), []byte("x"), []byte("y"), []byte("z")},
		{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")},
		{[]byte("SET"), []byte("a"), []byte("overwritten")},
	}

	s1, s2 := store.New(), store.New()
	r1 := applyStream(NewStateMachine(storeApply(s1), nil), stream)
	r2 := applyStream(NewStateMachine(storeApply(s2), nil), stream)

	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("replies diverged:\n r1=%+v\n r2=%+v", r1, r2)
	}
	if !snapshotsEqual(s1.Snapshot(), s2.Snapshot()) {
		t.Fatal("store snapshots diverged after applying the same stream twice")
	}
}

func TestSnapshotRestoreUnsupportedInV1(t *testing.T) {
	sm := NewStateMachine(storeApply(store.New()), nil)
	if _, _, err := sm.Snapshot(); err != raft.ErrSnapshotUnsupported {
		t.Fatalf("Snapshot() = %v, want ErrSnapshotUnsupported", err)
	}
	if err := sm.Restore(nil); err != raft.ErrSnapshotUnsupported {
		t.Fatalf("Restore() = %v, want ErrSnapshotUnsupported", err)
	}
}

// snapshotsEqual compares two store snapshots independent of entry order.
func snapshotsEqual(a, b []store.SnapshotEntry) bool {
	if len(a) != len(b) {
		return false
	}
	index := func(es []store.SnapshotEntry) map[string]*store.SnapshotEntry {
		m := make(map[string]*store.SnapshotEntry, len(es))
		for i := range es {
			m[es[i].Key] = &es[i]
		}
		return m
	}
	ma, mb := index(a), index(b)
	for k := range ma {
		eb, ok := mb[k]
		if !ok {
			return false
		}
		ca, cb := *ma[k], *eb
		ca.ExpireAt, cb.ExpireAt = ca.ExpireAt.UTC(), cb.ExpireAt.UTC()
		if !reflect.DeepEqual(ca, cb) {
			return false
		}
	}
	return true
}
