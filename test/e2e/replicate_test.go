package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// replicateOpts returns ServerOpts that enable the M18 single-node Raft path.
func replicateOpts(dir, fsync string) ServerOpts {
	return ServerOpts{Dir: dir, AppendFsync: fsync, ExtraArgs: []string{"-replicate"}}
}

// TestReplicate_GoRedisRoundTrip runs the full M11 list/hash/TYPE surface
// through a -replicate server. Every mutating command flows Propose→Apply, and
// because typedRoundTrip is the exact assertion set used against the standalone
// server, passing it proves replicated behaviour is identical — reads served
// locally, mutations replicated and applied, replies (incl. RPUSH/LPOP/HDEL
// values computed inside Apply) byte-for-byte the same.
func TestReplicate_GoRedisRoundTrip_RESP2(t *testing.T) {
	s := StartServer(t, replicateOpts(t.TempDir(), "no"))
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	typedRoundTrip(t, c)
}

func TestReplicate_GoRedisRoundTrip_RESP3(t *testing.T) {
	s := StartServer(t, replicateOpts(t.TempDir(), "no"))
	c := newRESP3Client(s.Addr)
	defer func() { _ = c.Close() }()
	typedRoundTrip(t, c)
}

// TestReplicate_MutatingReplies exercises the mutating commands whose reply is
// computed inside Apply — the exact values ToyRaft's Propose discards at the
// Node boundary. If the index→reply capture were broken, these would come back
// wrong (or as errors), so this is the sharpest test of the M18 seam.
func TestReplicate_MutatingReplies(t *testing.T) {
	s := StartServer(t, replicateOpts(t.TempDir(), "no"))
	c := newClient(s.Addr)
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// INCR/DECR return the new counter value from inside Apply.
	if n, err := c.Incr(ctx, "ctr").Result(); err != nil || n != 1 {
		t.Fatalf("INCR: got %d err %v, want 1", n, err)
	}
	if n, err := c.Incr(ctx, "ctr").Result(); err != nil || n != 2 {
		t.Fatalf("INCR: got %d err %v, want 2", n, err)
	}
	if n, err := c.Decr(ctx, "ctr").Result(); err != nil || n != 1 {
		t.Fatalf("DECR: got %d err %v, want 1", n, err)
	}

	// DEL returns the count actually removed.
	if err := c.Set(ctx, "a", "1", 0).Err(); err != nil {
		t.Fatalf("SET a: %v", err)
	}
	if err := c.Set(ctx, "b", "2", 0).Err(); err != nil {
		t.Fatalf("SET b: %v", err)
	}
	if n, err := c.Del(ctx, "a", "b", "missing").Result(); err != nil || n != 2 {
		t.Fatalf("DEL: got %d err %v, want 2", n, err)
	}

	// EXPIRE is resolved to an absolute PEXPIREAT at propose time; the reply is
	// 1 (timeout set) and the TTL reads back close to the requested duration.
	if err := c.Set(ctx, "ttlkey", "v", 0).Err(); err != nil {
		t.Fatalf("SET ttlkey: %v", err)
	}
	if ok, err := c.Expire(ctx, "ttlkey", 100*time.Second).Result(); err != nil || !ok {
		t.Fatalf("EXPIRE: got %v err %v, want true", ok, err)
	}
	if d, err := c.TTL(ctx, "ttlkey").Result(); err != nil || d <= 90*time.Second || d > 100*time.Second {
		t.Fatalf("TTL: got %v err %v, want ~100s", d, err)
	}
	// PERSIST returns 1 when it clears a TTL.
	if ok, err := c.Persist(ctx, "ttlkey").Result(); err != nil || !ok {
		t.Fatalf("PERSIST: got %v err %v, want true", ok, err)
	}
	// go-redis maps the "-1" (no expiry) TTL reply to a -1ns duration.
	if d, err := c.TTL(ctx, "ttlkey").Result(); err != nil || d != -1*time.Nanosecond {
		t.Fatalf("TTL after PERSIST: got %v err %v, want -1ns (no expiry)", d, err)
	}

	// RENAME / RENAMENX / COPY replies come from Apply too.
	if err := c.Set(ctx, "src", "hello", 0).Err(); err != nil {
		t.Fatalf("SET src: %v", err)
	}
	if err := c.Rename(ctx, "src", "dst").Err(); err != nil {
		t.Fatalf("RENAME: %v", err)
	}
	if got, err := c.Get(ctx, "dst").Result(); err != nil || got != "hello" {
		t.Fatalf("GET dst after RENAME: got %q err %v", got, err)
	}
	if n, err := c.Copy(ctx, "dst", "dst2", 0, false).Result(); err != nil || n != 1 {
		t.Fatalf("COPY: got %d err %v, want 1", n, err)
	}
	if got, err := c.Get(ctx, "dst2").Result(); err != nil || got != "hello" {
		t.Fatalf("GET dst2 after COPY: got %q err %v", got, err)
	}

	// A wrong-type command still surfaces its error reply through Apply.
	if err := c.LPush(ctx, "dst", "x").Err(); err == nil {
		t.Fatal("LPUSH on a string key: want WRONGTYPE error, got nil")
	}
}

// TestReplicate_RestartDurability proves the M18 durability decision: with the
// state machine in the loop, AOF is still written exactly once (inside Apply),
// so a clean restart re-derives all state from the AOF — the in-memory Raft log
// starting empty changes nothing observable.
func TestReplicate_RestartDurability(t *testing.T) {
	dir := t.TempDir()
	s1 := StartServer(t, replicateOpts(dir, "always"))
	c1 := newClient(s1.Addr)
	ctx := context.Background()

	if err := c1.Set(ctx, "s", "persisted", 0).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	if _, err := c1.Incr(ctx, "ctr").Result(); err != nil {
		t.Fatalf("INCR: %v", err)
	}
	if _, err := c1.Incr(ctx, "ctr").Result(); err != nil {
		t.Fatalf("INCR: %v", err)
	}
	if err := c1.RPush(ctx, "l", "a", "b", "c").Err(); err != nil {
		t.Fatalf("RPUSH: %v", err)
	}
	if err := c1.HSet(ctx, "h", "f1", "v1", "f2", "v2").Err(); err != nil {
		t.Fatalf("HSET: %v", err)
	}
	_ = c1.Close()
	s1.Stop()

	// Restart against the same directory, still replicated.
	s2 := StartServer(t, replicateOpts(dir, "always"))
	c2 := newClient(s2.Addr)
	defer func() { _ = c2.Close() }()

	assertReplicatedState(t, c2)
}

func assertReplicatedState(t *testing.T, c *redis.Client) {
	t.Helper()
	ctx := context.Background()
	if got, err := c.Get(ctx, "s").Result(); err != nil || got != "persisted" {
		t.Fatalf("GET s after restart: got %q err %v", got, err)
	}
	if got, err := c.Get(ctx, "ctr").Result(); err != nil || got != "2" {
		t.Fatalf("GET ctr after restart: got %q err %v, want 2", got, err)
	}
	if got, err := c.LRange(ctx, "l", 0, -1).Result(); err != nil || len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("LRANGE l after restart: got %v err %v", got, err)
	}
	all, err := c.HGetAll(ctx, "h").Result()
	if err != nil || all["f1"] != "v1" || all["f2"] != "v2" {
		t.Fatalf("HGETALL h after restart: got %v err %v", all, err)
	}
}
