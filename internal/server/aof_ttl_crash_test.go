package server

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// TestAOF_CrashInjection_TTL is the M4 milestone-owned risk test: the
// canonical PXAT / PEXPIREAT / PERSIST encoding (ADR-0004) must
// round-trip every TTL state across an unclean shutdown under
// FsyncAlways. Specifically:
//
//   - A key with a long TTL must come back with that TTL roughly intact
//     (PXAT is absolute, so the only drift is the kill-to-restart
//     window, NOT the entire downtime).
//   - A key whose absolute deadline has elapsed during downtime must be
//     absent on read (lazy expiry catches it on first Get; sweeper would
//     also reap it).
//   - A PERSIST'd key must have no TTL after restart — the explicit
//     PERSIST record must override the earlier SET ... PXAT.
//
// Reuses the M3 self-re-exec / SIGKILL machinery (startChild +
// runChildServer in aof_crash_test.go).
func TestAOF_CrashInjection_TTL(t *testing.T) {
	if testing.Short() {
		t.Skip("crash injection forks a subprocess; skipped under -short")
	}

	dir := t.TempDir()
	child, addr := startChild(t, dir, "TestAOF_CrashInjection_TTL")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		_ = child.Process.Kill()
		t.Fatalf("dial child: %v", err)
	}
	rdr := resp.NewReader(c)
	wtr := resp.NewWriter(c)

	// Cover each AOF-v2 record shape:
	//   long:  SET ... EX 60     → canonical SET k v PXAT <future>
	//   short: SET ... PX 150    → canonical SET k v PXAT <near-future>
	//                              (expected to expire during downtime)
	//   moved: SET + EXPIRE 30   → canonical PEXPIREAT <future>
	//   bare:  SET + EXPIRE + PERSIST → canonical PERSIST k overrides
	//                                   the earlier PEXPIREAT
	cmds := [][]string{
		{"SET", "long", "L", "EX", "60"},
		{"SET", "short", "S", "PX", "150"},
		{"SET", "moved", "M"},
		{"EXPIRE", "moved", "30"},
		{"SET", "bare", "B"},
		{"EXPIRE", "bare", "30"},
		{"PERSIST", "bare"},
	}
	for _, cmd := range cmds {
		if err := writeArgs(wtr, cmd); err != nil {
			t.Fatalf("send %v: %v", cmd, err)
		}
		v, err := rdr.ReadFrame()
		if err != nil {
			t.Fatalf("read reply for %v: %v", cmd, err)
		}
		if v.Kind == resp.KindError {
			t.Fatalf("%v → %s", cmd, v.Str)
		}
	}

	// Allow `short` (150ms TTL) to elapse before kill.
	time.Sleep(250 * time.Millisecond)

	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	_ = c.Close()
	_ = child.Wait()

	// Restart in-process against the same dir.
	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s2)
	defer func() {
		cancel()
		<-errCh
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	// long: most of the 60s survives (small drift OK).
	writeCmd(t, w2, "TTL", "long")
	got := readReply(t, r2)
	if got.Kind != resp.KindInteger || got.Int < 50 || got.Int > 60 {
		t.Errorf("TTL long = %+v, want 50..60", got)
	}
	writeCmd(t, w2, "GET", "long")
	expectBulk(t, r2, "L")

	// short: absolute deadline already passed → gone.
	writeCmd(t, w2, "GET", "short")
	expectNullBulk(t, r2)
	writeCmd(t, w2, "TTL", "short")
	expectInt(t, r2, -2)

	// moved: EXPIRE-set 30s TTL survived.
	writeCmd(t, w2, "TTL", "moved")
	got = readReply(t, r2)
	if got.Kind != resp.KindInteger || got.Int < 20 || got.Int > 30 {
		t.Errorf("TTL moved = %+v, want 20..30", got)
	}
	writeCmd(t, w2, "GET", "moved")
	expectBulk(t, r2, "M")

	// bare: PERSIST cleared the TTL.
	writeCmd(t, w2, "TTL", "bare")
	expectInt(t, r2, -1)
	writeCmd(t, w2, "GET", "bare")
	expectBulk(t, r2, "B")
}

func writeArgs(w *resp.Writer, args []string) error {
	elems := make([]resp.Value, len(args))
	for i, a := range args {
		elems[i] = resp.Bulk([]byte(a))
	}
	if err := w.WriteFrame(resp.Array(elems...)); err != nil {
		return err
	}
	return w.Flush()
}

// TestAOF_V1FileReplaysOnV2Binary pins the backwards-compat contract:
// a v1 AOF file (header byte 0x01, written by a pre-M4 binary) must
// replay cleanly on the M4+ binary. The file is hand-crafted so the
// test does not depend on having an actual M3 binary in the path.
func TestAOF_V1FileReplaysOnV2Binary(t *testing.T) {
	dir := t.TempDir()
	if err := writeHandCraftedV1AOF(dir, [][]string{
		{"SET", "k1", "v1"},
		{"SET", "k2", "v2"},
		{"DEL", "k1"},
	}); err != nil {
		t.Fatalf("write v1 file: %v", err)
	}

	s := setupServerWithAOF(t, dir, aof.FsyncEverysec)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
		_ = s.Close()
	}()
	c, r, w := dial(t, s.Addr())
	defer c.Close()

	writeCmd(t, w, "GET", "k1")
	expectNullBulk(t, r)
	writeCmd(t, w, "GET", "k2")
	expectBulk(t, r, "v2")
	writeCmd(t, w, "DBSIZE")
	expectInt(t, r, 1)
}

// writeHandCraftedV1AOF emits a valid v1 AOF (header byte 0x01) with
// the given commands encoded as RESP arrays — the exact bytes a pre-M4
// binary would have written.
func writeHandCraftedV1AOF(dir string, cmds [][]string) error {
	f, err := os.OpenFile(filepath.Join(dir, aof.Filename),
		os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := make([]byte, aof.HeaderLen)
	copy(hdr, aof.Magic[:])
	hdr[aof.HeaderLen-1] = aof.Version1
	if _, err := f.Write(hdr); err != nil {
		return err
	}

	w := resp.NewWriter(f)
	for _, cmd := range cmds {
		elems := make([]resp.Value, len(cmd))
		for i, p := range cmd {
			elems[i] = resp.Bulk([]byte(p))
		}
		if err := w.WriteFrame(resp.Array(elems...)); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}
