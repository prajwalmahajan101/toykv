package aof

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplay_NoFile(t *testing.T) {
	dir := t.TempDir()
	stats, err := Replay(dir, func([][]byte) error { return nil })
	if err != nil {
		t.Fatalf("Replay on missing file: %v", err)
	}
	if stats.Records != 0 {
		t.Fatalf("records = %d, want 0", stats.Records)
	}
}

func TestReplay_HeaderOnly(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stats, err := Replay(dir, func([][]byte) error { return nil })
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if stats.Records != 0 {
		t.Fatalf("records = %d, want 0", stats.Records)
	}
	if stats.Bytes != int64(HeaderLen) {
		t.Fatalf("bytes = %d, want %d", stats.Bytes, HeaderLen)
	}
}

func TestReplay_CleanRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	records := [][][]byte{
		{[]byte("SET"), []byte("a"), []byte("1")},
		{[]byte("SET"), []byte("b"), []byte("2")},
		{[]byte("DEL"), []byte("a")},
		{[]byte("INCR"), []byte("ctr")},
	}
	for _, r := range records {
		if err := w.Append(r); err != nil {
			t.Fatalf("Append %v: %v", r, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var got [][][]byte
	stats, err := Replay(dir, func(argv [][]byte) error {
		// Copy because the underlying buffer is reused.
		cp := make([][]byte, len(argv))
		for i, a := range argv {
			cp[i] = append([]byte(nil), a...)
		}
		got = append(got, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if stats.Records != len(records) {
		t.Fatalf("records = %d, want %d", stats.Records, len(records))
	}
	if len(got) != len(records) {
		t.Fatalf("got %d records replayed, want %d", len(got), len(records))
	}
	for i := range records {
		if len(got[i]) != len(records[i]) {
			t.Fatalf("record %d argv len = %d, want %d", i, len(got[i]), len(records[i]))
		}
		for j := range records[i] {
			if !bytes.Equal(got[i][j], records[i][j]) {
				t.Fatalf("record %d argv[%d] = %q, want %q", i, j, got[i][j], records[i][j])
			}
		}
	}
}

func TestReplay_BadHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("NOPETHIS"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := Replay(dir, func([][]byte) error { return nil })
	var rerr *ReplayError
	if !errors.As(err, &rerr) {
		t.Fatalf("got %v, want *ReplayError", err)
	}
	if !errors.Is(rerr, ErrBadHeader) {
		t.Fatalf("wrapped err = %v, want ErrBadHeader", rerr.Err)
	}
	if rerr.Offset != 0 {
		t.Fatalf("offset = %d, want 0", rerr.Offset)
	}
}

func TestReplay_CorruptedTail(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Append([][]byte{[]byte("SET"), []byte("a"), []byte("1")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Append a partial RESP frame (missing CRLF + body).
	f, err := os.OpenFile(filepath.Join(dir, Filename), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open for corrupt: %v", err)
	}
	if _, err := f.Write([]byte("*3\r\n$3\r\nSE")); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	_ = f.Close()

	_, err = Replay(dir, func([][]byte) error { return nil })
	var rerr *ReplayError
	if !errors.As(err, &rerr) {
		t.Fatalf("got %v, want *ReplayError", err)
	}
	if rerr.Offset <= int64(HeaderLen) {
		t.Fatalf("offset = %d, want > %d (past header + clean record)", rerr.Offset, HeaderLen)
	}
}

func TestReplay_ApplyErrorReported(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Append([][]byte{[]byte("SET"), []byte("a"), []byte("1")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	boom := errors.New("boom")
	_, err = Replay(dir, func([][]byte) error { return boom })
	var rerr *ReplayError
	if !errors.As(err, &rerr) {
		t.Fatalf("got %v, want *ReplayError", err)
	}
	if !errors.Is(rerr, boom) {
		t.Fatalf("wrapped err = %v, want boom", rerr.Err)
	}
}
