package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// M22 owned-risk: the cluster pane must surface the # Replication topology
// and, critically, reflect a leadership change on the next poll without any
// user interaction. View() is a pure function of model state, so the whole
// suite's synchronous runMsg + View() harness observes the flip
// deterministically — no teatest goroutine/timing needed.

const leaderInfoBody = "# Replication\r\n" +
	"role:master\r\n" +
	"connected_slaves:2\r\n" +
	"slave0:ip=10.0.0.2,port=7002,state=online,offset=41,lag=1\r\n" +
	"slave1:ip=10.0.0.3,port=7003,state=online,offset=42,lag=0\r\n" +
	"master_repl_offset:42\r\n"

const followerInfoBody = "# Replication\r\n" +
	"role:slave\r\n" +
	"connected_slaves:0\r\n" +
	"master_repl_offset:42\r\n"

func TestParseInfo_Replication(t *testing.T) {
	s := parseInfo(leaderInfoBody)
	if s.repl.role != "master" {
		t.Errorf("role = %q, want master", s.repl.role)
	}
	if s.repl.off != 42 {
		t.Errorf("master_repl_offset = %d, want 42", s.repl.off)
	}
	if len(s.repl.peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(s.repl.peers))
	}
	p := s.repl.peers[0]
	if p.addr != "10.0.0.2:7002" || p.state != "online" || p.off != 41 || p.lag != 1 {
		t.Errorf("peer[0] = %+v, want {addr:10.0.0.2:7002 state:online off:41 lag:1}", p)
	}
}

func TestParseInfo_StandaloneHasNoRole(t *testing.T) {
	// A non-replicated server omits the # Replication section entirely.
	s := parseInfo("# Server\r\nuptime_in_seconds:5\r\n")
	if s.repl.role != "" {
		t.Errorf("standalone repl.role = %q, want empty", s.repl.role)
	}
}

func TestClusterPane_LeaderShowsPeers(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 130, Height: 24})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}, info: parseInfo(leaderInfoBody)})
	out := m.View()
	for _, want := range []string{"cluster", "master", "10.0.0.2:7002", "l=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("leader cluster pane missing %q\n%s", want, out)
		}
	}
}

func TestClusterPane_LeadershipFlipReflected(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 130, Height: 24})

	// Leader: pane shows master + peer rows.
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}, info: parseInfo(leaderInfoBody)})
	if out := m.View(); !strings.Contains(out, "10.0.0.2:7002") {
		t.Fatalf("setup: leader pane should list peers\n%s", out)
	}

	// Next poll reports this node as a follower — pane flips with no keypress.
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}, info: parseInfo(followerInfoBody)})
	out := m.View()
	if !strings.Contains(out, "slave") {
		t.Errorf("after flip, pane should show role slave\n%s", out)
	}
	if strings.Contains(out, "10.0.0.2:7002") {
		t.Errorf("after flip to follower, stale peer rows must be gone\n%s", out)
	}
}

func TestClusterPane_StandaloneHidesPane(t *testing.T) {
	m := NewModel(newFake(), ":6390", 2*time.Second, "")
	m, _ = runMsg(m, tea.WindowSizeMsg{Width: 130, Height: 24})
	m, _ = runMsg(m, refreshMsg{keys: []KeyInfo{{Name: "k"}}, info: infoStatus{dbsize: 1}})
	if out := m.View(); strings.Contains(out, "cluster") {
		t.Errorf("standalone server must not render the cluster pane\n%s", out)
	}
}
