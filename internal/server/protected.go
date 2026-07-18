package server

import (
	"fmt"
	"net"
	"strings"
)

// Protected mode (M15) — the safe-by-default deployment contract that earns
// the 2.0.0 major. When enabled (the default), the server refuses to *start*
// on a non-loopback bind that has neither a password (requirepass) nor TLS
// configured, exiting with a message that names the fix. This flips v1's
// implicit "bind anywhere, serve anyone" contract. Loopback binds and
// auth'd/TLS binds are unaffected. See ADR-0016.
//
// This is a deliberate deviation from Redis, which still *accepts* the
// connection and only refuses non-loopback *commands*; toykv surfaces the
// unsafe posture at boot instead of per-command.

// ParseProtectedMode interprets a -protected-mode flag / Config value. The
// empty string defaults to enabled — the safe default, so a zero-value
// Config is protected. Accepts yes|on|true|1 and no|off|false|0
// (case-insensitive); any other value is an error.
func ParseProtectedMode(s string) (enabled bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "yes", "on", "true", "1":
		return true, nil
	case "no", "off", "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid -protected-mode %q (want yes|no)", s)
	}
}

// checkProtectedMode returns a refusal error when protected mode forbids
// this bind, or nil to allow startup. A bad mode value is also an error.
func checkProtectedMode(addr, requirePass string, tlsOn bool, mode string) error {
	enabled, err := ParseProtectedMode(mode)
	if err != nil {
		return err
	}
	// Auth or TLS makes any bind safe; a disabled mode opts out entirely.
	if !enabled || requirePass != "" || tlsOn {
		return nil
	}
	loopback, err := bindIsLoopback(addr)
	if err != nil {
		return fmt.Errorf("protected mode: cannot parse -addr %q: %w", addr, err)
	}
	if loopback {
		return nil
	}
	return fmt.Errorf(
		"protected mode: refusing to start on non-loopback address %q without authentication or TLS. "+
			"Fix by one of: set -requirepass, configure -tls-cert/-tls-key, bind a loopback address "+
			"(e.g. 127.0.0.1), or pass -protected-mode no to override",
		addr,
	)
}

// bindIsLoopback reports whether addr binds only the loopback interface.
// The policy is fail-safe: an empty or unspecified host (":6390",
// "0.0.0.0", "::") binds all interfaces and counts as non-loopback (the
// dangerous case); "localhost" and any loopback IP count as loopback; a
// hostname is resolved and counts as loopback only when *every* resolved
// address is loopback (an unresolvable host is treated as non-loopback).
func bindIsLoopback(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port (or malformed): treat the whole string as the host.
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false, nil // ":6390" — all interfaces
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return false, nil // 0.0.0.0 / :: — all interfaces
		}
		return ip.IsLoopback(), nil
	}
	// A hostname: resolve. Loopback only if it resolves purely to loopback.
	ips, lookupErr := net.LookupIP(host)
	if lookupErr != nil || len(ips) == 0 {
		return false, nil // fail closed
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}
