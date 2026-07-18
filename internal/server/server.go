package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// serverVersion is reported in the HELLO handshake's `version` field.
// It is a placeholder until the M15 release wires the ldflags build
// version through Config, matching the CLI/TUI `-version` plumbing.
const serverVersion = "2.0.0-dev"

// Version returns the server version string, exposed so the command layer
// (cmd/toykv) can stamp it onto the OpenTelemetry resource (service.version)
// without duplicating the constant.
func Version() string { return serverVersion }

// Config holds server configuration.
type Config struct {
	Addr        string               // TCP listen address, e.g. ":6390"
	Log         *slog.Logger         // structured logger; nil ⇒ slog.Default()
	Store       *store.Store         // backing key-value store; must be non-nil
	Dir         string               // AOF data directory; "" disables persistence
	FsyncPolicy aof.FsyncPolicy      // ignored when Dir == ""
	NowFunc     func() time.Time     // optional; defaults to time.Now. Drives TTL command timestamps.
	SweeperOpts store.SweeperOptions // optional; zero ⇒ store package defaults (1s / batch 20)
	RequirePass string               // "" ⇒ no authentication; otherwise the AUTH password
	TLS         *tls.Config          // nil ⇒ plaintext; otherwise wraps the listener
	// ProtectedMode gates the safe-by-default startup refusal (M15). "" or
	// "yes"/"on" ⇒ enabled (refuse a non-loopback bind without auth/TLS);
	// "no"/"off" ⇒ disabled. Any other value fails New. See protected.go.
	ProtectedMode string
	// Telemetry carries the initialized OpenTelemetry surface (M16). nil ⇒
	// a no-op surface is installed, so instrumentation calls are always
	// safe and cost nothing when telemetry is disabled.
	Telemetry *telemetry.Providers
}

// Server is the TCP listener and command dispatcher.
type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *store.Store
	aof     *aof.Writer // nil ⇒ persistence disabled
	sweeper *store.Sweeper
	nowFunc func() time.Time
	tel     *telemetry.Providers // never nil after New; no-op when disabled

	mu       sync.Mutex
	listener net.Listener
	wg       sync.WaitGroup
	closed   bool

	// startTime is stamped at construction and drives INFO's uptime.
	startTime time.Time
	// replayStats captures the AOF replay result (records/bytes/duration)
	// so INFO can report it. Zero value when persistence is disabled.
	replayStats aof.ReplayStats

	// connID assigns a monotonic id to each accepted connection, echoed
	// in the HELLO handshake. Starts at 0; the first connection gets 1.
	connID atomic.Uint64
	// clientCount is the number of connections currently being served,
	// reported by INFO as connected_clients. Incremented on accept,
	// decremented when the connection goroutine exits.
	clientCount atomic.Int64

	// rewriteMu guards rewriteInFlight. Held only across the flag
	// read-modify-write, never across the rewrite itself.
	rewriteMu       sync.Mutex
	rewriteInFlight bool
}

// now returns the current time according to the configured clock. Used
// by TTL commands so tests can inject a deterministic clock.
func (s *Server) now() time.Time { return s.nowFunc() }

// New constructs a Server. It does not open the listener; that happens
// in Run.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("server: Config.Addr must be set")
	}
	if cfg.Store == nil {
		return nil, errors.New("server: Config.Store must be set")
	}
	// Protected-mode refusal happens before AOF replay so an unsafe bind
	// never touches disk (and before the listener opens in Run).
	if err := checkProtectedMode(cfg.Addr, cfg.RequirePass, cfg.TLS != nil, cfg.ProtectedMode); err != nil {
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = time.Now
	}
	if cfg.Telemetry == nil {
		cfg.Telemetry = telemetry.Disabled()
	}
	// Hand the store the live telemetry handle so its keyspace/expiry
	// metrics (§1.3) and, later, spans (§3) are recorded. Safe before serving.
	cfg.Store.SetMetrics(cfg.Telemetry.Metrics)
	s := &Server{
		cfg:       cfg,
		log:       cfg.Log,
		store:     cfg.Store,
		nowFunc:   cfg.NowFunc,
		tel:       cfg.Telemetry,
		sweeper:   store.NewSweeper(cfg.Store, cfg.SweeperOpts),
		startTime: cfg.NowFunc(),
	}
	s.sweeper.SetTracer(cfg.Telemetry.Tracer) // sweeper.tick span (§3)

	if cfg.Dir != "" {
		// Replay first so any AOF parse failure is surfaced before we
		// open the listener. Replay uses an internal dispatch path that
		// applies commands directly to the store and never re-appends.
		stats, err := aof.Replay(cfg.Dir, s.replayApply)
		if err != nil {
			return nil, fmt.Errorf("server: aof replay: %w", err)
		}
		s.replayStats = stats
		// aof.replay span (§3): a startup root, recorded before Accept.
		_, replaySpan := s.tel.Tracer.Start(context.Background(), "aof.replay")
		replaySpan.SetAttributes(
			attribute.Int("records", stats.Records),
			attribute.Int64("bytes", stats.Bytes),
		)
		replaySpan.End()

		w, err := aof.Open(cfg.Dir, cfg.FsyncPolicy, aof.WithMetrics(cfg.Telemetry.Metrics))
		if err != nil {
			return nil, fmt.Errorf("server: aof open: %w", err)
		}
		s.aof = w
		s.recordReplayStats(stats)
		s.log.Info("aof ready",
			"dir", cfg.Dir,
			"fsync", cfg.FsyncPolicy.String(),
			"replay_records", stats.Records,
			"replay_bytes", stats.Bytes,
			"replay_duration", stats.Duration,
		)
	}
	if err := s.registerObservableGauges(); err != nil {
		s.log.Warn("telemetry: observable gauge registration failed", "err", err)
	}
	return s, nil
}

