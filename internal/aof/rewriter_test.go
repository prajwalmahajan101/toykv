package aof

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// collectReplay reads dir's AOF and returns each record's argv as a
// fresh copy, so tests can assert on the post-rewrite contents.
func collectReplay(t *testing.T, dir string) [][][]byte {
	t.Helper()
	var got [][][]byte
	if _, err := Replay(dir, func(argv [][]byte) error {
		cp := make([][]byte, len(argv))
		for i, a := range argv {
			cp[i] = append([]byte(nil), a...)
		}
		got = append(got, cp)
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return got
}

func argvEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestRewriter_EmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	r := NewRewriter(w, func() []SnapshotCmd { return nil })
	if err := r.Rewrite(context.Background()); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	got := collectReplay(t, dir)
	if len(got) != 0 {
		t.Fatalf("empty rewrite produced %d records, want 0: %v", len(got), got)
	}
}

func TestRewriter_ReplacesContentsWithSnapshot(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Pre-rewrite churn: 3 records that will be replaced by 1.
	for _, argv := range [][][]byte{
		{[]byte("SET"), []byte("k"), []byte("v1")},
		{[]byte("SET"), []byte("k"), []byte("v2")},
		{[]byte("DEL"), []byte("other")},
	} {
		if err := w.Append(argv); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	r := NewRewriter(w, func() []SnapshotCmd {
		return []SnapshotCmd{{Argv: [][]byte{[]byte("SET"), []byte("k"), []byte("v2")}}}
	})
	if err := r.Rewrite(context.Background()); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	got := collectReplay(t, dir)
	want := [][][]byte{{[]byte("SET"), []byte("k"), []byte("v2")}}
	if len(got) != 1 || !argvEqual(got[0], want[0]) {
		t.Fatalf("post-rewrite contents = %v, want %v", got, want)
	}
}

func TestRewriter_PreservesPXAT(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	snap := SnapshotCmd{Argv: [][]byte{
		[]byte("SET"), []byte("k"), []byte("v"),
		[]byte("PXAT"), []byte("1700000042123"),
	}}
	r := NewRewriter(w, func() []SnapshotCmd { return []SnapshotCmd{snap} })
	if err := r.Rewrite(context.Background()); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	got := collectReplay(t, dir)
	if len(got) != 1 || !argvEqual(got[0], snap.Argv) {
		t.Fatalf("PXAT round-trip = %v, want %v", got, snap.Argv)
	}
}

func TestRewriter_ConcurrentAppendsSurviveSwap(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Snapshot starts with one key.
	snapBase := []SnapshotCmd{{Argv: [][]byte{[]byte("SET"), []byte("snap"), []byte("S")}}}

	// Pause inside the snapshot callback long enough for concurrent
	// Appends to land in the side buffer.
	released := make(chan struct{})
	snapshotEntered := make(chan struct{})
	var once sync.Once

	r := NewRewriter(w, func() []SnapshotCmd {
		once.Do(func() { close(snapshotEntered) })
		<-released
		return snapBase
	})

	rewriteErr := make(chan error, 1)
	go func() { rewriteErr <- r.Rewrite(context.Background()) }()

	<-snapshotEntered

	// Concurrent Appends — these must end up in the post-rewrite file
	// because BeginRewrite happens before the snapshot callback.
	var appendCount int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			argv := [][]byte{[]byte("SET"), []byte("live"), {byte('0' + i)}}
			if err := w.Append(argv); err != nil {
				t.Errorf("concurrent Append: %v", err)
				return
			}
			atomic.AddInt32(&appendCount, 1)
		}(i)
	}
	wg.Wait()
	close(released)

	if err := <-rewriteErr; err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if got := atomic.LoadInt32(&appendCount); got != 5 {
		t.Fatalf("appendCount = %d, want 5", got)
	}

	got := collectReplay(t, dir)
	// Expect 1 snapshot record + 5 live records.
	if len(got) != 6 {
		t.Fatalf("post-rewrite records = %d, want 6: %v", len(got), got)
	}
	if !argvEqual(got[0], snapBase[0].Argv) {
		t.Fatalf("first record = %v, want snapshot %v", got[0], snapBase[0].Argv)
	}
	// Subsequent records must be the live SETs in arbitrary order.
	seen := map[string]bool{}
	for _, rec := range got[1:] {
		if len(rec) != 3 || string(rec[0]) != "SET" || string(rec[1]) != "live" {
			t.Fatalf("unexpected record: %v", rec)
		}
		seen[string(rec[2])] = true
	}
	if len(seen) != 5 {
		t.Fatalf("distinct live values = %d, want 5", len(seen))
	}

	// One last sanity append after the swap goes straight to the new file.
	if err := w.Append([][]byte{[]byte("SET"), []byte("after"), []byte("x")}); err != nil {
		t.Fatalf("post-swap Append: %v", err)
	}
	got = collectReplay(t, dir)
	if len(got) != 7 || string(got[6][1]) != "after" {
		t.Fatalf("post-swap append missing: %v", got)
	}
}

func TestRewriter_DoubleRewriteSerialError(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	if err := w.BeginRewrite(); err != nil {
		t.Fatalf("first BeginRewrite: %v", err)
	}
	if err := w.BeginRewrite(); err == nil {
		t.Fatalf("second BeginRewrite: want error, got nil")
	}
	w.AbortRewrite()
}

func TestOpen_RemovesStaleTmp(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, TmpFilename)
	if err := os.WriteFile(tmpPath, []byte("garbage from a previous crashed rewrite"), 0o644); err != nil {
		t.Fatalf("seed tmp: %v", err)
	}

	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale .tmp survived Open: err=%v", err)
	}
}
