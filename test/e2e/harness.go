// Package e2e drives the shipped toykv binaries (server, cli) as subprocesses
// for end-to-end protocol-compat tests. The unit suites (internal/...) exercise
// in-process code paths; this suite proves the actual artifacts users run.
package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

var sigTerm = syscall.SIGTERM

// Binaries holds absolute paths to the e2e-built server and cli binaries.
// Populated by BuildBinaries from TestMain so every test reuses the same build.
type Binaries struct {
	Server string
	CLI    string
}

var builtBinaries Binaries

// BuildBinaries compiles cmd/toykv and cmd/toykv-cli into a temp dir and
// returns their paths. Intended to be called once from TestMain.
func BuildBinaries() (Binaries, func(), error) {
	tmp, err := os.MkdirTemp("", "toykv-e2e-bin-*")
	if err != nil {
		return Binaries{}, func() {}, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		cleanup()
		return Binaries{}, func() {}, err
	}

	for _, b := range []struct {
		pkg string
		out *string
	}{
		{pkg: "./cmd/toykv", out: new(string)},
		{pkg: "./cmd/toykv-cli", out: new(string)},
	} {
		name := filepath.Base(b.pkg) + suffix
		path := filepath.Join(tmp, name)
		cmd := exec.Command("go", "build", "-o", path, b.pkg)
		cmd.Dir = repoRoot
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			cleanup()
			return Binaries{}, func() {}, fmt.Errorf("go build %s: %w\n%s", b.pkg, err, stderr.String())
		}
		*b.out = path
	}

	builtBinaries = Binaries{
		Server: filepath.Join(tmp, "toykv"+suffix),
		CLI:    filepath.Join(tmp, "toykv-cli"+suffix),
	}
	return builtBinaries, cleanup, nil
}

// findRepoRoot walks up from the current file location to the repo root
// (identified by go.mod). Works regardless of where `go test` is invoked from.
func findRepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("go.mod not found above harness.go")
}

// ServerOpts configures a subprocess server launched by StartServer.
type ServerOpts struct {
	// Dir is the AOF directory. If empty, a t.TempDir is used.
	Dir string
	// AppendFsync is the -appendfsync flag. Defaults to "no" for test speed.
	AppendFsync string
	// RequirePass sets -requirepass. Readiness still probes via plaintext
	// PING — PING is whitelisted for unauthenticated connections.
	RequirePass string
	// TLSCert / TLSKey set -tls-cert / -tls-key (paths to PEM files; see
	// WriteSelfSignedPair). When set, readiness probes over TLS.
	TLSCert string
	TLSKey  string
	// ExtraArgs are appended to the server command line.
	ExtraArgs []string
	// BindHost overrides the bind host (default "127.0.0.1"); a free port is
	// still chosen and readiness always probes 127.0.0.1:<port>, so an
	// all-interfaces host ("0.0.0.0") works. Used by protected-mode tests
	// that need a non-loopback bind that still starts.
	BindHost string
}

// Server is a running toykv subprocess.
type Server struct {
	Addr string
	Dir  string

	cmd    *exec.Cmd
	stderr bytes.Buffer
	t      *testing.T
	once   sync.Once
}

// StartServer launches the built server binary on a free port and waits until
// it accepts PING. The server is automatically stopped at test cleanup.
func StartServer(t *testing.T, opts ServerOpts) *Server {
	t.Helper()
	if builtBinaries.Server == "" {
		t.Fatalf("e2e: server binary not built; call BuildBinaries in TestMain")
	}

	port := freePort(t)
	probeAddr := "127.0.0.1:" + strconv.Itoa(port)
	bindHost := opts.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	addr := net.JoinHostPort(bindHost, strconv.Itoa(port))

	dir := opts.Dir
	if dir == "" {
		dir = t.TempDir()
	}
	fsync := opts.AppendFsync
	if fsync == "" {
		fsync = "no"
	}

	args := []string{"-addr", addr, "-dir", dir, "-appendfsync", fsync, "-log-level", "warn"}
	if opts.RequirePass != "" {
		args = append(args, "-requirepass", opts.RequirePass)
	}
	if opts.TLSCert != "" || opts.TLSKey != "" {
		args = append(args, "-tls-cert", opts.TLSCert, "-tls-key", opts.TLSKey)
	}
	args = append(args, opts.ExtraArgs...)

	//nolint:gosec // G204: builtBinaries.Server is a path we built ourselves in TestMain.
	cmd := exec.Command(builtBinaries.Server, args...)
	s := &Server{Addr: probeAddr, Dir: dir, cmd: cmd, t: t}
	cmd.Stderr = &s.stderr
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	if err := waitReady(probeAddr, 3*time.Second, opts.TLSCert != ""); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("server not ready on %s: %v\nstderr:\n%s", probeAddr, err, s.stderr.String())
	}

	t.Cleanup(s.Stop)
	return s
}