// replayApply runs a command from the AOF through the normal dispatch
// path. Because s.aof is still nil during replay, the mutating handlers
// no-op their appendIfLive — the same code serves both replay and live
// traffic without a second handler table.
func (s *Server) replayApply(argv [][]byte) error {
	// Replay never writes replies to a client, so the connState's proto is
	// irrelevant, but it must be authenticated so NOAUTH gating never
	// rejects a replayed record. HELLO is not a mutating command and never
	// appears in the AOF, so it is never replayed.
	reply := s.dispatch(&connState{authenticated: true}, argv)
	if reply.Kind == resp.KindError {
		return errors.New(reply.Str)
	}
	return nil
}

// Close drains the AOF (flush + fsync) and closes the file. It does not
// stop the listener — use the ctx passed to Run for that. Close is safe
// to call multiple times; subsequent calls are no-ops.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	w := s.aof
	s.aof = nil
	s.mu.Unlock()

	if w == nil {
		return nil
	}
	return w.Close()
}

// Addr returns the actual listen address (useful with ":0" in tests).
// Returns "" before Run has bound the listener.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Run opens the listener, accepts connections, and serves them until
// ctx is cancelled. It returns nil on clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	l, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Addr, err)
	}
	// TLS wraps the raw listener; everything downstream (accept loop,
	// EMFILE backoff, drain) is listener-shape agnostic.
	if s.cfg.TLS != nil {
		l = tls.NewListener(l, s.cfg.TLS)
	}
	s.mu.Lock()
	s.listener = l
	s.mu.Unlock()
	s.log.Info("listening", "addr", l.Addr().String())

	// The sweeper runs only while we're serving live traffic — never
	// during replay (which happens in New, before Run). It exits when
	// ctx cancels.
	go s.sweeper.Run(ctx)

	// Closing the listener on ctx cancel triggers the Accept loop to exit
	// via net.ErrClosed.
	closeOnce := &sync.Once{}
	closeListener := func() { closeOnce.Do(func() { _ = l.Close() }) }
	go func() {
		<-ctx.Done()
		closeListener()
	}()

	var backoff time.Duration
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			if isEMFILE(err) {
				s.tel.Metrics.ConnectionsRejected.Add(ctx, 1,
					metric.WithAttributes(attribute.String("reason", "emfile")))
				backoff = nextBackoff(backoff)
				s.log.Warn("accept temporarily failed (EMFILE), backing off", "delay", backoff)
				time.Sleep(backoff)
				continue
			}
			closeListener()
			s.wg.Wait()
			return fmt.Errorf("server: accept: %w", err)
		}
		backoff = 0
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.clientCount.Add(1)
			defer s.clientCount.Add(-1)
			// Close the conn when ctx is cancelled so the read loop
			// unblocks. handleConn will see the resulting error and exit.
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					_ = c.Close()
				case <-done:
				}
			}()
			s.handleConn(c)
		}(c)
	}
	s.wg.Wait()
	s.log.Info("shutdown clean")
	return nil
}

// isEMFILE reports whether err indicates the process exhausted its open
// file-descriptor limit. Per-platform variations are covered.
func isEMFILE(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

// nextBackoff returns the next delay in an exponential backoff capped
// at 1s, starting from 5ms.
func nextBackoff(prev time.Duration) time.Duration {
	const (
		minDelay = 5 * time.Millisecond
		maxDelay = 1 * time.Second
	)
	if prev <= 0 {
		return minDelay
	}
	next := prev * 2
	if next > maxDelay {
		return maxDelay
	}
	return next
}
