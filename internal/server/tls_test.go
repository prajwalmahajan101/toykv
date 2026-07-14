package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// testTLSConfig generates an in-memory self-signed cert and returns the
// server config plus a client config whose root pool trusts it, so the
// handshake verifies a real chain instead of skipping verification.
func testTLSConfig(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "toykv-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	server = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	client = &tls.Config{RootCAs: pool, ServerName: "localhost"}
	return server, client
}

// setupTLSServer constructs a Server with a TLS listener config.
func setupTLSServer(t *testing.T) (*Server, *tls.Config) {
	t.Helper()
	serverConf, clientConf := testTLSConfig(t)
	s, err := New(Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(),
		TLS:   serverConf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, clientConf
}

func TestTLS_HandshakeAndRoundTrip(t *testing.T) {
	s, clientConf := setupTLSServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, err := tls.Dial("tcp", s.Addr(), clientConf)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	r, w := resp.NewReader(c), resp.NewWriter(c)

	writeCmd(t, w, "PING")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "PONG" {
		t.Fatalf("PING over TLS: got %+v, want +PONG", reply)
	}
	writeCmd(t, w, "SET", "k", "v")
	if reply := readReply(t, r); reply.Kind != resp.KindSimpleString || reply.Str != "OK" {
		t.Fatalf("SET over TLS: got %+v, want +OK", reply)
	}
	writeCmd(t, w, "GET", "k")
	if reply := readReply(t, r); reply.Kind != resp.KindBulkString || string(reply.Bytes) != "v" {
		t.Fatalf("GET over TLS: got %+v, want \"v\"", reply)
	}
}

// TestTLS_PlaintextClientRejected: a non-TLS client against the TLS
// listener must fail cleanly, not hang or crash the server.
func TestTLS_PlaintextClientRejected(t *testing.T) {
	s, clientConf := setupTLSServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	// A plaintext RESP command is TLS-handshake garbage; the server drops
	// the conn, so the read must return an error (EOF/reset), never a reply.
	if _, err := c.Write([]byte("*1\r\n$4\r\nPING\r\n")); err == nil {
		buf := make([]byte, 64)
		if n, err := c.Read(buf); err == nil {
			t.Fatalf("plaintext client got %d bytes (%q), want connection failure", n, buf[:n])
		}
	}

	// The server must still serve a proper TLS client afterwards.
	tc, err := tls.Dial("tcp", s.Addr(), clientConf)
	if err != nil {
		t.Fatalf("tls dial after plaintext attempt: %v", err)
	}
	defer func() { _ = tc.Close() }()
	r, w := resp.NewReader(tc), resp.NewWriter(tc)
	writeCmd(t, w, "PING")
	if reply := readReply(t, r); reply.Str != "PONG" {
		t.Fatalf("PING after plaintext attempt: got %+v", reply)
	}
}

// TestTLS_MinVersionEnforced: a client capped at TLS 1.1 must fail the
// handshake against the MinVersion TLS 1.2 listener.
func TestTLS_MinVersionEnforced(t *testing.T) {
	s, clientConf := setupTLSServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() { cancel(); <-errCh }()

	capped := clientConf.Clone()
	capped.MinVersion = tls.VersionTLS10
	capped.MaxVersion = tls.VersionTLS11
	if c, err := tls.Dial("tcp", s.Addr(), capped); err == nil {
		_ = c.Close()
		t.Fatal("TLS 1.1 handshake succeeded, want failure (MinVersion 1.2)")
	} else if !strings.Contains(err.Error(), "protocol version") {
		t.Logf("handshake failed as expected with: %v", err)
	}
}

// TestTLS_DrainOnCancel: ctx-cancel with an open TLS conn still shuts
// down cleanly (Run returns nil).
func TestTLS_DrainOnCancel(t *testing.T) {
	s, clientConf := setupTLSServer(t)
	_, cancel, errCh := runServer(t, s)

	c, err := tls.Dial("tcp", s.Addr(), clientConf)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	r, w := resp.NewReader(c), resp.NewWriter(c)
	writeCmd(t, w, "PING")
	if reply := readReply(t, r); reply.Str != "PONG" {
		t.Fatalf("PING: got %+v", reply)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v on cancel with open TLS conn, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not drain within 5s of cancel")
	}
}
