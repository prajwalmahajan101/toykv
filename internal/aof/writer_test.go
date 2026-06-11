package aof

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpen_CreatesHeaderOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != HeaderLen {
		t.Fatalf("file size = %d, want %d (header only)", len(got), HeaderLen)
	}
	if !bytes.Equal(got[:len(Magic)], Magic[:]) {
		t.Fatalf("magic mismatch: got %x", got[:len(Magic)])
	}
	if got[HeaderLen-1] != Version1 {
		t.Fatalf("version = %x, want %x", got[HeaderLen-1], Version1)
	}
}

func TestOpen_RejectsBadHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("NOTTHIS\x01"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := Open(dir, FsyncAlways)
	if !errors.Is(err, ErrBadHeader) {
		t.Fatalf("got %v, want ErrBadHeader", err)
	}
}

func TestAppend_WritesValidRESPRecord(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Append([][]byte{[]byte("SET"), []byte("k"), []byte("v")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n"
	if string(got[HeaderLen:]) != want {
		t.Fatalf("body = %q, want %q", got[HeaderLen:], want)
	}
}

func TestAppend_ConcurrentMutexSerialised(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncAlways)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	const goroutines = 16
	const iters = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if err := w.Append([][]byte{[]byte("SET"), []byte("k"), []byte("v")}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// Concurrent appends must not interleave bytes — replay would
	// surface as a parse error otherwise.
	stats, err := Replay(dir, func([][]byte) error { return nil })
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if want := goroutines * iters; stats.Records != want {
		t.Fatalf("records = %d, want %d", stats.Records, want)
	}
}

func TestAppend_Everysec_FsyncsViaTicker(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, FsyncEverysec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	if err := w.Append([][]byte{[]byte("SET"), []byte("k"), []byte("v")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// We can't observe fsync directly, but we can observe that the
	// dirty flag clears after a tick. Allow generous slack.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		clean := !w.dirty
		w.mu.Unlock()
		if clean {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("everysec ticker did not clear the dirty flag within 3s")
}

func TestParsePolicy(t *testing.T) {
	cases := []struct {
		in   string
		want FsyncPolicy
		ok   bool
	}{
		{"always", FsyncAlways, true},
		{"everysec", FsyncEverysec, true},
		{"no", FsyncNever, true},
		{"sometimes", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := ParsePolicy(c.in)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("ParsePolicy(%q) = (%v, %v), want (%v, nil)", c.in, got, err, c.want)
			}
		} else if err == nil {
			t.Errorf("ParsePolicy(%q) = (%v, nil), want error", c.in, got)
		}
	}
}

func TestPolicy_StringRoundTrip(t *testing.T) {
	for _, p := range []FsyncPolicy{FsyncAlways, FsyncEverysec, FsyncNever} {
		got, err := ParsePolicy(p.String())
		if err != nil || got != p {
			t.Errorf("round-trip %v: ParsePolicy(%q) = (%v, %v)", p, p.String(), got, err)
		}
	}
}