// Stop sends SIGTERM and waits up to 2s. SIGKILL on timeout.
func (s *Server) Stop() {
	s.once.Do(func() {
		if s.cmd == nil || s.cmd.Process == nil {
			return
		}
		_ = s.cmd.Process.Signal(sigTerm)
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	})
}

// Stderr returns the server's accumulated stderr (handy on failure).
func (s *Server) Stderr() string { return s.stderr.String() }

// RunServerExpectRefusal launches the server binary bound to bindHost (a
// non-loopback host, e.g. "0.0.0.0") with extraArgs and asserts it exits
// non-zero before serving — the protected-mode startup refusal. It returns
// the process stderr so the caller can check the message. Persistence is
// disabled (-dir "").
func RunServerExpectRefusal(t *testing.T, bindHost string, extraArgs ...string) string {
	t.Helper()
	if builtBinaries.Server == "" {
		t.Fatalf("e2e: server binary not built; call BuildBinaries in TestMain")
	}
	port := freePort(t)
	addr := net.JoinHostPort(bindHost, strconv.Itoa(port))
	args := append([]string{"-addr", addr, "-dir", "", "-log-level", "warn"}, extraArgs...)

	//nolint:gosec // G204: builtBinaries.Server is a path we built ourselves in TestMain.
	cmd := exec.Command(builtBinaries.Server, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("server started on %s but a protected-mode refusal was expected;\nstderr:\n%s", addr, stderr.String())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("server did not exit on %s (expected refusal);\nstderr:\n%s", addr, stderr.String())
	}
	return stderr.String()
}

// freePort probes a free TCP port by binding 127.0.0.1:0 and closing.
// There is a tiny race window before the subprocess re-binds, but in practice
// this is reliable for tests on a quiet developer/CI machine.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// waitReady dials addr and sends an inline PING until +PONG arrives or
// deadline. PING is whitelisted for unauthenticated connections, so the
// probe works against a requirepass server. Against a TLS listener the
// probe dials TLS (InsecureSkipVerify — test-only readiness, not a
// chain-verification assertion).
func waitReady(addr string, timeout time.Duration, useTLS bool) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		var conn net.Conn
		var err error
		if useTLS {
			d := &net.Dialer{Timeout: 200 * time.Millisecond}
			//nolint:gosec // G402: readiness probe against our own test server.
			conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		} else {
			conn, err = net.DialTimeout("tcp", addr, 200*time.Millisecond)
		}
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
			_ = conn.Close()
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err == nil && n >= 5 && bytes.HasPrefix(buf[:n], []byte("+PONG")) {
			return nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timed out waiting for +PONG")
	}
	return lastErr
}

// WriteSelfSignedPair generates a self-signed ECDSA certificate valid
// for localhost/127.0.0.1 and writes cert.pem + key.pem into dir
// (typically t.TempDir()). Real files, because the server flags and
// redis-cli both take paths. Returns the two paths.
func WriteSelfSignedPair(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "toykv-e2e"},
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

// CLIResult is the captured outcome of one toykv-cli subprocess run.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunCLI execs the built toykv-cli with args and stdin, capturing stdout/stderr
// and the exit code. timeout caps total wall time.
func RunCLI(t *testing.T, addr string, stdin string, args ...string) CLIResult {
	t.Helper()
	if builtBinaries.CLI == "" {
		t.Fatalf("e2e: cli binary not built; call BuildBinaries in TestMain")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := append([]string{"-addr", addr}, args...)
	//nolint:gosec // G204: builtBinaries.CLI is a path we built ourselves in TestMain.
	cmd := exec.CommandContext(ctx, builtBinaries.CLI, full...)
	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run cli: %v\nstderr:\n%s", err, errb.String())
		}
	}
	return CLIResult{Stdout: out.String(), Stderr: errb.String(), ExitCode: code}
}
