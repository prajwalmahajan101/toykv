package server

import (
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// TestAOF_CrashInjection_TypedOps is the M11 milestone-owned risk test:
// every typed mutation (LPUSH/RPUSH/LPOP/HSET/HDEL) the server
// acknowledged must survive a SIGKILL and be present after v3 replay,
// under FsyncAlways. The parent maintains a model of the expected
// state, applying each op only once its ack arrives; after the crash
// and replay the store must match the model exactly.
func TestAOF_CrashInjection_TypedOps(t *testing.T) {
	if testing.Short() {
		t.Skip("crash injection forks a subprocess; skipped under -short")
	}

	dir := t.TempDir()
	child, addr := startChild(t, dir, "TestAOF_CrashInjection_TypedOps")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		_ = child.Process.Kill()
		t.Fatalf("dial child: %v", err)
	}
	rdr := resp.NewReader(c)
	wtr := resp.NewWriter(c)

	// Model of acked state.
	lists := map[string][]string{}
	hashes := map[string]map[string]string{}

	send := func(args ...string) (resp.Value, bool) {
		elems := make([]resp.Value, len(args))
		for i, a := range args {
			elems[i] = resp.Bulk([]byte(a))
		}
		if err := wtr.WriteFrame(resp.Array(elems...)); err != nil {
			return resp.Value{}, false
		}
		if err := wtr.Flush(); err != nil {
			return resp.Value{}, false
		}
		v, err := rdr.ReadFrame()
		if err != nil {
			return resp.Value{}, false // killed before ack landed
		}
		return v, true
	}

	const total = 300
	killAt := 143 // arbitrary mid-stream boundary
	acks := 0
loop:
	for i := range total {
		lk := fmt.Sprintf("list%d", i%3)
		hk := fmt.Sprintf("hash%d", i%2)
		switch i % 5 {
		case 0, 1: // RPUSH lk e<i>
			e := fmt.Sprintf("e%03d", i)
			v, ok := send("RPUSH", lk, e)
			if !ok {
				break loop
			}
			if v.Kind == resp.KindInteger {
				lists[lk] = append(lists[lk], e)
				acks++
			}
		case 2: // LPUSH lk f<i>
			e := fmt.Sprintf("f%03d", i)
			v, ok := send("LPUSH", lk, e)
			if !ok {
				break loop
			}
			if v.Kind == resp.KindInteger {
				lists[lk] = append([]string{e}, lists[lk]...)
				acks++
			}
		case 3: // HSET hk field<i> val<i>
			f, val := fmt.Sprintf("field%03d", i), fmt.Sprintf("val%03d", i)
			v, ok := send("HSET", hk, f, val)
			if !ok {
				break loop
			}
			if v.Kind == resp.KindInteger {
				if hashes[hk] == nil {
					hashes[hk] = map[string]string{}
				}
				hashes[hk][f] = val
				acks++
			}
		case 4: // LPOP lk — sometimes a miss (empty), that's fine
			v, ok := send("LPOP", lk)
			if !ok {
				break loop
			}
			switch {
			case v.Kind == resp.KindBulkString && !v.IsNull:
				if len(lists[lk]) == 0 || lists[lk][0] != string(v.Bytes) {
					t.Fatalf("model drift: LPOP %s = %q, model head %v", lk, v.Bytes, lists[lk])
				}
				lists[lk] = lists[lk][1:]
				acks++
			case v.IsNull || v.Kind == resp.KindNull:
				// miss — no state change
			}
		}
		if i == killAt {
			if err := child.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("SIGKILL: %v", err)
			}
		}
	}
	_ = c.Close()
	_ = child.Wait()

	if acks < killAt/2 {
		t.Fatalf("only %d acks recorded before kill; test design broken", acks)
	}

	// Restart in-process; v3 replay must reconstruct the model exactly.
	s2 := setupServerWithAOF(t, dir, aof.FsyncAlways)
	_, cancel, errCh := runServer(t, s2)
	defer func() {
		cancel()
		<-errCh
		_ = s2.Close()
	}()
	c2, r2, w2 := dial(t, s2.Addr())
	defer c2.Close()

	for lk, want := range lists {
		writeCmd(t, w2, "LRANGE", lk, "0", "-1")
		expectBulkArray(t, r2, want...)
	}
	for hk, want := range hashes {
		writeCmd(t, w2, "HLEN", hk)
		expectInt(t, r2, int64(len(want)))
		for f, val := range want {
			writeCmd(t, w2, "HGET", hk, f)
			expectBulk(t, r2, val)
		}
	}
}
