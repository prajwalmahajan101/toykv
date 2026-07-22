package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newRESP3Client returns a go-redis client that negotiates RESP3 on
// connect. go-redis sends `HELLO 3` as the first command of every new
// connection when Protocol is 3, so a successful round-trip proves the
// server's HELLO negotiation works against a real client.
func newRESP3Client(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:        addr,
		Protocol:    3,
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
		MaxRetries:  -1,
	})
}

func TestRESP3_GoRedisRoundTrip(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newRESP3Client(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// If HELLO 3 negotiation failed, the first command on the pooled
	// connection would surface the handshake error here.
	if got, err := c.Ping(ctx).Result(); err != nil || got != "PONG" {
		t.Fatalf("PING (RESP3): got %q err %v", got, err)
	}

	// Every §5.1 command must still round-trip under RESP3.
	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	if got, err := c.Get(ctx, "k").Result(); err != nil || got != "v" {
		t.Fatalf("GET: got %q err %v", got, err)
	}
	// The migrated null (`_` on RESP3) must decode to redis.Nil, exactly
	// as `$-1` does on RESP2.
	if _, err := c.Get(ctx, "missing").Result(); err != redis.Nil {
		t.Fatalf("GET missing (RESP3): want redis.Nil, got %v", err)
	}
	// NX failure on an existing key is the other migrated null.
	if got, err := c.SetNX(ctx, "k", "other", 0).Result(); err != nil || got {
		t.Fatalf("SETNX existing (RESP3): got %v err %v, want false", got, err)
	}
	if n, err := c.Incr(ctx, "ctr").Result(); err != nil || n != 1 {
		t.Fatalf("INCR: got %d err %v", n, err)
	}
	if n, err := c.Exists(ctx, "k").Result(); err != nil || n != 1 {
		t.Fatalf("EXISTS: got %d err %v", n, err)
	}
	if err := c.Del(ctx, "k", "ctr").Err(); err != nil {
		t.Fatalf("DEL: %v", err)
	}
}

// TestRESP3_HashMapAndOrder drives HSET/HKEYS/HVALS/HGETALL under RESP3
// against the shipped binary. It is the end-to-end gate for two fixes at
// once: the server's `%` map reply must decode (go-redis fails hard on an
// unknown prefix, same as toykv-cli did), and HKEYS[i]↔HVALS[i]↔HGETALL
// must correspond in a stable insertion order.
func TestRESP3_HashMapAndOrder(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newRESP3Client(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// Insert in a deliberately non-alphabetical order so a map-iteration
	// bug would show up as a reordering.
	fields := []string{"zeta", "alpha", "mike", "bravo"}
	vals := []string{"1", "2", "3", "4"}
	args := make([]any, 0, len(fields)*2)
	for i := range fields {
		args = append(args, fields[i], vals[i])
	}
	if err := c.HSet(ctx, "h", args...).Err(); err != nil {
		t.Fatalf("HSET: %v", err)
	}

	// HGETALL decodes to a map[string]string via the RESP3 `%` frame.
	all, err := c.HGetAll(ctx, "h").Result()
	if err != nil {
		t.Fatalf("HGETALL (RESP3 map): %v", err)
	}
	if len(all) != len(fields) {
		t.Fatalf("HGETALL len = %d, want %d", len(all), len(fields))
	}
	for i, f := range fields {
		if all[f] != vals[i] {
			t.Errorf("HGETALL[%q] = %q, want %q", f, all[f], vals[i])
		}
	}

	// HKEYS[i] must correspond to HVALS[i], and both must follow insertion
	// order — repeated across two calls to prove the order is stable.
	for attempt := 0; attempt < 2; attempt++ {
		keys, err := c.HKeys(ctx, "h").Result()
		if err != nil {
			t.Fatalf("HKEYS: %v", err)
		}
		hv, err := c.HVals(ctx, "h").Result()
		if err != nil {
			t.Fatalf("HVALS: %v", err)
		}
		if len(keys) != len(fields) || len(hv) != len(fields) {
			t.Fatalf("HKEYS/HVALS len = %d/%d, want %d", len(keys), len(hv), len(fields))
		}
		for i := range fields {
			if keys[i] != fields[i] {
				t.Errorf("attempt %d: HKEYS[%d] = %q, want %q (insertion order)", attempt, i, keys[i], fields[i])
			}
			if hv[i] != vals[i] {
				t.Errorf("attempt %d: HVALS[%d] = %q, want %q", attempt, i, hv[i], vals[i])
			}
		}
	}
}
