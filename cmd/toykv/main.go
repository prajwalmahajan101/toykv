// Command toykv is the in-memory key-value store server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/server"
	"github.com/prajwalmahajan101/toykv/internal/store"
)

const usage = `toykv — in-memory key-value store server

usage:
  toykv [flags]

flags:
  -addr        string  listen address (default ":6390")
  -dir         string  data directory for the AOF (default "./data"; "" disables persistence)
  -appendfsync string  fsync policy: always|everysec|no (default "always")
  -log-level   string  log level: debug|info|warn|error (default "info")
  -h, --help           show this help and exit
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stdout, usage) }
	var (
		addr        = flag.String("addr", ":6390", "listen address")
		dir         = flag.String("dir", "./data", "data directory for the AOF; \"\" disables persistence")
		appendfsync = flag.String("appendfsync", "always", "fsync policy: always|everysec|no")
		logLevel    = flag.String("log-level", "info", "log level: debug|info|warn|error")
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

	s, err := server.New(server.Config{
		Addr:        *addr,
		Log:         log,
		Store:       store.New(),
		Dir:         *dir,
		FsyncPolicy: policy,
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
