package server

import (
	"context"
	"crypto/subtle"

	"go.opentelemetry.io/otel/trace"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// Redis 7 error strings, byte-for-byte. WRONGPASS is deliberately shared
// between a bad password and a non-"default" username so a client cannot
// probe which part failed.
const (
	errWrongPass = "WRONGPASS invalid username-password pair or user is disabled."
	//nolint:gosec // G101: a Redis wire error string, not a credential.
	errNoPassSet = "ERR Client sent AUTH, but no password is set. Did you mean AUTH <username> <password>?"
)

// authenticate verifies user/pass against the server's requirepass and,
// on success, marks the connection authenticated. Shared by AUTH and
// HELLO … AUTH so both entry points have identical semantics. The
// password comparison is constant-time; the username check is not — the
// only valid username is the public constant "default", so its timing
// leaks nothing secret.
func authenticate(s *Server, cs *connState, user, pass []byte) resp.Value {
	if s.cfg.RequirePass == "" {
		return resp.Error(errNoPassSet)
	}
	span := trace.SpanFromContext(cs.context())
	userOK := string(user) == "default"
	passOK := subtle.ConstantTimeCompare(pass, []byte(s.cfg.RequirePass)) == 1
	if !userOK || !passOK {
		s.tel.Metrics.AuthAttempts.Add(context.Background(), 1, resultAttr("wrongpass"))
		span.AddEvent("auth.failure") // never records the password (M12 no-oracle)
		s.log.DebugContext(cs.context(), "auth attempt", "result", "wrongpass")
		return resp.Error(errWrongPass)
	}
	cs.authenticated = true
	s.tel.Metrics.AuthAttempts.Add(context.Background(), 1, resultAttr("success"))
	span.AddEvent("auth.success")
	s.log.DebugContext(cs.context(), "auth attempt", "result", "success")
	return resp.String("OK")
}

// cmdAuth implements AUTH password | AUTH username password. The one-arg
// form is shorthand for the "default" user, matching Redis.
func cmdAuth(s *Server, cs *connState, argv [][]byte) resp.Value {
	user := []byte("default")
	pass := argv[1]
	if len(argv) == 3 {
		user = argv[1]
		pass = argv[2]
	}
	return authenticate(s, cs, user, pass)
}
