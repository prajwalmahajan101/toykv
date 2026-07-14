package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newAuthClient returns a go-redis client with a password on the given
// protocol. Protocol 3 makes go-redis authenticate via
// `HELLO 3 AUTH default <pass>` (the M10+M12 path); protocol 2 sends a
// plain AUTH command first (the redis-cli -a path).
func newAuthClient(addr, pass string, protocol int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:        addr,
		Password:    pass,
		Protocol:    protocol,
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
		MaxRetries:  -1,
	})
}

// TestAuth_GoRedisRoundTrip: the ROADMAP §M12 exit criterion, driven on
// both wire protocols against the shipped binary.
func TestAuth_GoRedisRoundTrip(t *testing.T) {
	s := StartServer(t, ServerOpts{RequirePass: "s3cret"})
	ctx := context.Background()

	for _, proto := range []int{2, 3} {
		c := newAuthClient(s.Addr, "s3cret", proto)
		if got, err := c.Ping(ctx).Result(); err != nil || got != "PONG" {
			t.Fatalf("proto %d: PING got %q err %v", proto, got, err)
		}
		if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
			t.Fatalf("proto %d: SET: %v", proto, err)
		}
		if got, err := c.Get(ctx, "k").Result(); err != nil || got != "v" {
			t.Fatalf("proto %d: GET got %q err %v", proto, got, err)
		}
		_ = c.Close()
	}
}

// TestAuth_GoRedisWrongPassword: both protocols must surface WRONGPASS.
func TestAuth_GoRedisWrongPassword(t *testing.T) {
	s := StartServer(t, ServerOpts{RequirePass: "s3cret"})
	ctx := context.Background()

	for _, proto := range []int{2, 3} {
		c := newAuthClient(s.Addr, "wrong", proto)
		if err := c.Ping(ctx).Err(); err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
			t.Fatalf("proto %d: PING with wrong password: err %v, want WRONGPASS", proto, err)
		}
		_ = c.Close()
	}
}

// TestAuth_GoRedisNoPassword: a client that never authenticates gets
// NOAUTH on gated commands.
func TestAuth_GoRedisNoPassword(t *testing.T) {
	s := StartServer(t, ServerOpts{RequirePass: "s3cret"})
	ctx := context.Background()

	// Protocol 2 with no password sends no handshake at all — the first
	// gated command must come back NOAUTH. (Protocol 3 would fail earlier,
	// on the unauthenticated HELLO-only handshake being whitelisted, then
	// NOAUTH on the command; the gated reply is the same.)
	c := newAuthClient(s.Addr, "", 2)
	defer func() { _ = c.Close() }()
	if err := c.Get(ctx, "k").Err(); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Fatalf("GET without AUTH: err %v, want NOAUTH", err)
	}
}

// TestAuth_RedisCLI: the literal exit-criterion commands, via redis-cli.
func TestAuth_RedisCLI(t *testing.T) {
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not on PATH; CI installs redis-tools to exercise this")
	}
	s := StartServer(t, ServerOpts{RequirePass: "s3cret"})

	// redis-cli -a <pass> authenticates and round-trips.
	if got := runRedisCLI(t, s.Addr, "--no-auth-warning", "-a", "s3cret", "PING"); got != "PONG" {
		t.Errorf("redis-cli -a s3cret PING = %q, want PONG", got)
	}
	// Wrong password is rejected. Tested via an explicit AUTH command,
	// not `-a wrong PING`: with -a, a failed AUTH only warns on stderr and
	// the whitelisted PING still returns +PONG on stdout. AUTH's own
	// error reply, by contrast, lands on stdout (same as the GET/NOAUTH
	// case below).
	if got := runRedisCLI(t, s.Addr, "AUTH", "wrong"); !strings.Contains(got, "WRONGPASS") {
		t.Errorf("redis-cli AUTH wrong = %q, want WRONGPASS", got)
	}
	// Unauthenticated PING still PONGs (the deliberate whitelist).
	if got := runRedisCLI(t, s.Addr, "PING"); got != "PONG" {
		t.Errorf("unauthenticated redis-cli PING = %q, want PONG", got)
	}
	// Unauthenticated data command is gated.
	if got := runRedisCLI(t, s.Addr, "GET", "k"); !strings.Contains(got, "NOAUTH") {
		t.Errorf("unauthenticated redis-cli GET = %q, want NOAUTH", got)
	}
	// RESP3 handshake path: -3 makes redis-cli send HELLO 3 AUTH.
	if got := runRedisCLI(t, s.Addr, "--no-auth-warning", "-3", "-a", "s3cret", "PING"); got != "PONG" {
		t.Errorf("redis-cli -3 -a s3cret PING = %q, want PONG", got)
	}
}
