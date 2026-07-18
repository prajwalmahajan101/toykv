package chaos

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/aof"
)

// TestKeyspaceReplay_SurvivesCrash is M15's defence-in-depth crash test: a
// hard SIGKILL after RENAME/RENAMENX/COPY, then restart, must replay every
// keyspace op from the AOF — carrying TTL and value type — while the file
// stays on the existing v3 format with no header bump ("no AOF format
// bump"). It is a single deterministic kill+restart; the real crash storm
// lives in the tagged invariant suite (invariants_test.go).
func TestKeyspaceReplay_SurvivesCrash(t *testing.T) {
	srv := NewServer(t, "always") // fsync=always ⇒ ack implies durable
	srv.Start(t)
	t.Cleanup(srv.Stop)

	cli := NewRawClient(srv.Addr)
	// Seed one of each type; the string carries a TTL.
	mustDo(t, cli, "SET", "s", "v", "EX", "1000")
	mustDo(t, cli, "RPUSH", "lst", "a", "b")
	mustDo(t, cli, "HSET", "h", "f", "hv")
	// Keyspace ops: move the string (+TTL), move the list, copy the hash.
	mustDo(t, cli, "RENAME", "s", "s2")
	mustDo(t, cli, "RENAMENX", "lst", "lst2")
	mustDo(t, cli, "COPY", "h", "h2")
	cli.Close()

	// Hard crash — no graceful drain — then restart to force AOF replay.
	srv.Kill()
	srv.Start(t)

	cli2 := NewRawClient(srv.Addr)
	defer cli2.Close()

	// String moved to its new name, with its TTL, and the old name is gone.
	if v, isNil, err := cli2.Do("GET", "s2"); err != nil || isNil || v != "v" {
		t.Fatalf("GET s2 after replay = (%q,%v,%v), want (v,false,nil)", v, isNil, err)
	}
	if _, isNil, err := cli2.Do("GET", "s"); err != nil || !isNil {
		t.Fatalf("GET s after replay = (nil=%v,%v), want a miss (RENAME replayed)", isNil, err)
	}
	if ttl, _, err := cli2.Do("TTL", "s2"); err != nil {
		t.Fatalf("TTL s2: %v", err)
	} else if n, _ := strconv.Atoi(ttl); n <= 0 || n > 1000 {
		t.Fatalf("TTL s2 = %s, want (0,1000] — TTL did not survive replay", ttl)
	}

	// List moved; type preserved.
	if typ, _, err := cli2.Do("TYPE", "lst2"); err != nil || typ != "list" {
		t.Fatalf("TYPE lst2 after replay = (%q,%v), want list", typ, err)
	}

	// Hash copy present AND source retained (COPY does not consume).
	if v, _, err := cli2.Do("HGET", "h2", "f"); err != nil || v != "hv" {
		t.Fatalf("HGET h2 f after replay = (%q,%v), want hv", v, err)
	}
	if v, _, err := cli2.Do("HGET", "h", "f"); err != nil || v != "hv" {
		t.Fatalf("HGET h f after replay = (%q,%v), want hv (COPY kept source)", v, err)
	}

	// The AOF header must still be v3 — the keyspace ops ride the existing
	// format verbatim, no bump.
	assertAOFVersion(t, srv.Dir, aof.Version3)
}

// mustDo runs a command and fails the test on any transport or error reply.
func mustDo(t *testing.T, cli *RawClient, parts ...string) {
	t.Helper()
	if _, _, err := cli.Do(parts...); err != nil {
		t.Fatalf("%v: %v", parts, err)
	}
}

// assertAOFVersion reads the on-disk AOF header version byte and asserts it
// equals want.
func assertAOFVersion(t *testing.T, dir string, want byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, aof.Filename))
	if err != nil {
		t.Fatalf("read AOF: %v", err)
	}
	if len(data) < aof.HeaderLen {
		t.Fatalf("AOF shorter than header (%d bytes)", len(data))
	}
	if got := data[aof.HeaderLen-1]; got != want {
		t.Fatalf("AOF header version = 0x%02x, want 0x%02x (unexpected format bump)", got, want)
	}
}
