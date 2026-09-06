package client

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// fakeNode is a minimal RESP2 server: it accepts connections and answers every
// command frame with whatever reply() returns. It lets the redirect tests drive
// a ClusterClient over real TCP without standing up a cluster.
type fakeNode struct {
	ln    net.Listener
	reply func(argv []resp.Value) resp.Value
}

func startFakeNode(t *testing.T, reply func(argv []resp.Value) resp.Value) *fakeNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fn := &fakeNode{ln: ln, reply: reply}
	go fn.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return fn
}

func (fn *fakeNode) addr() string { return fn.ln.Addr().String() }

func (fn *fakeNode) serve() {
	for {
		conn, err := fn.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			r, w := resp.NewReader(c), resp.NewWriter(c)
			for {
				frame, err := r.ReadFrame()
				if err != nil {
					return
				}
				if err := w.WriteFrame(fn.reply(frame.Array)); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}(conn)
	}
}

// fastRedirect shrinks the backoff so redirect tests run in milliseconds.
func fastRedirect(cc *ClusterClient) {
	cc.backoffBase = time.Millisecond
	cc.backoffMax = 2 * time.Millisecond
}

func TestClusterClientFollowsRedirect(t *testing.T) {
	leader := startFakeNode(t, func(argv []resp.Value) resp.Value { return resp.OK() })
	follower := startFakeNode(t, func(argv []resp.Value) resp.Value {
		return resp.Error("NOTLEADER " + leader.addr())
	})

	cc, err := DialCluster(follower.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	fastRedirect(cc)

	v, err := cc.Do("SET", "k", "v")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if v.Kind != resp.KindSimpleString || v.Str != "OK" {
		t.Fatalf("reply = %+v, want +OK", v)
	}
	if cc.Addr() != leader.addr() {
		t.Fatalf("connected to %s, want leader %s", cc.Addr(), leader.addr())
	}
}

func TestClusterClientRedirectStormTerminates(t *testing.T) {
	// A node that always bounces the client to itself models an unsettled
	// election. The client must give up after maxRedirects and surface the
	// NOTLEADER reply rather than loop forever.
	var hits int64
	var self *fakeNode
	self = startFakeNode(t, func(argv []resp.Value) resp.Value {
		atomic.AddInt64(&hits, 1)
		return resp.Error("NOTLEADER " + self.addr())
	})

	cc, err := DialCluster(self.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	fastRedirect(cc)

	v, err := cc.Do("SET", "k", "v")
	if err != nil {
		t.Fatalf("Do returned transport error: %v", err)
	}
	if v.Kind != resp.KindError {
		t.Fatalf("reply = %+v, want a NOTLEADER error", v)
	}
	// maxRedirects redials + the initial attempt.
	if want := int64(cc.maxRedirects + 1); atomic.LoadInt64(&hits) != want {
		t.Fatalf("node hit %d times, want %d (bounded retry)", hits, want)
	}
}

func TestClusterClientRetriesLeaderlessThenSurfaces(t *testing.T) {
	// A NOTLEADER reply whose hint is not a host:port models a leaderless window
	// mid-election. The client re-polls the SAME node (no redial) up to the cap,
	// then surfaces the error unchanged — a persistently leaderless cluster
	// converges to an error rather than looping forever.
	var hits int64
	node := startFakeNode(t, func(argv []resp.Value) resp.Value {
		atomic.AddInt64(&hits, 1)
		return resp.Error("NOTLEADER no leader elected")
	})

	cc, err := DialCluster(node.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	fastRedirect(cc)

	v, err := cc.Do("GET", "k")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if v.Kind != resp.KindError || v.Str != "NOTLEADER no leader elected" {
		t.Fatalf("reply = %+v, want the NOTLEADER text unchanged", v)
	}
	if want := int64(cc.maxRedirects + 1); atomic.LoadInt64(&hits) != want {
		t.Fatalf("node re-polled %d times, want %d (bounded in-place retry)", hits, want)
	}
	if cc.Addr() != node.addr() {
		t.Fatalf("client moved to %s; must stay put on a non-dialable hint", cc.Addr())
	}
}

func TestClusterClientRecoversFromLeaderlessWindow(t *testing.T) {
	// A node that is leaderless for the first few polls, then advertises a
	// dialable leader, must be followed once the leader appears — the transient
	// election converges within the retry budget.
	leader := startFakeNode(t, func(argv []resp.Value) resp.Value { return resp.OK() })
	var polls int64
	follower := startFakeNode(t, func(argv []resp.Value) resp.Value {
		if atomic.AddInt64(&polls, 1) <= 2 {
			return resp.Error("NOTLEADER no leader elected") // leaderless window
		}
		return resp.Error("NOTLEADER " + leader.addr()) // leader now known
	})

	cc, err := DialCluster(follower.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	fastRedirect(cc)

	v, err := cc.Do("SET", "k", "v")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if v.Kind != resp.KindSimpleString || v.Str != "OK" {
		t.Fatalf("reply = %+v, want +OK after the election settles", v)
	}
	if cc.Addr() != leader.addr() {
		t.Fatalf("connected to %s, want leader %s", cc.Addr(), leader.addr())
	}
}
