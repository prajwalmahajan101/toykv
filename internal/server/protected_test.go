package server

import (
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/cluster"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

func TestParseProtectedMode(t *testing.T) {
	on := []string{"", "yes", "YES", "on", "true", "1", " yes "}
	off := []string{"no", "NO", "off", "false", "0"}
	bad := []string{"bogus", "maybe", "y", "n"}

	for _, s := range on {
		if enabled, err := ParseProtectedMode(s); err != nil || !enabled {
			t.Errorf("ParseProtectedMode(%q) = (%v,%v), want (true,nil)", s, enabled, err)
		}
	}
	for _, s := range off {
		if enabled, err := ParseProtectedMode(s); err != nil || enabled {
			t.Errorf("ParseProtectedMode(%q) = (%v,%v), want (false,nil)", s, enabled, err)
		}
	}
	for _, s := range bad {
		if _, err := ParseProtectedMode(s); err == nil {
			t.Errorf("ParseProtectedMode(%q) = nil err, want error", s)
		}
	}
}

func TestCheckProtectedMode(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		requirePass string
		tlsOn       bool
		mode        string
		wantRefuse  bool
	}{
		// The one unsafe combination: enabled + non-loopback + no auth/TLS.
		{"non-loopback no auth", "0.0.0.0:6390", "", false, "yes", true},
		{"empty host (all ifaces)", ":6390", "", false, "yes", true},
		{"lan ip no auth", "192.168.1.5:6390", "", false, "yes", true},
		{"ipv6 unspecified", "[::]:6390", "", false, "yes", true},

		// Auth or TLS makes any bind safe.
		{"non-loopback with requirepass", "0.0.0.0:6390", "s3cret", false, "yes", false},
		{"non-loopback with tls", "0.0.0.0:6390", "", true, "yes", false},

		// Disabled opts out.
		{"non-loopback disabled", "0.0.0.0:6390", "", false, "no", false},

		// Loopback binds are always allowed.
		{"ipv4 loopback", "127.0.0.1:6390", "", false, "yes", false},
		{"ipv6 loopback", "[::1]:6390", "", false, "yes", false},
		{"localhost", "localhost:6390", "", false, "yes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkProtectedMode(tt.addr, tt.requirePass, tt.tlsOn, tt.mode)
			if tt.wantRefuse && err == nil {
				t.Fatalf("checkProtectedMode(%q,...) = nil, want refusal", tt.addr)
			}
			if !tt.wantRefuse && err != nil {
				t.Fatalf("checkProtectedMode(%q,...) = %v, want allow", tt.addr, err)
			}
		})
	}

	// A bad mode value is an error regardless of the bind.
	if err := checkProtectedMode("127.0.0.1:6390", "", false, "bogus"); err == nil {
		t.Fatal("checkProtectedMode with bad mode = nil, want error")
	}
}

// TestNew_ProtectedModeRefusal proves server.New refuses an unsafe bind and
// allows the safe variants, so the refusal protects any embedder (not just
// the CLI).
func TestNew_ProtectedModeRefusal(t *testing.T) {
	base := func() Config {
		return Config{Addr: "0.0.0.0:0", Store: store.New()}
	}
	if _, err := New(base()); err == nil {
		t.Fatal("New on non-loopback + no auth = nil, want refusal")
	}

	safe := base()
	safe.ProtectedMode = "no"
	if _, err := New(safe); err != nil {
		t.Fatalf("New with -protected-mode no: %v", err)
	}

	authed := base()
	authed.RequirePass = "pw"
	if _, err := New(authed); err != nil {
		t.Fatalf("New with requirepass: %v", err)
	}

	loop := base()
	loop.Addr = "127.0.0.1:0"
	if _, err := New(loop); err != nil {
		t.Fatalf("New on loopback: %v", err)
	}
}

func TestCheckRaftBind(t *testing.T) {
	tests := []struct {
		name       string
		raftAddr   string
		insecure   bool
		wantRefuse bool
	}{
		// A public raft bind without -raft-insecure is the one unsafe case.
		{"non-loopback", "0.0.0.0:7001", false, true},
		{"empty host (all ifaces)", ":7001", false, true},
		{"lan ip", "192.168.1.5:7001", false, true},
		{"ipv6 unspecified", "[::]:7001", false, true},

		// -raft-insecure acknowledges the trusted-network posture.
		{"non-loopback insecure", "0.0.0.0:7001", true, false},

		// Loopback raft binds are always allowed.
		{"ipv4 loopback", "127.0.0.1:7001", false, false},
		{"ipv6 loopback", "[::1]:7001", false, false},
		{"localhost", "localhost:7001", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRaftBind(tt.raftAddr, tt.insecure)
			if tt.wantRefuse && err == nil {
				t.Fatalf("checkRaftBind(%q,%v) = nil, want refusal", tt.raftAddr, tt.insecure)
			}
			if !tt.wantRefuse && err != nil {
				t.Fatalf("checkRaftBind(%q,%v) = %v, want allow", tt.raftAddr, tt.insecure, err)
			}
		})
	}
}

// TestNew_RaftBindRefusal proves server.New refuses a multi-node cluster whose
// plaintext Raft peer transport would bind a non-loopback address without the
// -raft-insecure acknowledgement. The guard runs before cluster.New, so no real
// transport/log is opened — only the refusal path is exercised here; the allow
// paths are covered by TestCheckRaftBind (standing up a real cluster is an
// integration concern, not a guard test).
func TestNew_RaftBindRefusal(t *testing.T) {
	cfg := Config{
		Addr:      "127.0.0.1:0", // loopback client bind so protected mode passes first
		Store:     store.New(),
		Replicate: true,
		NodeID:    "n1",
		Peers: []cluster.Peer{
			{ID: "n1", Addr: "0.0.0.0:7001"}, // self: public raft bind → unsafe
			{ID: "n2", Addr: "10.0.0.2:7001"},
			{ID: "n3", Addr: "10.0.0.3:7001"},
		},
		RaftDir: t.TempDir(),
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("New on multi-node with non-loopback raft bind + no -raft-insecure = nil, want refusal")
	}
}
