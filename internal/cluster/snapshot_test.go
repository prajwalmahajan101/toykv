package cluster

import (
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// TestSnapshotSerializerRoundTrip is the forward-compat check (unused on the
// M18 live path): live state → SerializeSnapshot → CommandsFromSnapshot →
// replay into a fresh store rebuilds identical state. This is what a ToyRaft v2
// Snapshot/Restore will do, so the serializer is proven before it is wired.
func TestSnapshotSerializerRoundTrip(t *testing.T) {
	src := store.New()
	apply := storeApply(src)
	seed := [][][]byte{
		{[]byte("SET"), []byte("s"), []byte("hello")},
		{[]byte("RPUSH"), []byte("l"), []byte("a"), []byte("b"), []byte("c")},
		{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")},
	}
	for _, argv := range seed {
		apply(argv)
	}

	blob := SerializeSnapshot(src.Snapshot())
	cmds, err := CommandsFromSnapshot(blob)
	if err != nil {
		t.Fatalf("CommandsFromSnapshot: %v", err)
	}

	dst := store.New()
	dstApply := storeApply(dst)
	for _, argv := range cmds {
		dstApply(argv)
	}

	if !snapshotsEqual(src.Snapshot(), dst.Snapshot()) {
		t.Fatalf("restored state diverged:\n src=%+v\n dst=%+v", src.Snapshot(), dst.Snapshot())
	}
}

func TestSerializeSnapshotDeterministic(t *testing.T) {
	// Build a snapshot with several keys; map-ordering in store.Snapshot must
	// not leak into the serialized bytes (commandsFromSnapshot sorts by key).
	entries := []store.SnapshotEntry{
		{Key: "z", Type: "string", Value: []byte("1")},
		{Key: "a", Type: "string", Value: []byte("2")},
		{Key: "m", Type: "string", Value: []byte("3"), ExpireAt: time.UnixMilli(1700000000000)},
	}
	first := SerializeSnapshot(append([]store.SnapshotEntry(nil), entries...))
	// Feed a reordered copy; output must be byte-identical.
	reordered := []store.SnapshotEntry{entries[2], entries[0], entries[1]}
	if got := SerializeSnapshot(reordered); string(got) != string(first) {
		t.Fatal("SerializeSnapshot must be independent of input entry order")
	}
}

func TestCommandsFromSnapshotRejectsTruncated(t *testing.T) {
	blob := SerializeSnapshot([]store.SnapshotEntry{{Key: "k", Type: "string", Value: []byte("v")}})
	if _, err := CommandsFromSnapshot(blob[:len(blob)-2]); err == nil {
		t.Fatal("truncated snapshot body should error")
	}
	if _, err := CommandsFromSnapshot(blob[:2]); err == nil {
		t.Fatal("truncated snapshot header should error")
	}
}
