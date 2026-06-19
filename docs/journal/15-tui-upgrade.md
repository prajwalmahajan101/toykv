# 15 — TUI redesign + code-review hardening

**Date:** 2026-06-20
**Branch:** `feature/tui_upgrade`
**PR:** [#22](https://github.com/prajwalmahajan101/toykv/pull/22) — rebase-merged onto `main` at `c060a2f`
**Trigger:** Live use of the M7 TUI surfaced three concrete problems in one sitting — "no panels, ugly colors, no borders / keybindings unintuitive, no help / missing TTL, type, value preview, stats." The blunt summary was "tui is very bad." That's M7 working as designed (functional, minimal, milestone-shaped); it's not what a v1 KV TUI should *feel* like next to lazygit / k9s / yazi. The branch had a single job: close the gap between "works" and "feels finished," then take the code review seriously instead of marking the redesign done at the first green pipeline.

## Decision / surprise

Three calls landed differently from the original plan.

1. **Type column and per-RESP-kind preview were dropped before the first commit.** The redesign plan budgeted real estate for a `type` column (`str`/`list`/`hash`/`set`/`zset`) and per-kind value previews in the right pane (`LRANGE 0 9`, `HGETALL`, `SMEMBERS`, `ZRANGE … WITHSCORES`). A 30-second probe against a freshly-started `toykv --addr :6391 --dir ""` returned `(error) ERR unknown command` for every single one — toykv v1.0.0 implements *only* strings, plus `INCR/DECR/EXPIRE/TTL/KEYS/DBSIZE/FLUSHDB/PING`. So the column would always say `str` and the preview would always be `GET`. The `KeyInfo.Kind` field still ships as a forward-compatible placeholder; the column itself doesn't. Better to ship a clean string-only TUI now than a clever-looking but useless type column. (When lists/hashes/sets land in some future M11+, the slot is reserved.)

2. **Pipelining the TTL fan-out — the "L"-effort sub-task in ISSUE-002 — was also dropped, deliberately.** `internal/client.Client`'s own doc string opens with "serialises Do calls behind a mutex; pipelining is out [of scope]." Issuing the N TTLs concurrently via `errgroup` would just queue them on the mutex. Pipelining is a real client-side change (multi-cmd-in-flight, response demux), not a TUI change. The user-visible bug — a slow `GET` for the old focus painting over the *new* focus after rapid `j/k` — got fixed by versioning: `fetchGen` bumps on every navigation, replies stamped with an older generation are silently dropped. The TTL fan-out being N+2 serial RTTs is now documented as a follow-up, not papered over.

3. **macOS CI surfaced a pre-existing race the moment the test matrix ran the first time.** `TestTUI_TeatestSmoke_SET` waited for `"foo"` to appear in rendered output before checking server state. But `"foo"` shows up inside the input prompt's echo the *moment* it is typed — well before `Enter` is pressed and `runMutating` actually fires the `SET`. Linux CI scheduled the `tea.Cmd` fast enough to land before the verify `GET`; macOS exposed the race. The fix was a two-line refactor of the test: poll the server (the pattern the INCR smoke test was already using) instead of trusting the rendered output to imply the cmd had run. Worth noting that the test was racy on `main` too — this branch didn't introduce the race, it just changed the timing enough to lose the coin flip.

The smaller surprise was how much value the **help overlay + footer hint bar** delivered relative to its size (~160 LoC in one file). Both read from the same `bindings` slice, so adding a key in one place updates the discoverability surface in three (overlay, footer, internal table). The footer's reservation logic (`?` and `q` always present, rank-ordered fill of the rest) means a 60-column terminal still shows quit and help even when half the mutate hints get dropped.

## Why it mattered

Four things this branch locks in:

1. **The TUI now passes its own 11-point hygiene checklist** — alt screen restored on panic *and* `SIGTERM`, semantic colour tokens with `NO_COLOR` honoured, footer hints always visible, `?` overlay for the full bindings table, breakpoint-driven layout (Wide / Mid / Narrow / Stack / Tiny with a "terminal too small" banner under 60×16), filter-match highlighting that no longer spliced into ANSI escape sequences. None of those were missing because the M7 TUI was wrong; they were missing because M7's exit criteria were "PRD §5.5 keybindings work." A v1 polish pass is a different brief.

2. **The refresh path is now safe under rapid navigation.** Before this branch, every `j/k` scheduled an unversioned `fetchRefresh`; if the user pressed `j` twice quickly while the previous `GET` was still in flight, the older reply could overwrite the value pane after the cursor had already moved. `fetchGen` is a monotonic counter bumped by `scheduleFetch()`; the eventual `refreshMsg` is dropped if its `gen` no longer matches. Gen=0 stays as a "untagged" wildcard so existing test fixtures keep working without per-test ceremony. The companion ISSUE-006 fix — clear `m.hasVal` and prefix the error banner with `"refresh:"` — means a server error after the focused key was deleted no longer keeps painting the dead value indefinitely.

3. **`Tab` is no longer a lie.** In Wide/Mid/Narrow layouts the value pane is read-only, so `Tab` only repainted the border accent — looked-like-it-should-do-something with no follow-through. The fix scopes `Tab` to `LayoutStack` only (where the value pane lives on its own row and warrants its own viewport) and wires `j/k` to a new `valueScroll` offset when focus is on the right. Reset on focused-key change. Three small commits in total; removes a UX dishonesty without inventing a new affordance.

4. **The package now documents its own invariants** — no stdio writes (alt screen would corrupt), no blocking I/O in Update (commands handle that), `View` stays deterministic given (Model, terminal size). All future contributors (including future-me) inherit the rules without having to re-derive them from the existing code. The `--log <path>` flag in `cmd/toykv-tui` is the escape hatch when a diagnostic genuinely needs to be recorded — routed through `slog` to a file, never to stdio.

## Code / measurement

- `internal/tui/styles.go` (new, ~120 LoC) — `styles` struct + `newStyles(noColor bool) styles`. One semantic palette, two branches (colour / identity). Adaptive light-dark mode via Lipgloss.
- `internal/tui/help.go` (new, ~160 LoC) — `bindings` slice as single source of truth, `renderFooter` with always-reserved `?`/`q`, `renderHelp` overlay via `lipgloss.Place`.
- `internal/tui/view.go` (full rewrite, ~360 LoC) — rounded-border panes with focused-pane accent, top header strip, breakpoint-driven layout selection, ANSI-safe `highlightFilter` (span-claim on the raw name then splice once), value-pane `valueScroll`, RESP value colorisation via line-prefix scan.
- `internal/tui/model.go` — `KeyInfo.Kind`, `Focus`/`LayoutKind` enums, `showHelp`, `valueScroll`, `st styles`, `fetchGen`, `breakpoint()`, `colWidths()`. `ModeNow()` → `Mode()` to match the bare-noun accessor convention.
- `internal/tui/update.go` — `?` toggles overlay (`q`/Ctrl+C still quit while open; `Esc`/`?` dismiss-only), `Tab` scoped to Stack, `g`/`G` jump top/bottom, `scheduleFetch()` helper threads `fetchGen` through every nav.
- `internal/tui/commands.go` — `fetchRefresh` now takes `gen uint64` and stamps the `refreshMsg`. No pipelining; documented as a follow-up.
- `cmd/toykv-tui/main.go` — `defer recover()` + explicit alt-screen escape sequence so any panic prints the trace on a restored terminal. `signal.Notify` on `SIGTERM` calling `p.Quit()`. New `--log <path>` flag.
- Tests: `styles_test.go` (NO_COLOR identity invariant), 12 new test cases across `view_test.go` / `update_test.go` / `smoke_test.go` (help overlay supersedes err/prompt, `q` quits while overlay open, Tab no-op outside Stack, breakpoint table, ANSI-safe highlight, stale-gen drop, refresh-error clears state, dial-failure log write).
- 14 commits total, all atomic, pre-commit gofmt + golangci-lint clean. CI matrix (ubuntu/macos × Go 1.25.x/1.26.x + chaos-smoke + GitGuardian) all green on rebase merge.

## Follow-ups

- Pipeline the TTL fan-out for real. Needs an `internal/client` API change (`DoPipeline(argv ...[]string) ([]resp.Value, error)`) — not a TUI change. Worth doing if the key list ever grows past a few hundred entries.
- Type column + per-kind previews return whenever toykv grows lists/hashes/sets/zsets. The slot is reserved.
- The TTL `>= 60s` warn threshold is hard-coded. Could become a setting if anyone asks; nobody has.

## Reflection

The honest read on this branch is that M7 shipped on time and on spec, and three months later the spec was visibly thin compared to where the rest of the project had moved. The fix wasn't "M7 was wrong"; it was "v1's polish bar is higher than M7's, and that's a v1.x branch, not a milestone re-do." The structure of two halves — redesign first, then take the architectural review seriously rather than papering over its findings — is the structure to copy next time a milestone's surface ages out of sync with the rest of the project. It is also the structure that produced two of the cleanest fixes in this branch (`fetchGen` versioning, ANSI-safe highlight): both were caught by the review, not by the redesign, and both would have shipped as latent bugs if the review had been treated as a formality.

The other thing worth recording: the `code_assist:code_review` skill's distinction between "anything we'd ship anyway" and "things the reviewer caught us pretending we didn't have" was sharp. Nothing in the 11-issue list felt like reviewer manufactured-importance — every one of them, including the Low-severity ones (`ModeNow` naming, `pane()` rebuilds), was a real thing. Following that signal end-to-end (FIX-everything rather than DEFER-the-Lows) cost about one hour, produced 8 small atomic commits, and left the surface measurably better. The opposite outcome — "we'll get to the Lows later" — would have left a backlog that nobody opens.
