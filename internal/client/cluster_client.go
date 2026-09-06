package client

import (
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// Cluster-redirect defaults. maxRedirects bounds how many NOTLEADER hops a
// single call will chase before giving up — a finite cap so a redirect storm
// during an election converges to an error instead of looping forever. The
// backoff spaces successive redials so a flapping cluster is not hammered.
const (
	defaultMaxRedirects = 8
	defaultBackoffBase  = 10 * time.Millisecond
	defaultBackoffMax   = 500 * time.Millisecond
)

// notLeaderPrefix is the reply prefix a follower sends to bounce a client to
// the leader. The token after it is a dialable host:port when the leader
// advertised a client address (server: notLeaderReply); otherwise it is
// operator-readable text the client surfaces unchanged.
const notLeaderPrefix = "NOTLEADER "

// ClusterClient wraps a single-connection Client with transparent leader
// redirect. Do/DoBytes forward to the inner client; when a node answers a
// keyed read or a write with a dialable NOTLEADER reply, the ClusterClient
// re-dials the hinted leader (bounded retry with backoff) and replays the call.
// Against a standalone server no NOTLEADER is ever emitted, so it behaves
// exactly like a bare Client. Calls are serialised by the same one-conn
// discipline as Client; concurrent use is safe but not pipelined.
type ClusterClient struct {
	mu           sync.Mutex
	inner        *Client
	addr         string
	dialTimeout  time.Duration
	maxRedirects int
	backoffBase  time.Duration
	backoffMax   time.Duration
	rng          *rand.Rand
}

// DialCluster opens a redirect-following client to addr. It is the cluster-aware
// counterpart to Dial: the same entry point for standalone and clustered
// servers.
func DialCluster(addr string) (*ClusterClient, error) {
	return DialClusterTimeout(addr, 0)
}

// DialClusterTimeout is DialCluster with a per-dial connect deadline applied to
// the initial dial and every redirect redial.
func DialClusterTimeout(addr string, timeout time.Duration) (*ClusterClient, error) {
	inner, err := DialTimeout(addr, timeout)
	if err != nil {
		return nil, err
	}
	return &ClusterClient{
		inner:        inner,
		addr:         addr,
		dialTimeout:  timeout,
		maxRedirects: defaultMaxRedirects,
		backoffBase:  defaultBackoffBase,
		backoffMax:   defaultBackoffMax,
		//nolint:gosec // G404: backoff jitter only decorrelates concurrent retries; not security-sensitive.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// Do encodes argv as bulk strings and runs it with redirect following.
func (cc *ClusterClient) Do(argv ...string) (resp.Value, error) {
	bargv := make([][]byte, len(argv))
	for i, s := range argv {
		bargv[i] = []byte(s)
	}
	return cc.DoBytes(bargv)
}

// DoBytes runs a binary-safe command, retrying past NOTLEADER replies up to
// maxRedirects times. A dialable hint (the leader advertised a client address)
// re-dials that leader; a non-dialable NOTLEADER — a leaderless window mid-
// election — re-polls the current node after a backoff, so a transient election
// converges instead of failing the call. A transport error returns immediately;
// so does any non-NOTLEADER reply. When the retry budget is exhausted the last
// NOTLEADER reply is returned as-is so the caller sees the cluster never settled.
func (cc *ClusterClient) DoBytes(argv [][]byte) (resp.Value, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	for attempt := 0; ; attempt++ {
		v, err := cc.inner.DoBytes(argv)
		if err != nil {
			return resp.Value{}, err
		}
		if !isNotLeader(v) || attempt >= cc.maxRedirects {
			return v, nil
		}
		cc.sleepBackoff(attempt)
		if hint, dialable := dialableHint(v); dialable {
			if err := cc.redialLocked(hint); err != nil {
				return resp.Value{}, err
			}
		}
		// Non-dialable: keep the current conn and re-poll after the backoff.
	}
}

// Close closes the underlying connection.
func (cc *ClusterClient) Close() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.inner == nil {
		return nil
	}
	return cc.inner.Close()
}

// Addr reports the address the client is currently connected to (it moves to
// the leader as redirects are followed). Intended for tests and diagnostics.
func (cc *ClusterClient) Addr() string {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.addr
}

// redialLocked tears down the stale conn and dials the redirect target. The
// caller has already backed off. Caller must hold cc.mu.
func (cc *ClusterClient) redialLocked(addr string) error {
	_ = cc.inner.Close()
	next, err := DialTimeout(addr, cc.dialTimeout)
	if err != nil {
		return fmt.Errorf("client: redirect to %s: %w", addr, err)
	}
	cc.inner = next
	cc.addr = addr
	return nil
}

// sleepBackoff waits base*2^attempt (capped) plus jitter before a redial so a
// redirect storm during an election does not hot-loop the cluster.
func (cc *ClusterClient) sleepBackoff(attempt int) {
	d := cc.backoffBase << attempt
	if d > cc.backoffMax || d <= 0 {
		d = cc.backoffMax
	}
	if d <= 0 {
		return
	}
	time.Sleep(d/2 + time.Duration(cc.rng.Int63n(int64(d/2)+1)))
}

// isNotLeader reports whether v is a NOTLEADER error reply in any form (dialable
// redirect, "leader is <id>" hint, or "no leader elected").
func isNotLeader(v resp.Value) bool {
	return v.Kind == resp.KindError && strings.HasPrefix(v.Str, notLeaderPrefix)
}

// dialableHint returns the leader's client address when a NOTLEADER reply
// carries one (the token after the prefix is a host:port). dialable is false for
// the operator-readable fallbacks ("no leader elected", "leader is <id> …"),
// which the client cannot connect to.
func dialableHint(v resp.Value) (addr string, dialable bool) {
	if !isNotLeader(v) {
		return "", false
	}
	hint := strings.TrimSpace(strings.TrimPrefix(v.Str, notLeaderPrefix))
	if _, _, err := net.SplitHostPort(hint); err != nil {
		return "", false
	}
	return hint, true
}
