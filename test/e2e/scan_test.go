package e2e

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/redis/go-redis/v9"
)

// scanAll drives SCAN to completion via go-redis and returns every key
// returned across all pages (with duplicates preserved).
func scanAll(ctx context.Context, t *testing.T, c *redis.Client, match string, count int64) []string {
	t.Helper()
	var got []string
	var cursor uint64
	for i := 0; ; i++ {
		if i > 1_000_000 {
			t.Fatalf("SCAN did not terminate (cursor=%d)", cursor)
		}
		keys, next, err := c.Scan(ctx, cursor, match, count).Result()
		if err != nil {
			t.Fatalf("SCAN(%d): %v", cursor, err)
		}
		got = append(got, keys...)
		cursor = next
		if cursor == 0 {
			return got
		}
	}
}

func TestScan_FullEnumeration(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	want := make([]string, 0, 50)
	for i := range 50 {
		k := fmt.Sprintf("key:%02d", i)
		want = append(want, k)
		if err := c.Set(ctx, k, "v", 0).Err(); err != nil {
			t.Fatalf("SET %s: %v", k, err)
		}
	}

	// COUNT=7 forces multiple pages; every key must appear exactly once.
	got := scanAll(ctx, t, c, "*", 7)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("SCAN returned %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SCAN key %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScan_MatchAndCount(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	for _, k := range []string{"user:1", "user:2", "user:3", "post:1", "post:2"} {
		if err := c.Set(ctx, k, "v", 0).Err(); err != nil {
			t.Fatalf("SET %s: %v", k, err)
		}
	}

	got := scanAll(ctx, t, c, "user:*", 2)
	sort.Strings(got)
	want := []string{"user:1", "user:2", "user:3"}
	if len(got) != len(want) {
		t.Fatalf("SCAN user:* = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SCAN user:* [%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScan_RESP3(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newRESP3Client(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	for i := range 10 {
		if err := c.Set(ctx, fmt.Sprintf("k%d", i), "v", 0).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}
	}
	got := scanAll(ctx, t, c, "*", 3)
	if len(got) != 10 {
		t.Fatalf("RESP3 SCAN returned %d keys, want 10", len(got))
	}
}

// TestScan_CursorGuaranteeUnderConcurrentMutation is M13's owned risk
// test. A set of "must-see" keys is created AFTER a body of lower-seq
// noise keys, so deleting noise mid-scan exercises exactly the
// index-shift hazard that a positional cursor would fail. The
// insertion-seq cursor must still return every key present for the whole
// iteration. Run with -race.
func TestScan_CursorGuaranteeUnderConcurrentMutation(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	ctx := context.Background()

	writer := newClient(s.Addr)
	defer func() { _ = writer.Close() }()

	// Low-seq noise created first — these will be mutated during the scan.
	const noise = 200
	for i := range noise {
		if err := writer.Set(ctx, fmt.Sprintf("noise:%d", i), "v", 0).Err(); err != nil {
			t.Fatalf("seed noise: %v", err)
		}
	}
	// Higher-seq must-see keys created after the noise.
	const mustCount = 100
	mustSee := make(map[string]bool, mustCount)
	for i := range mustCount {
		k := fmt.Sprintf("must:%d", i)
		mustSee[k] = true
		if err := writer.Set(ctx, k, "v", 0).Err(); err != nil {
			t.Fatalf("seed must: %v", err)
		}
	}

	// Mutator: churn the low-seq noise range while the scan runs —
	// deleting keys below the must-see seqs is the skip hazard.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := newClient(s.Addr)
		defer func() { _ = m.Close() }()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = m.Del(ctx, fmt.Sprintf("noise:%d", i%noise)).Err()
			_ = m.Set(ctx, fmt.Sprintf("churn:%d", i), "v", 0).Err()
			i++
		}
	}()

	// Full scan loop with a small COUNT to widen the concurrency window.
	scanner := newClient(s.Addr)
	defer func() { _ = scanner.Close() }()
	seen := make(map[string]bool)
	for _, k := range scanAll(ctx, t, scanner, "*", 10) {
		seen[k] = true
	}

	close(stop)
	<-done

	var missing []string
	for k := range mustSee {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("SCAN guarantee violated: %d must-see keys missed: %v", len(missing), missing)
	}
}
