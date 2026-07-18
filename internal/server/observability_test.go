package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/aof"
	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/store"
	"github.com/prajwalmahajan101/toykv/internal/telemetry"
)

// --- M16 owned-risk test (1): no-op when disabled ---
//
// With telemetry disabled the instruments and spans are no-ops. This
// benchmark establishes the disabled hot-path cost; a run against the
// pre-M16 binary is the release-gate parity check (see M17). The value here
// is a guard against a future change accidentally making the disabled path
// do real work.
func BenchmarkObserveCommand_Disabled(b *testing.B) {
	s, err := New(Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(), // no Telemetry ⇒ telemetry.Disabled()
	})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	cs := &connState{authenticated: true}
	s.dispatch(cs, cmd("SET", "k", "v"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.observeCommand(cs, cmd("GET", "k"))
	}
}

// --- M16 owned-risk test (1b): disabled-path allocation budget ---
//
// ADR-0017 keeps the instruments unconditional (no if-enabled guard on the
// hot path), so the disabled path must stay allocation-light on its own —
// per-command attribute sets and metric options are memoized in cmdInstr, not
// rebuilt. This budget guards that: a regression that reintroduces per-command
// attribute/option construction (the ~20% throughput loss found at M17) trips
// here. The budget covers the whole observeCommand path (dispatch + the two
// irreducible no-op Tracer.Start context allocations), with headroom.
func TestObserveCommand_Disabled_AllocBudget(t *testing.T) {
	s, err := New(Config{
		Addr:  "127.0.0.1:0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: store.New(), // no Telemetry ⇒ telemetry.Disabled()
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cs := &connState{authenticated: true}
	argv := cmd("GET", "k")
	s.dispatch(cs, cmd("SET", "k", "v")) // seed the key so GET hits
	avg := testing.AllocsPerRun(500, func() { s.observeCommand(cs, argv) })
	const budget = 16
	if avg > budget {
		t.Fatalf("disabled observeCommand allocs/op = %.0f, want <= %d "+
			"(the no-guard design must stay allocation-light — did per-command "+
			"attribute/option construction creep back onto the hot path?)", avg, budget)
	}
}

// --- M16 owned-risk test (4): exporter-down resilience ---
//
// A configured-but-unreachable OTLP endpoint must never turn a successful
// command into a client error; export failures drop asynchronously.
func TestExporterDown_NeverFailsCommand(t *testing.T) {
	providers, err := telemetry.Init(context.Background(), telemetry.Config{
		Endpoint:    "127.0.0.1:1", // nothing is listening here
		Protocol:    "grpc",
		ServiceName: "toykv",
		Sampling:    1,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = providers.Shutdown(ctx)
	})

	s, err := New(Config{
		Addr:      "127.0.0.1:0",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:     store.New(),
		Telemetry: providers,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cs := &connState{authenticated: true}
	for _, c := range [][]string{{"SET", "k", "v"}, {"GET", "k"}, {"INCR", "n"}} {
		reply := s.observeCommand(cs, cmd(c...))
		if reply.Kind == resp.KindError {
			t.Fatalf("%v against a dead exporter returned an error: %q", c, reply.Str)
		}
	}
}

// --- M16 owned-risk test (2): durability unaffected by instrumentation ---
//
// The span wrapping around the AOF append must not disturb the
// mutate→append→fsync→reply order. With tracing compiled in and FsyncAlways,
// an acked write must survive a restart (replay). (The full subprocess
// crash matrix lives in test/chaos and also passes with instrumentation in.)
func TestDurability_WithInstrumentation(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tp, _ := telemetry.TestSpanProviders()

	s1, err := New(Config{Addr: "127.0.0.1:0", Log: log, Store: store.New(), Dir: dir, FsyncPolicy: aof.FsyncAlways, Telemetry: tp})
	if err != nil {
		t.Fatalf("New s1: %v", err)
	}
	cs := &connState{authenticated: true}
	if reply := s1.observeCommand(cs, cmd("SET", "k", "v")); reply.Kind == resp.KindError {
		t.Fatalf("SET failed: %q", reply.Str)
	}
	_ = s1.Close()

	// Restart with a fresh store — replay must reconstruct the acked write.
	s2, err := New(Config{Addr: "127.0.0.1:0", Log: log, Store: store.New(), Dir: dir, FsyncPolicy: aof.FsyncAlways})
	if err != nil {
		t.Fatalf("New s2: %v", err)
	}
	defer func() { _ = s2.Close() }()
	v, ok, err := s2.store.Get("k")
	if err != nil || !ok || string(v) != "v" {
		t.Errorf("acked write not durable with instrumentation: got %q ok=%v err=%v", v, ok, err)
	}
}
