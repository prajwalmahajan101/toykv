package aof

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// seedFileWithVersion writes a valid AOF containing one `SET k v`
// record under the given header version — simulating a file written by
// an older binary.
func seedFileWithVersion(t *testing.T, dir string, version byte) {
	t.Helper()
	// Header.
	buf := make([]byte, HeaderLen)
	copy(buf, Magic[:])
	buf[HeaderLen-1] = version
	// One RESP-encoded record: *3 $3 SET $1 k $1 v
	record := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n"
	if err := os.WriteFile(filepath.Join(dir, Filename), append(buf, record...), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestOpen_UpgradesOldHeaderInPlace proves the M11 invariant: after
// Open, a file that can receive v3 records carries a v3 header, so an
// older binary refuses it up-front (ErrBadVersion) instead of dying
// mid-replay on an unknown command.
func TestOpen_UpgradesOldHeaderInPlace(t *testing.T) {
	for _, ver := range []byte{Version1, Version2} {
		t.Run(map[byte]string{Version1: "v1", Version2: "v2"}[ver], func(t *testing.T) {
			dir := t.TempDir()
			seedFileWithVersion(t, dir, ver)

			w, err := Open(dir, FsyncAlways)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Close() }()

			got, err := os.ReadFile(filepath.Join(dir, Filename))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if got[HeaderLen-1] != CurrentVersion {
				t.Fatalf("header version = 0x%02x after Open, want 0x%02x", got[HeaderLen-1], CurrentVersion)
			}
			if !bytes.Equal(got[:len(Magic)], Magic[:]) {
				t.Fatalf("magic corrupted by upgrade: %x", got[:len(Magic)])
			}
			// The old record body must be untouched.
			if want := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n"; string(got[HeaderLen:]) != want {
				t.Fatalf("record body changed: %q", got[HeaderLen:])
			}
		})
	}
}

// TestOpen_UpgradedFileReplaysOldAndNewRecords appends a v3-era typed
// record to an upgraded v2 file and verifies a full replay sees both
// the pre-upgrade and post-upgrade records in order.
func TestOpen_UpgradedFileReplaysOldAndNewRecords(t *testing.T) {
	dir := t.TempDir()
	seedFileWithVersion(t, dir, Version2)

	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Append([][]byte{[]byte("LPUSH"), []byte("l"), []byte("a"), []byte("b")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var got [][]string
	stats, err := Replay(dir, func(argv [][]byte) error {
		rec := make([]string, len(argv))
		for i, a := range argv {
			rec[i] = string(a)
		}
		got = append(got, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if stats.Records != 2 {
		t.Fatalf("Records = %d, want 2", stats.Records)
	}
	want := [][]string{
		{"SET", "k", "v"},
		{"LPUSH", "l", "a", "b"},
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("record %d = %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("record %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// TestReplay_AcceptsAllSupportedVersions drives the same record file
// through each supported header version.
func TestReplay_AcceptsAllSupportedVersions(t *testing.T) {
	for _, ver := range supportedVersions {
		dir := t.TempDir()
		seedFileWithVersion(t, dir, ver)
		n := 0
		if _, err := Replay(dir, func([][]byte) error { n++; return nil }); err != nil {
			t.Fatalf("Replay(v0x%02x): %v", ver, err)
		}
		if n != 1 {
			t.Fatalf("Replay(v0x%02x) applied %d records, want 1", ver, n)
		}
	}
}
