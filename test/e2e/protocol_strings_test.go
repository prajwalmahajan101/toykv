package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newClient returns a go-redis client targeting the supplied server. It uses
// short timeouts so a stuck server fails the test fast rather than hanging.
func newClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:        addr,
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
		MaxRetries:  -1, // surface failures, don't paper over them
	})
}

func TestProtocol_PingEcho(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	if got, err := c.Ping(ctx).Result(); err != nil || got != "PONG" {
		t.Fatalf("PING: got %q err %v", got, err)
	}
	if got, err := c.Echo(ctx, "hello").Result(); err != nil || got != "hello" {
		t.Fatalf("ECHO: got %q err %v", got, err)
	}
}

func TestProtocol_SetGetDel(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	if got, err := c.Get(ctx, "k").Result(); err != nil || got != "v" {
		t.Fatalf("GET: got %q err %v", got, err)
	}
	if _, err := c.Get(ctx, "missing").Result(); err != redis.Nil {
		t.Fatalf("GET missing: want redis.Nil, got %v", err)
	}
	if n, err := c.Del(ctx, "k", "missing").Result(); err != nil || n != 1 {
		t.Fatalf("DEL: got %d err %v", n, err)
	}
}

func TestProtocol_Exists(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	_ = c.Set(ctx, "a", "1", 0).Err()
	_ = c.Set(ctx, "b", "2", 0).Err()
	if n, err := c.Exists(ctx, "a", "b", "missing", "a").Result(); err != nil || n != 3 {
		t.Fatalf("EXISTS: got %d err %v", n, err)
	}
}

func TestProtocol_IncrDecr(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	if n, err := c.Incr(ctx, "counter").Result(); err != nil || n != 1 {
		t.Fatalf("INCR fresh: got %d err %v", n, err)
	}
	if n, err := c.Incr(ctx, "counter").Result(); err != nil || n != 2 {
		t.Fatalf("INCR existing: got %d err %v", n, err)
	}
	if n, err := c.Decr(ctx, "counter").Result(); err != nil || n != 1 {
		t.Fatalf("DECR: got %d err %v", n, err)
	}

	// non-integer payload → error.
	_ = c.Set(ctx, "str", "abc", 0).Err()
	if _, err := c.Incr(ctx, "str").Result(); err == nil {
		t.Fatal("INCR on non-int: want error, got nil")
	}
}

func TestProtocol_KeysDbsizeFlushdb(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	for _, k := range []string{"a:1", "a:2", "b:1"} {
		if err := c.Set(ctx, k, "x", 0).Err(); err != nil {
			t.Fatalf("SET %s: %v", k, err)
		}
	}
	if n, err := c.DBSize(ctx).Result(); err != nil || n != 3 {
		t.Fatalf("DBSIZE: got %d err %v", n, err)
	}
	got, err := c.Keys(ctx, "a:*").Result()
	if err != nil {
		t.Fatalf("KEYS: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("KEYS a:*: want 2, got %v", got)
	}
	if _, err := c.FlushDB(ctx).Result(); err != nil {
		t.Fatalf("FLUSHDB: %v", err)
	}
	if n, _ := c.DBSize(ctx).Result(); n != 0 {
		t.Fatalf("DBSIZE after FLUSHDB: %d", n)
	}
}
