package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestProtocol_TTL_MissingAndPersistent(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// Missing → -2 (returned by go-redis as -2 * time.Nanosecond).
	if d, err := c.TTL(ctx, "missing").Result(); err != nil || d != -2*time.Nanosecond {
		t.Fatalf("TTL missing: got %v err %v", d, err)
	}

	// Set without TTL → -1.
	_ = c.Set(ctx, "k", "v", 0).Err()
	if d, err := c.TTL(ctx, "k").Result(); err != nil || d != -1*time.Nanosecond {
		t.Fatalf("TTL persistent: got %v err %v", d, err)
	}
}

func TestProtocol_Expire_Persist_Roundtrip(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	_ = c.Set(ctx, "k", "v", 0).Err()
	if ok, err := c.Expire(ctx, "k", 10*time.Second).Result(); err != nil || !ok {
		t.Fatalf("EXPIRE: ok=%v err=%v", ok, err)
	}
	d, err := c.TTL(ctx, "k").Result()
	if err != nil || d <= 0 || d > 10*time.Second {
		t.Fatalf("TTL after EXPIRE: %v err %v", d, err)
	}
	if ok, err := c.Persist(ctx, "k").Result(); err != nil || !ok {
		t.Fatalf("PERSIST: ok=%v err=%v", ok, err)
	}
	if d, _ := c.TTL(ctx, "k").Result(); d != -1*time.Nanosecond {
		t.Fatalf("TTL after PERSIST: want -1, got %v", d)
	}
}

func TestProtocol_PExpire_PTTL(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	_ = c.Set(ctx, "k", "v", 0).Err()
	if ok, err := c.PExpire(ctx, "k", 5*time.Second).Result(); err != nil || !ok {
		t.Fatalf("PEXPIRE: %v %v", ok, err)
	}
	d, err := c.PTTL(ctx, "k").Result()
	if err != nil || d <= 0 || d > 5*time.Second {
		t.Fatalf("PTTL: got %v err %v", d, err)
	}
}

func TestProtocol_PExpireAt(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	_ = c.Set(ctx, "k", "v", 0).Err()
	when := time.Now().Add(2 * time.Second)
	if ok, err := c.PExpireAt(ctx, "k", when).Result(); err != nil || !ok {
		t.Fatalf("PEXPIREAT: %v %v", ok, err)
	}
	if d, _ := c.PTTL(ctx, "k").Result(); d <= 0 || d > 2*time.Second+50*time.Millisecond {
		t.Fatalf("PTTL after PEXPIREAT: %v", d)
	}
}

func TestProtocol_SetWithExpiry(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// SET k v EX 10 — go-redis translates the duration internally.
	if err := c.Set(ctx, "ex", "v", 10*time.Second).Err(); err != nil {
		t.Fatalf("SET EX: %v", err)
	}
	if d, _ := c.TTL(ctx, "ex").Result(); d <= 0 || d > 10*time.Second {
		t.Fatalf("TTL after SET EX: %v", d)
	}

	// SET k v PX 100 — short enough to verify expiry within the test.
	if err := c.Set(ctx, "px", "v", 100*time.Millisecond).Err(); err != nil {
		t.Fatalf("SET PX: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := c.Get(ctx, "px").Result(); err != redis.Nil {
		t.Fatalf("GET after PX expiry: want redis.Nil, got %v", err)
	}
}

func TestProtocol_Expire_Missing(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// EXPIRE on missing key returns 0 (Redis semantics).
	if ok, err := c.Expire(ctx, "nope", 10*time.Second).Result(); err != nil || ok {
		t.Fatalf("EXPIRE missing: ok=%v err=%v (want false, nil)", ok, err)
	}
	// PERSIST on missing/no-TTL key returns 0.
	if ok, err := c.Persist(ctx, "nope").Result(); err != nil || ok {
		t.Fatalf("PERSIST missing: ok=%v err=%v", ok, err)
	}
}
