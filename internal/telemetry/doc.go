// Package telemetry is the single owner of toykv's OpenTelemetry wiring
// (M16). It builds the tracer / meter / logger providers, the OTLP
// exporter, the process-runtime instrumentation, and the slog→OTLP log
// bridge, and it hands the rest of the server pre-created instrument
// handles and span-openers.
//
// Off by default: when Config.Endpoint is empty the global providers are
// the SDK no-ops, so instrument handles and spans created against them
// cost nothing on the hot path — there is no `if enabled` guard sprinkled
// through the server. Telemetry never fails a command: exporter errors are
// logged and dropped, never surfaced to a client.
package telemetry
