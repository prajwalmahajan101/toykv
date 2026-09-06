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
	})

	for _, tc := range []struct {
		name, spec string
	}{
		{"missing @", "n1-127.0.0.1:7001"},
		{"empty id", "@127.0.0.1:7001"},
		{"bad address", "n1@127.0.0.1"},
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
