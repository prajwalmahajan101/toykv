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

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// Config holds server configuration. Future milestones add Dir +
// FsyncPolicy (M3) and other knobs.
type Config struct {
	Addr  string       // TCP listen address, e.g. ":6390"
	Log   *slog.Logger // structured logger; nil ⇒ slog.Default()
	Store *store.Store // backing key-value store; must be non-nil
}

// Server is the TCP listener and command dispatcher.
type Server struct {
	cfg   Config
	log   *slog.Logger
	store *store.Store

	mu       sync.Mutex
	listener net.Listener
	wg       sync.WaitGroup
}

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
	return &Server{cfg: cfg, log: cfg.Log, store: cfg.Store}, nil
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
