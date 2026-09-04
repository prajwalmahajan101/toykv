// Command toykv is the in-memory key-value store server.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

const usage = `toykv — in-memory key-value store server

usage:
  toykv [flags]

flags:
  -addr        string  listen address (default ":6390")
  -dir         string  data directory for the AOF (default "./data"; "" disables persistence)
  -appendfsync string  fsync policy: always|everysec|no (default "always")
  -log-level   string  log level: debug|info|warn|error (default "info")
  -requirepass string  password clients must AUTH with ("" disables authentication)
  -tls-cert    string  path to the TLS certificate (PEM); requires -tls-key
  -tls-key     string  path to the TLS private key (PEM); requires -tls-cert
  -protected-mode string  refuse a non-loopback bind without auth/TLS: yes|no (default "yes")
  -replicate           enable the Raft-replicated command path (M18 single-node) (default false)
  -node-id     string  Raft node id; used only with -replicate (default "n1")
  -otel-endpoint    string  OTLP collector endpoint host:port ("" disables all telemetry)
  -otel-protocol    string  OTLP transport: grpc|http (default "grpc")
  -otel-service-name string service.name reported to telemetry (default "toykv")
  -otel-sampling    float   trace sampling ratio [0,1]; errors always sampled (default 0.05)
  -otel-capture-keys        record a salted-hash of the key on store spans (default false)
  -h, --help           show this help and exit
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stdout, usage) }
	var (
		addr        = flag.String("addr", ":6390", "listen address")
		dir         = flag.String("dir", "./data", "data directory for the AOF; \"\" disables persistence")
		appendfsync = flag.String("appendfsync", "always", "fsync policy: always|everysec|no")
		logLevel    = flag.String("log-level", "info", "log level: debug|info|warn|error")
		requirePass = flag.String("requirepass", "", "password clients must AUTH with; \"\" disables authentication")
		tlsCert     = flag.String("tls-cert", "", "path to the TLS certificate (PEM); requires -tls-key")
		tlsKey      = flag.String("tls-key", "", "path to the TLS private key (PEM); requires -tls-cert")
		protected   = flag.String("protected-mode", "yes", "refuse a non-loopback bind without auth/TLS: yes|no")

		replicate = flag.Bool("replicate", false, "enable the Raft-replicated command path (M18 single-node)")
		nodeID    = flag.String("node-id", "n1", "Raft node id; used only with -replicate")

		otelEndpoint    = flag.String("otel-endpoint", "", "OTLP collector endpoint host:port; \"\" disables telemetry")
		otelProtocol    = flag.String("otel-protocol", "grpc", "OTLP transport: grpc|http")
		otelService     = flag.String("otel-service-name", "toykv", "service.name reported to telemetry")
		otelSampling    = flag.Float64("otel-sampling", 0.05, "trace sampling ratio [0,1]; errors always sampled")
		otelCaptureKeys = flag.Bool("otel-capture-keys", false, "record a salted-hash of the key on store spans")
	)
	flag.Parse()

	level, err := parseLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	policy, err := aof.ParsePolicy(*appendfsync)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	tlsConf, err := buildTLSConfig(*tlsCert, *tlsKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Validate the flag value here (usage error → exit 2), distinct from
	// the unsafe-bind refusal that server.New raises (deployment error →
	// exit 1). Log the opt-out so a disabled safety net is never silent.
	protectedOn, err := server.ParseProtectedMode(*protected)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if !protectedOn {
		log.Warn("protected mode disabled via -protected-mode no; non-loopback binds will not be refused")
	}

	// Telemetry is initialized before the server so its providers/globals
	// are in place when the server registers observable-gauge callbacks.
	// A malformed OTLP config (e.g. unknown -otel-protocol) is a usage
	// error → exit 2; an unreachable endpoint is not — it drops silently.
	providers, err := telemetry.Init(context.Background(), telemetry.Config{
		Endpoint:    *otelEndpoint,
		Protocol:    *otelProtocol,
		ServiceName: *otelService,
		Version:     server.Version(),
		Sampling:    *otelSampling,
		CaptureKeys: *otelCaptureKeys,
		Log:         log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// Tee console logs to OTLP (Loki) when telemetry is on; a no-op wrap
	// otherwise. Records logged with a span context carry the trace id.
	log = slog.New(telemetry.NewSlogHandler(log.Handler(), providers))
	// Registered before s.Close so teardown order is: stop serving →
	// flush+close AOF (s.Close) → flush+close telemetry exporters.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := providers.Shutdown(shutdownCtx); err != nil {
			log.Warn("telemetry shutdown", "err", err)
		}
	}()

	s, err := server.New(server.Config{
		Addr:          *addr,
		Log:           log,
		Store:         store.New(),
		Dir:           *dir,
		FsyncPolicy:   policy,
		RequirePass:   *requirePass,
		TLS:           tlsConf,
		ProtectedMode: *protected,
		Telemetry:     providers,
		Replicate:     *replicate,
		NodeID:        *nodeID,
	})
	if err != nil {
		log.Error("server init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := s.Run(ctx); err != nil {
		log.Error("server run failed", "err", err)
		os.Exit(1)
	}
}

// buildTLSConfig turns the -tls-cert / -tls-key flag pair into a
// *tls.Config. Both empty means plaintext (nil config); exactly one set
// is a configuration error surfaced before the server starts.
func buildTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	switch {
	case certFile == "" && keyFile == "":
		return nil, nil
	case certFile == "":
		return nil, errors.New("-tls-key given without -tls-cert")
	case keyFile == "":
		return nil, errors.New("-tls-cert given without -tls-key")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS key pair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("invalid -log-level (want debug|info|warn|error)")
	}
}
