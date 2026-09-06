package cluster

import "testing"

func TestParsePeers(t *testing.T) {
	t.Run("empty spec yields nil", func(t *testing.T) {
		peers, err := ParsePeers("")
		if err != nil {
			t.Fatalf("ParsePeers(\"\"): %v", err)
		}
		if peers != nil {
			t.Fatalf("want nil, got %v", peers)
		}
	})

	t.Run("valid odd membership", func(t *testing.T) {
		peers, err := ParsePeers("n1@127.0.0.1:7001, n2@127.0.0.1:7002 ,n3@host:7003")
		if err != nil {
			t.Fatalf("ParsePeers: %v", err)
		}
		if len(peers) != 3 {
			t.Fatalf("want 3 peers, got %d", len(peers))
		}
		if peers[0].ID != "n1" || peers[0].Addr != "127.0.0.1:7001" {
			t.Fatalf("peer[0] = %+v", peers[0])
		}
		if peers[2].ID != "n3" || peers[2].Addr != "host:7003" {
			t.Fatalf("peer[2] = %+v", peers[2])
		}
		// Back-compat: an M19 spec with no client-addr suffix leaves ClientAddr "".
		if peers[0].ClientAddr != "" {
			t.Fatalf("peer[0].ClientAddr = %q, want empty", peers[0].ClientAddr)
		}
	})

	t.Run("client-addr suffix parses per-peer", func(t *testing.T) {
		// Mixed: n1/n3 advertise a client addr, n2 does not (still valid).
		peers, err := ParsePeers("n1@127.0.0.1:7001/127.0.0.1:6391, n2@127.0.0.1:7002 ,n3@host:7003/host:6393")
		if err != nil {
			t.Fatalf("ParsePeers: %v", err)
		}
		if peers[0].Addr != "127.0.0.1:7001" || peers[0].ClientAddr != "127.0.0.1:6391" {
			t.Fatalf("peer[0] = %+v", peers[0])
		}
		if peers[1].ClientAddr != "" {
			t.Fatalf("peer[1].ClientAddr = %q, want empty", peers[1].ClientAddr)
		}
		if peers[2].Addr != "host:7003" || peers[2].ClientAddr != "host:6393" {
			t.Fatalf("peer[2] = %+v", peers[2])
		}
	})

	for _, tc := range []struct {
		name, spec string
	}{
		{"missing @", "n1-127.0.0.1:7001"},
		{"empty id", "@127.0.0.1:7001"},
		{"bad address", "n1@127.0.0.1"},
		{"bad client address", "n1@h:1/h,n2@h:2,n3@h:3"},
		{"duplicate id", "n1@h:1,n1@h:2,n3@h:3"},
		{"even membership", "n1@h:1,n2@h:2"},
		{"empty entry", "n1@h:1,,n3@h:3"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			if _, err := ParsePeers(tc.spec); err == nil {
				t.Fatalf("ParsePeers(%q) = nil error; want rejection", tc.spec)
			}
		})
	}
}
