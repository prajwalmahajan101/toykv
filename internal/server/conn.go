package server

import (
	"errors"
	"io"
	"net"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// handleConn serves a single client connection until EOF or error.
func (s *Server) handleConn(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := resp.NewReader(c)
	w := resp.NewWriter(c)

	for {
		argv, err := r.ReadCommand()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// Send one error frame so the client sees what happened, then
			// drop the conn. We do not try to recover from a corrupted stream.
			s.log.Debug("protocol error", "remote", remoteAddr(c), "err", err)
			_ = w.WriteFrame(resp.Error("ERR Protocol error"))
			_ = w.Flush()
			return
		}
		reply := s.dispatch(argv)
		if err := w.WriteFrame(reply); err != nil {
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
