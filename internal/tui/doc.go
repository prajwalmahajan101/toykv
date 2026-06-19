// Package tui implements the Bubble Tea program backing toykv-tui:
// Model/Update/View, refresh ticking, mode-aware key dispatch, and
// modal input handling. The TUI consumes a Doer (satisfied by
// *internal/client.Client) so tests can drive Update with a fake
// without a real network. See docs/LLD.md §7 and PRD §5.5.
//
// Invariants for code in this package:
//
//   - Never write to os.Stdout or os.Stderr. The TUI runs in the
//     alternate screen with raw mode enabled; any stray print would
//     corrupt the rendered frame. If a diagnostic must be recorded,
//     route it through the optional --log <path> flag wired in
//     cmd/toykv-tui (slog handler to a file), not through the standard
//     log package.
//   - Never block the Update goroutine on I/O. Network calls happen
//     inside tea.Cmds returned from Update; the reducer itself stays
//     pure.
//   - View must remain deterministic given (Model, terminal size).
//     Side effects (logging, timers) belong in commands, not the view.
package tui
