package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// typedRoundTrip drives the M11 list/hash/TYPE surface through a real
// go-redis client. Shared by the RESP2 and RESP3 variants — the same
// assertions must hold on both protocols (the wire framing differs,
// the decoded values must not).
func typedRoundTrip(t *testing.T, c *redis.Client) {
	t.Helper()
	ctx := context.Background()

	// Lists.
	if n, err := c.RPush(ctx, "l", "a", "b").Result(); err != nil || n != 2 {
		t.Fatalf("RPUSH: got %d err %v", n, err)
	}
	if n, err := c.LPush(ctx, "l", "front").Result(); err != nil || n != 3 {
		t.Fatalf("LPUSH: got %d err %v", n, err)
	}
	if got, err := c.LRange(ctx, "l", 0, -1).Result(); err != nil || strings.Join(got, ",") != "front,a,b" {
		t.Fatalf("LRANGE: got %v err %v", got, err)
	}
	if n, err := c.LLen(ctx, "l").Result(); err != nil || n != 3 {
		t.Fatalf("LLEN: got %d err %v", n, err)
	}
	if got, err := c.LIndex(ctx, "l", -1).Result(); err != nil || got != "b" {
		t.Fatalf("LINDEX: got %q err %v", got, err)
	}
	if got, err := c.LPop(ctx, "l").Result(); err != nil || got != "front" {
		t.Fatalf("LPOP: got %q err %v", got, err)
	}
	if got, err := c.RPop(ctx, "l").Result(); err != nil || got != "b" {
		t.Fatalf("RPOP: got %q err %v", got, err)
	}

	// Hashes — incl. HGETALL, which decodes from a native map on RESP3
	// and a flat array on RESP2; go-redis must produce the same
	// map[string]string either way.
	if n, err := c.HSet(ctx, "h", "f1", "v1", "f2", "v2").Result(); err != nil || n != 2 {
		t.Fatalf("HSET: got %d err %v", n, err)
	}
	if got, err := c.HGet(ctx, "h", "f1").Result(); err != nil || got != "v1" {
		t.Fatalf("HGET: got %q err %v", got, err)
	}
	if _, err := c.HGet(ctx, "h", "missing").Result(); err != redis.Nil {
		t.Fatalf("HGET missing: want redis.Nil, got %v", err)
	}
	all, err := c.HGetAll(ctx, "h").Result()
	if err != nil || len(all) != 2 || all["f1"] != "v1" || all["f2"] != "v2" {
		t.Fatalf("HGETALL: got %v err %v", all, err)
	}
	if ok, err := c.HExists(ctx, "h", "f2").Result(); err != nil || !ok {
		t.Fatalf("HEXISTS: got %v err %v", ok, err)
	}
	if n, err := c.HLen(ctx, "h").Result(); err != nil || n != 2 {
		t.Fatalf("HLEN: got %d err %v", n, err)
	}
	if keys, err := c.HKeys(ctx, "h").Result(); err != nil || len(keys) != 2 {
		t.Fatalf("HKEYS: got %v err %v", keys, err)
	}
	if vals, err := c.HVals(ctx, "h").Result(); err != nil || len(vals) != 2 {
		t.Fatalf("HVALS: got %v err %v", vals, err)
	}
	if n, err := c.HDel(ctx, "h", "f1").Result(); err != nil || n != 1 {
		t.Fatalf("HDEL: got %d err %v", n, err)
	}

	// TYPE per kind.
	if err := c.Set(ctx, "s", "v", 0).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	for key, want := range map[string]string{"s": "string", "l": "list", "h": "hash", "nope": "none"} {
		if got, err := c.Type(ctx, key).Result(); err != nil || got != want {
			t.Fatalf("TYPE %s: got %q err %v, want %q", key, got, err, want)
		}
	}

	// WRONGTYPE surfaces as a server error through the client.
	if err := c.LPush(ctx, "s", "x").Err(); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("LPUSH on string: want WRONGTYPE error, got %v", err)
	}

	// Empty-collection deletion: pop the last element, key vanishes.
	if got, err := c.LPop(ctx, "l").Result(); err != nil || got != "a" {
		t.Fatalf("LPOP last: got %q err %v", got, err)
	}
	if n, err := c.Exists(ctx, "l").Result(); err != nil || n != 0 {
		t.Fatalf("EXISTS after last pop: got %d err %v, want 0", n, err)
	}

	// Cleanup so the shared server is reusable across variants.
	if err := c.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("FLUSHDB: %v", err)
	}
}

func TestTypes_GoRedisRoundTrip_RESP2(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	typedRoundTrip(t, c)
}

func TestTypes_GoRedisRoundTrip_RESP3(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newRESP3Client(s.Addr)
	defer func() { _ = c.Close() }()
	typedRoundTrip(t, c)
}
