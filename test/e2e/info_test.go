package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// infoField extracts the value of a "key:value" line from an INFO blob.
// Returns "" and false when the field is absent.
func infoField(info, key string) (string, bool) {
	for line := range strings.SplitSeq(info, "\n") {
		line = strings.TrimRight(line, "\r")
		if name, val, ok := strings.Cut(line, ":"); ok && name == key {
			return val, true
		}
	}
	return "", false
}

func requireField(t *testing.T, info, key, want string) {
	t.Helper()
	got, ok := infoField(info, key)
	if !ok {
		t.Fatalf("INFO missing field %q\n---\n%s", key, info)
	}
	if got != want {
		t.Fatalf("INFO %s = %q, want %q", key, got, want)
	}
}

// TestInfo_ParsesAndReportsState checks INFO on both RESP2 and RESP3 —
// go-redis .Info() parsing the reply proves the string/verbatim form is
// wire-compatible — and that key fields track live server state.
func TestInfo_ParsesAndReportsState(t *testing.T) {
	s := StartServer(t, ServerOpts{AppendFsync: "everysec"})
	ctx := context.Background()

	for _, proto := range []struct {
		name string
		c    *redis.Client
	}{
		{"RESP2", newClient(s.Addr)},
		{"RESP3", newRESP3Client(s.Addr)},
	} {
		t.Run(proto.name, func(t *testing.T) {
			c := proto.c
			defer func() { _ = c.Close() }()

			info, err := c.Info(ctx).Result()
			if err != nil {
				t.Fatalf("INFO: %v", err)
			}
			// appendfsync reflects the -appendfsync flag.
			requireField(t, info, "appendfsync", "everysec")
			requireField(t, info, "aof_enabled", "1")
			requireField(t, info, "loading", "0")
			if _, ok := infoField(info, "uptime_in_seconds"); !ok {
				t.Fatalf("INFO missing uptime_in_seconds\n%s", info)
			}
			if _, ok := infoField(info, "redis_version"); !ok {
				t.Fatalf("INFO missing redis_version\n%s", info)
			}
		})
	}
}

// TestInfo_KeyspaceTracksDBSize checks db0:keys matches the number of
// keys after inserts. The keyspace line uses "db0:keys=N" (Redis form),
// not "db0:N".
func TestInfo_KeyspaceTracksDBSize(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	const n = 12
	for i := range n {
		if err := c.Set(ctx, fmt.Sprintf("k%d", i), "v", 0).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}
	}

	info, err := c.Info(ctx, "keyspace").Result()
	if err != nil {
		t.Fatalf("INFO keyspace: %v", err)
	}
	db0, ok := infoField(info, "db0")
	if !ok {
		t.Fatalf("INFO keyspace missing db0 line\n%s", info)
	}
	if want := fmt.Sprintf("keys=%d", n); db0 != want {
		t.Fatalf("db0 = %q, want %q", db0, want)
	}
}

// TestInfo_ReplayStatsAfterRestart verifies aof_replay_* is populated
// after a restart replays a populated AOF.
func TestInfo_ReplayStatsAfterRestart(t *testing.T) {
	dir := t.TempDir()

	s1 := StartServer(t, ServerOpts{Dir: dir, AppendFsync: "always"})
	c1 := newClient(s1.Addr)
	ctx := context.Background()
	for i := range 5 {
		if err := c1.Set(ctx, fmt.Sprintf("k%d", i), "v", 0).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}
	}
	_ = c1.Close()
	s1.Stop()

	// Restart against the same dir — startup replays the AOF.
	s2 := StartServer(t, ServerOpts{Dir: dir, AppendFsync: "always"})
	c2 := newClient(s2.Addr)
	defer func() { _ = c2.Close() }()

	info, err := c2.Info(ctx, "stats").Result()
	if err != nil {
		t.Fatalf("INFO stats: %v", err)
	}
	records, ok := infoField(info, "aof_replay_records")
	if !ok {
		t.Fatalf("INFO stats missing aof_replay_records\n%s", info)
	}
	if records == "0" || records == "" {
		t.Fatalf("aof_replay_records = %q after restart, want > 0", records)
	}
}
