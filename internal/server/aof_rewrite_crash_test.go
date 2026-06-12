package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// TestAOF_CrashInjection_DuringRewrite is the M5 milestone-owned crash
// test (ADR-0005). It exercises three SIGKILL timing windows during a
// BGREWRITEAOF:
//
//  1. Before the rename: parent issues BGREWRITEAOF then SIGKILLs after
//     a brief sleep — likely to land before, during, or just after the
//     rename. Across many seeds at least one run lands pre-rename.
//  2. After the rename, while live appends are still flowing through
//     the side buffer.
//  3. After the rewrite completes, during fresh post-swap appends.
//
// In all cases the invariants are: (a) exactly one canonical AOF on
// disk, (b) no .tmp survives a clean restart, (c) every acked SET is
// recoverable via GET. Under FsyncAlways with the dual-write design,
// (c) holds even for writes that landed during the rewrite — see
// ADR-0005's crash-invariant table.
func TestAOF_CrashInjection_DuringRewrite(t *testing.T) {
	if testing.Short() {
		t.Skip("crash injection forks a subprocess; skipped under -short")
	}

	scenarios := []struct {
		name     string
		killWait time.Duration // delay after BGREWRITEAOF before SIGKILL
	}{
		{"early-kill", 0},                     // most likely pre-rename
		{"mid-kill", 5 * time.Millisecond},    // likely post-rename
		{"late-kill", 100 * time.Millisecond}, // post-rewrite, during fresh appends
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			child, addr := startChild(t, dir, "TestAOF_CrashInjection_DuringRewrite")

			c, err := net.Dial("tcp", addr)
			if err != nil {
				_ = child.Process.Kill()
				t.Fatalf("dial child: %v", err)
			}
			rdr := resp.NewReader(c)
			wtr := resp.NewWriter(c)

			// Phase 1: seed with churn so the rewrite has work to do.
			const seed = 80
			acked := make(map[string]string, seed*2)
			for i := 0; i < seed; i++ {
				k := fmt.Sprintf("seed%03d", i)
				v := fmt.Sprintf("v%03d", i)
				ackSet(t, rdr, wtr, acked, k, v)
			}

			// Phase 2: trigger BGREWRITEAOF.
			if err := wtr.WriteFrame(resp.Array(resp.Bulk([]byte("BGREWRITEAOF")))); err != nil {
				t.Fatalf("write BGREWRITEAOF: %v", err)
			}
			if err := wtr.Flush(); err != nil {
				t.Fatalf("flush BGREWRITEAOF: %v", err)
			}
			if got, err := rdr.ReadFrame(); err != nil || got.Kind != resp.KindSimpleString {
				t.Fatalf("BGREWRITEAOF reply = %+v err=%v", got, err)
			}

			// Phase 3: keep writing during the rewrite so live appends hit
			// the side buffer / new file.
			liveDone := make(chan struct{})
			killed := make(chan struct{})
			go func() {
				defer close(liveDone)
				for i := 0; i < 200; i++ {
					select {
					case <-killed:
						return
					default:
					}
					k := fmt.Sprintf("live%03d", i)
					v := fmt.Sprintf("vL%03d", i)
					// We swallow errors past the kill — the conn breaks.
					if !tryAckSet(rdr, wtr, acked, k, v) {
						return
					}
				}
			}()

			// Phase 4: SIGKILL at the scheduled offset.
			time.Sleep(sc.killWait)
			if err := child.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("SIGKILL: %v", err)
			}
			close(killed)
			<-liveDone
			_ = c.Close()
			_ = child.Wait()

			// Invariant (a): exactly one canonical AOF.
			info, err := os.Stat(filepath.Join(dir, aof.Filename))
			if err != nil || info.Size() < int64(aof.HeaderLen) {
				t.Fatalf("canonical AOF missing or truncated: info=%+v err=%v", info, err)
			}

			// Invariant (b): restart cleans up any leftover .tmp.
			s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
			_, cancel, errCh := runServer(t, s2)
			defer func() {
				cancel()
				<-errCh
				_ = s2.Close()
			}()
			if _, err := os.Stat(filepath.Join(dir, aof.TmpFilename)); !os.IsNotExist(err) {
				t.Fatalf(".tmp survived restart: err=%v", err)
			}

			// Invariant (c): every acked SET is recoverable.
			c2, r2, w2 := dial(t, s2.Addr())
			defer c2.Close()
			missing := 0
			for k, v := range acked {
				writeCmd(t, w2, "GET", k)
				got := readReply(t, r2)
				if got.Kind != resp.KindBulkString || got.IsNull || string(got.Bytes) != v {
					missing++
					if missing <= 5 {
						t.Errorf("scenario=%s acked key %q: got %+v, want bulk %q",
							sc.name, k, got, v)
					}
				}
			}
			if missing > 0 {
				t.Fatalf("scenario=%s: %d/%d acked SETs lost across rewrite+SIGKILL",
					sc.name, missing, len(acked))
			}
		})
	}
}

// ackSet writes SET k v and records ack on +OK. Fails the test on any
// transport error — used during the pre-rewrite seed phase where we
// expect the connection to be alive.
func ackSet(t *testing.T, r *resp.Reader, w *resp.Writer, acked map[string]string, k, v string) {
	t.Helper()
	if err := w.WriteFrame(resp.Array(resp.Bulk([]byte("SET")), resp.Bulk([]byte(k)), resp.Bulk([]byte(v)))); err != nil {
		t.Fatalf("write SET: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush SET: %v", err)
	}
	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read SET reply: %v", err)
	}
	if got.Kind == resp.KindSimpleString && got.Str == "OK" {
		acked[k] = v
	}
}

// tryAckSet is the live-writer variant: returns false on any transport
// error (which is the expected outcome past the kill).
func tryAckSet(r *resp.Reader, w *resp.Writer, acked map[string]string, k, v string) bool {
	if err := w.WriteFrame(resp.Array(resp.Bulk([]byte("SET")), resp.Bulk([]byte(k)), resp.Bulk([]byte(v)))); err != nil {
		return false
	}
	if err := w.Flush(); err != nil {
		return false
	}
	got, err := r.ReadFrame()
	if err != nil {
		return false
	}
	if got.Kind == resp.KindSimpleString && got.Str == "OK" {
		acked[k] = v
	}
	return true
}
