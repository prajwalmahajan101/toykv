package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// startTLSServer launches the shipped binary with a freshly generated
// self-signed pair and returns the server plus a client tls.Config whose
// root pool trusts the cert (real chain verification, no skip).
func startTLSServer(t *testing.T) (*Server, *tls.Config, string) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile := WriteSelfSignedPair(t, dir)
	s := StartServer(t, ServerOpts{TLSCert: certFile, TLSKey: keyFile})

	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("append cert to pool")
	}
	return s, &tls.Config{RootCAs: pool, ServerName: "localhost"}, certFile
}

// TestTLS_GoRedisRoundTrip: the ROADMAP §M12 TLS exit criterion via
// go-redis — handshake completes and commands round-trip.
func TestTLS_GoRedisRoundTrip(t *testing.T) {
	s, clientConf, _ := startTLSServer(t)
	ctx := context.Background()

	c := redis.NewClient(&redis.Options{
		Addr:        s.Addr,
		TLSConfig:   clientConf,
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
		MaxRetries:  -1,
	})
	defer func() { _ = c.Close() }()

	if got, err := c.Ping(ctx).Result(); err != nil || got != "PONG" {
		t.Fatalf("PING over TLS: got %q err %v\nserver stderr:\n%s", got, err, s.Stderr())
	}
	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET over TLS: %v", err)
	}
	if got, err := c.Get(ctx, "k").Result(); err != nil || got != "v" {
		t.Fatalf("GET over TLS: got %q err %v", got, err)
	}
}

// TestTLS_RedisCLI: `redis-cli --tls --cacert …` completes a handshake
// and round-trips — the literal exit-criterion command line.
func TestTLS_RedisCLI(t *testing.T) {
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not on PATH; CI installs redis-tools to exercise this")
	}
	s, _, certFile := startTLSServer(t)

	if got := runRedisCLI(t, s.Addr, "--tls", "--cacert", certFile, "PING"); got != "PONG" {
		t.Fatalf("redis-cli --tls PING = %q, want PONG\nserver stderr:\n%s", got, s.Stderr())
	}
	if got := runRedisCLI(t, s.Addr, "--tls", "--cacert", certFile, "SET", "k", "v"); got != "OK" {
		t.Errorf("redis-cli --tls SET = %q, want OK", got)
	}
	if got := runRedisCLI(t, s.Addr, "--tls", "--cacert", certFile, "GET", "k"); got != "v" {
		t.Errorf("redis-cli --tls GET = %q, want v", got)
	}
}

// TestTLS_WithAuth: TLS and requirepass compose — the deployable
// posture M12 exists to enable.
func TestTLS_WithAuth(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := WriteSelfSignedPair(t, dir)
	s := StartServer(t, ServerOpts{RequirePass: "s3cret", TLSCert: certFile, TLSKey: keyFile})

	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)

	ctx := context.Background()
	c := redis.NewClient(&redis.Options{
		Addr:        s.Addr,
		Password:    "s3cret",
		TLSConfig:   &tls.Config{RootCAs: pool, ServerName: "localhost"},
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
		MaxRetries:  -1,
	})
	defer func() { _ = c.Close() }()

	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET over TLS+auth: %v\nserver stderr:\n%s", err, s.Stderr())
	}
	if got, err := c.Get(ctx, "k").Result(); err != nil || got != "v" {
		t.Fatalf("GET over TLS+auth: got %q err %v", got, err)
	}
}
