package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestRewrite_Restart_Roundtrip is the optional cross-milestone defence-in-depth
// check called out in ROADMAP M8: write a mixed workload, trigger BGREWRITEAOF,
// stop the server cleanly, restart against the same data directory, and verify
// every key survives. The exhaustive crash matrix already lives in M3/M5; this
// is a single-process happy-path smoke against the shipped binary.
func TestRewrite_Restart_Roundtrip(t *testing.T) {
	// Use a stable data dir so we can restart against it. t.TempDir handles cleanup.
	dir := t.TempDir()
	// Use everysec for a representative real-world fsync; the test workload is
	// small enough that the extra fsync cost is unnoticeable.
	s1 := StartServer(t, ServerOpts{Dir: dir, AppendFsync: "everysec"})
	c1 := newClient(s1.Addr)
	ctx := context.Background()

	const n = 50
	// Half persistent, half with a long TTL so PERSIST + EXPIRE both
	// round-trip through the rewrite.
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		val := fmt.Sprintf("v%03d", i)
		if i%2 == 0 {
			if err := c1.Set(ctx, key, val, 0).Err(); err != nil {
				t.Fatalf("SET %s: %v", key, err)
			}
		} else {
			if err := c1.Set(ctx, key, val, time.Hour).Err(); err != nil {
				t.Fatalf("SET %s EX 3600: %v", key, err)
			}
		}
	}

	if err := c1.Do(ctx, "BGREWRITEAOF").Err(); err != nil {
		t.Fatalf("BGREWRITEAOF: %v", err)
	}
	// Rewriter runs async; poll DBSIZE as a liveness signal and add a small
	// settle window so the rewrite has time to swap files.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := c1.DBSize(ctx).Result(); got == n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	_ = c1.Close()
	s1.Stop()

	// Restart against the same directory and verify state.
	s2 := StartServer(t, ServerOpts{Dir: dir, AppendFsync: "everysec"})
	c2 := newClient(s2.Addr)
	defer func() { _ = c2.Close() }()

	if got, err := c2.DBSize(ctx).Result(); err != nil || got != n {
		t.Fatalf("DBSIZE after restart: got %d err %v", got, err)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		want := fmt.Sprintf("v%03d", i)
		got, err := c2.Get(ctx, key).Result()
		if err == redis.Nil {
			t.Fatalf("GET %s after restart: nil", key)
		}
		if err != nil {
			t.Fatalf("GET %s after restart: %v", key, err)
		}
		if got != want {
			t.Fatalf("GET %s: got %q want %q", key, got, want)
		}
		// Odd indices had a TTL; it should survive the restart.
		if i%2 == 1 {
			if d, _ := c2.TTL(ctx, key).Result(); d <= 0 {
				t.Fatalf("TTL %s after restart: %v (want positive)", key, d)
			}
		}
	}
}
