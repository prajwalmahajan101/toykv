// Package client is the shared RESP2 client used by toykv-cli and
// toykv-tui (and, post-M8, the toykv-go SDK). It holds a single
// net.Conn and serialises Do calls behind a mutex; pipelining is out
// of scope for v1. See docs/LLD.md §6.7 and docs/adr/0007-sdk-strategy.md.
package client

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// ErrClosed is returned by Do/DoBytes after Close has been called or
// after a prior Do observed a transport error. Once a Client is in
// this state it is unusable; callers must Dial a fresh one.
var ErrClosed = errors.New("client: closed")

// Client is a single-connection RESP2 client. Concurrent Do calls are
// serialised; a transport error fails the call and marks the client
// closed.
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	r      *resp.Reader
	w      *resp.Writer
	closed bool
}

// Dial opens a TCP connection to addr and returns a ready Client.
func Dial(addr string) (*Client, error) {
	return DialTimeout(addr, 0)
}

// DialTimeout is Dial with a connect deadline. A zero timeout means
// no deadline (use the OS default).
func DialTimeout(addr string, timeout time.Duration) (*Client, error) {
	var (
		conn net.Conn
		err  error
	)
	if timeout > 0 {
		conn, err = net.DialTimeout("tcp", addr, timeout)
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

// newClient wraps an already-connected transport. Exported as
// NewConn for tests that drive the client over net.Pipe.
func newClient(conn net.Conn) *Client {
	return &Client{
		conn: conn,
		r:    resp.NewReader(conn),
		w:    resp.NewWriter(conn),
	}
}

// NewConn returns a Client speaking RESP2 over the supplied transport.
// Intended for tests; production callers should use Dial.
func NewConn(conn net.Conn) *Client { return newClient(conn) }

// Do encodes argv as a RESP2 array of bulk-strings, flushes, and
// reads the next frame as the reply.
func (c *Client) Do(argv ...string) (resp.Value, error) {
	bargv := make([][]byte, len(argv))
	for i, s := range argv {
		bargv[i] = []byte(s)
	}
	return c.DoBytes(bargv)
}

// DoBytes is Do for binary-safe argv (callers reuse the supplied
// slices until the call returns).
func (c *Client) DoBytes(argv [][]byte) (resp.Value, error) {
	if len(argv) == 0 {
		return resp.Value{}, fmt.Errorf("client: empty argv")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return resp.Value{}, ErrClosed
	}

	frame := resp.Value{Kind: resp.KindArray, Array: make([]resp.Value, len(argv))}
	for i, b := range argv {
		frame.Array[i] = resp.Bulk(b)
	}

	if err := c.w.WriteFrame(frame); err != nil {
		c.failLocked()
		return resp.Value{}, err
	}
	if err := c.w.Flush(); err != nil {
		c.failLocked()
		return resp.Value{}, err
	}

	v, err := c.r.ReadFrame()
	if err != nil {
		c.failLocked()
		return resp.Value{}, err
	}
	return v, nil
}

// Close closes the underlying connection. Safe to call multiple times.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// failLocked marks the client closed and tears down the conn after a
// transport error. Caller must hold c.mu.
func (c *Client) failLocked() {
	if c.closed {
		return
	}
	c.closed = true
	_ = c.conn.Close()
}
