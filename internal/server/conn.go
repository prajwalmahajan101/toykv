package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// handleConn serves a single client connection until EOF or error. It also
// owns the per-connection telemetry lifecycle (§1.2): the connection
// counters, the active gauge, the lifetime histogram, the by-protocol
// gauge, and — for TLS listeners — the handshake outcome.
func (s *Server) handleConn(c net.Conn) {
	defer func() { _ = c.Close() }()
	m := s.tel.Metrics
	ctx := context.Background() // T8 threads a real per-connection span context
	start := time.Now()
	m.Connections.Add(ctx, 1)

	// Force the TLS handshake up front so its outcome is observable and a
	// bad handshake fails fast, rather than surfacing later as a generic
	// protocol error. A plaintext listener has no *tls.Conn, so this is
	// skipped entirely.
	if tc, ok := c.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			m.TLSHandshakes.Add(ctx, 1, resultAttr("error"))
			s.log.Debug("tls handshake failed", "remote", remoteAddr(c), "err", err)
			return
		}
		m.TLSHandshakes.Add(ctx, 1, resultAttr("success"))
	}

	r := resp.NewReader(c)
	w := resp.NewWriter(c)
	cs := newConnState(s.connID.Add(1), s.cfg.RequirePass == "")
	cs.ctx = ctx // T8 replaces this with the connection-span context
	m.ConnectionsActive.Add(ctx, 1)
	m.ClientsByProtocol.Add(ctx, 1, protoAttr(cs.proto))
	defer func() {
		m.ConnectionsActive.Add(ctx, -1)
		// cs.proto reflects any HELLO upgrade, keeping the by-protocol gauge
		// balanced against the accept-time increment.
		m.ClientsByProtocol.Add(ctx, -1, protoAttr(cs.proto))
		m.ConnectionDuration.Record(ctx, time.Since(start).Seconds())
	}()

	for {
		argv, err := r.ReadCommand()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// Send one error frame so the client sees what happened, then
			// drop the conn. We do not try to recover from a corrupted stream.
			// A simple error is byte-identical across RESP2/RESP3, so it is
			// safe to send at the default protocol even after HELLO 3.
			m.ProtocolErrors.Add(ctx, 1)
			s.log.Debug("protocol error", "remote", remoteAddr(c), "err", err)
			_ = w.WriteFrame(resp.Error("ERR Protocol error"))
			_ = w.Flush()
			return
		}
		reply := s.observeCommand(cs, argv)
		if err := w.WriteFrameProto(reply, cs.proto); err != nil {
			s.log.Debug("write reply failed", "remote", remoteAddr(c), "err", err)
			return
		}
		if err := w.Flush(); err != nil {
			s.log.Debug("flush failed", "remote", remoteAddr(c), "err", err)
			return
		}
	}
}

// remoteAddr returns the peer address as a string, defaulting to
// "unknown" when the connection is a net.Pipe (used in tests).
func remoteAddr(c net.Conn) string {
	if a := c.RemoteAddr(); a != nil {
		return a.String()
	}
	return "unknown"
}
