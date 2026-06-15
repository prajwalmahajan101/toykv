// Package tui implements the Bubble Tea program backing toykv-tui:
// Model/Update/View, refresh ticking, mode-aware key dispatch, and
// modal input handling. The TUI consumes a Doer (satisfied by
// *internal/client.Client) so tests can drive Update with a fake
// without a real network. See docs/LLD.md §7 and PRD §5.5.
package tui
