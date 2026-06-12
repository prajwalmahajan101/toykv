package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Config holds server configuration.
type Config struct {
	Addr        string               // TCP listen address, e.g. ":6390"
	Log         *slog.Logger         // structured logger; nil ⇒ slog.Default()
	Store       *store.Store         // backing key-value store; must be non-nil
	Dir         string               // AOF data directory; "" disables persistence
	FsyncPolicy aof.FsyncPolicy      // ignored when Dir == ""
	NowFunc     func() time.Time     // optional; defaults to time.Now. Drives TTL command timestamps.
	SweeperOpts store.SweeperOptions // optional; zero ⇒ store package defaults (1s / batch 20)
}

// Server is the TCP listener and command dispatcher.
type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *store.Store
	aof     *aof.Writer // nil ⇒ persistence disabled
	sweeper *store.Sweeper
	nowFunc func() time.Time

	mu       sync.Mutex
	listener net.Listener
	wg       sync.WaitGroup
	closed   bool

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
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = time.Now
	}
	s := &Server{
		cfg:     cfg,
		log:     cfg.Log,
		store:   cfg.Store,
		nowFunc: cfg.NowFunc,
		sweeper: store.NewSweeper(cfg.Store, cfg.SweeperOpts),
	}

	if cfg.Dir != "" {
		// Replay first so any AOF parse failure is surfaced before we
		// open the listener. Replay uses an internal dispatch path that
		// applies commands directly to the store and never re-appends.
		stats, err := aof.Replay(cfg.Dir, s.replayApply)
		if err != nil {
			return nil, fmt.Errorf("server: aof replay: %w", err)
		}
		w, err := aof.Open(cfg.Dir, cfg.FsyncPolicy)
		if err != nil {
			return nil, fmt.Errorf("server: aof open: %w", err)
		}
		s.aof = w
		s.log.Info("aof ready",
			"dir", cfg.Dir,
			"fsync", cfg.FsyncPolicy.String(),
			"replay_records", stats.Records,
			"replay_bytes", stats.Bytes,
			"replay_duration", stats.Duration,
		)
	}
	return s, nil
}

// replayApply runs a command from the AOF through the normal dispatch
// path. Because s.aof is still nil during replay, the mutating handlers
// no-op their appendIfLive — the same code serves both replay and live
// traffic without a second handler table.
func (s *Server) replayApply(argv [][]byte) error {
	reply := s.dispatch(argv)
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
