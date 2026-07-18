package telemetry

import (
	"context"
	"errors"
)

// initEnabled builds the real OTLP-backed providers. It is stubbed in T1
// (skeleton only) and implemented in T2; until then, requesting telemetry
// via a non-empty endpoint is a configuration error surfaced at startup.
func initEnabled(_ context.Context, _ Config) (*Providers, error) {
	return nil, errors.New("telemetry: OTLP export not yet wired (M16 T2)")
}
