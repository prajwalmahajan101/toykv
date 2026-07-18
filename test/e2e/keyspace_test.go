package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestKeyspace_GoRedisRoundTrip drives RENAME / RENAMENX / COPY through the
// go-redis v9 client against the shipped binary — the M15 exit criterion
// that they match Redis semantics including TTL preservation. go-redis's
// COPY always sends "DB 0", exercising the single-DB acceptance path.
func TestKeyspace_GoRedisRoundTrip(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// RENAME moves the value and carries the TTL.
	if err := c.Set(ctx, "src", "v", 1000*time.Second).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	if err := c.Rename(ctx, "src", "dst").Err(); err != nil {
		t.Fatalf("RENAME: %v", err)
	}
	if v, err := c.Get(ctx, "dst").Result(); err != nil || v != "v" {
		t.Fatalf("GET dst = (%q,%v), want (v,nil)", v, err)
	}
	if ttl, err := c.TTL(ctx, "dst").Result(); err != nil || ttl <= 0 || ttl > 1000*time.Second {
		t.Fatalf("TTL dst = (%v,%v), want (0,1000s]", ttl, err)
	}
	if n, _ := c.Exists(ctx, "src").Result(); n != 0 {
		t.Fatal("src still present after RENAME")
	}

	// RENAME of a missing key → -ERR no such key.
	if err := c.Rename(ctx, "nope", "x").Err(); err == nil || !strings.Contains(err.Error(), "no such key") {
		t.Fatalf("RENAME missing err = %v, want 'no such key'", err)
	}

	// RENAMENX: moves when dst is free, refuses when it exists.
	c.Set(ctx, "a", "1", 0)
	if ok, err := c.RenameNX(ctx, "a", "b").Result(); err != nil || !ok {
		t.Fatalf("RENAMENX (free) = (%v,%v), want (true,nil)", ok, err)
	}
	c.Set(ctx, "a", "2", 0)
	if ok, err := c.RenameNX(ctx, "a", "b").Result(); err != nil || ok {
		t.Fatalf("RENAMENX (taken) = (%v,%v), want (false,nil)", ok, err)
	}

	// COPY: 1 on fresh, 0 without REPLACE when dst exists, 1 with REPLACE.
	c.Set(ctx, "cs", "cv", 0)
	if n, err := c.Copy(ctx, "cs", "cd", 0, false).Result(); err != nil || n != 1 {
		t.Fatalf("COPY (fresh) = (%v,%v), want (1,nil)", n, err)
	}
	if n, err := c.Copy(ctx, "cs", "cd", 0, false).Result(); err != nil || n != 0 {
		t.Fatalf("COPY (exists) = (%v,%v), want (0,nil)", n, err)
	}
	if n, err := c.Copy(ctx, "cs", "cd", 0, true).Result(); err != nil || n != 1 {
		t.Fatalf("COPY REPLACE = (%v,%v), want (1,nil)", n, err)
	}
	if v, _ := c.Get(ctx, "cs").Result(); v != "cv" {
		t.Fatal("COPY consumed the source key")
	}
}

// TestKeyspace_RedisCLI proves byte-compat with the real redis-cli. Skipped
// when redis-cli is not installed (same policy as the other cli sweeps).
func TestKeyspace_RedisCLI(t *testing.T) {
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not on PATH")
	}
	s := StartServer(t, ServerOpts{})

	runRedisCLI(t, s.Addr, "SET", "src", "v")
	if got := runRedisCLI(t, s.Addr, "RENAME", "src", "dst"); got != "OK" {
		t.Fatalf("RENAME = %q, want OK", got)
	}
	if got := runRedisCLI(t, s.Addr, "GET", "dst"); got != "v" {
		t.Fatalf("GET dst = %q, want v", got)
	}
	if got := runRedisCLI(t, s.Addr, "RENAMENX", "dst", "dst2"); got != "1" {
		t.Fatalf("RENAMENX = %q, want 1", got)
	}
	if got := runRedisCLI(t, s.Addr, "COPY", "dst2", "dst3"); got != "1" {
		t.Fatalf("COPY = %q, want 1", got)
	}
}
