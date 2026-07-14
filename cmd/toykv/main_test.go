package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedPair generates a self-signed ECDSA certificate and key
// and writes them as PEM files under dir, returning their paths.
func writeSelfSignedPair(t *testing.T, dir string) (certFile, keyFile string) {
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
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

func TestBuildTLSConfig(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSignedPair(t, dir)

	tests := []struct {
		name      string
		cert, key string
		wantNil   bool
		wantErr   bool
	}{
		{name: "both empty means plaintext", cert: "", key: "", wantNil: true},
		{name: "cert without key", cert: certFile, key: "", wantErr: true},
		{name: "key without cert", cert: "", key: keyFile, wantErr: true},
		{name: "unreadable pair", cert: filepath.Join(dir, "no.pem"), key: filepath.Join(dir, "nope.pem"), wantErr: true},
		{name: "valid pair", cert: certFile, key: keyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, err := buildTLSConfig(tt.cert, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildTLSConfig(%q, %q): want error, got nil", tt.cert, tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildTLSConfig(%q, %q): %v", tt.cert, tt.key, err)
			}
			if tt.wantNil {
				if conf != nil {
					t.Fatalf("want nil config for empty pair, got %+v", conf)
				}
				return
			}
			if conf == nil {
				t.Fatal("want non-nil config for valid pair")
			}
			if conf.MinVersion != tls.VersionTLS12 {
				t.Errorf("MinVersion = %#x, want TLS 1.2 (%#x)", conf.MinVersion, tls.VersionTLS12)
			}
			if len(conf.Certificates) != 1 {
				t.Errorf("Certificates count = %d, want 1", len(conf.Certificates))
			}
		})
	}
}
