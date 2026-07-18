package e2e

import (
	"context"
	"strings"
	"testing"
)

// TestProtectedMode_RefusesUnsafeBind is the M15 owned risk test: a server
// bound to a non-loopback address with neither requirepass nor TLS must
// refuse to start, exiting non-zero with the documented message.
func TestProtectedMode_RefusesUnsafeBind(t *testing.T) {
	stderr := RunServerExpectRefusal(t, "0.0.0.0")
	if !strings.Contains(stderr, "protected mode") || !strings.Contains(stderr, "refusing to start") {
		t.Fatalf("refusal stderr missing the documented message:\n%s", stderr)
	}
}

// TestProtectedMode_AllowsSafeVariants proves each escape hatch starts
// cleanly on a non-loopback bind: requirepass, an explicit override, and
// (implicitly, via every other test) a loopback address.
func TestProtectedMode_AllowsSafeVariants(t *testing.T) {
	ctx := context.Background()

	t.Run("requirepass", func(t *testing.T) {
		s := StartServer(t, ServerOpts{BindHost: "0.0.0.0", RequirePass: "s3cret"})
		c := newAuthClient(s.Addr, "s3cret", 2)
		defer func() { _ = c.Close() }()
		if err := c.Ping(ctx).Err(); err != nil {
			t.Fatalf("PING after auth: %v", err)
		}
	})

	t.Run("protected-mode override", func(t *testing.T) {
		s := StartServer(t, ServerOpts{BindHost: "0.0.0.0", ExtraArgs: []string{"-protected-mode", "no"}})
		c := newClient(s.Addr)
		defer func() { _ = c.Close() }()
		if err := c.Ping(ctx).Err(); err != nil {
			t.Fatalf("PING with override: %v", err)
		}
	})

	t.Run("loopback default", func(t *testing.T) {
		s := StartServer(t, ServerOpts{}) // 127.0.0.1, no auth — must start
		c := newClient(s.Addr)
		defer func() { _ = c.Close() }()
		if err := c.Ping(ctx).Err(); err != nil {
			t.Fatalf("PING on loopback: %v", err)
		}
	})
}
