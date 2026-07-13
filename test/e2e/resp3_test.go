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
