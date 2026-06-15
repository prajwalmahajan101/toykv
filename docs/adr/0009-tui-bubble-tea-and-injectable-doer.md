# ADR-0009: `toykv-tui` — Bubble Tea, injectable `Doer`, `tea.Cmd`-only refresh

- Status: Accepted
- Date: 2026-06-15
- Milestone: M7
- PR: `feat/m7-tui` (PR A: package; PR B: binary + smoke)

## Context

M7 ships `toykv-tui`, the two-pane Bubble Tea TUI mandated by PRD §5.5. It is the second consumer of `internal/client` (after the CLI) and the first place in toykv that ingests a real terminal-UI dependency tree (`bubbletea`, `lipgloss`, `bubbles`). It is also the first sub-package whose tests have to drive a UI loop without a real network.

Three calls had real alternatives worth recording:

1. **Which TUI library.** Bubble Tea vs. `tview` vs. `tcell` directly. The roadmap (`docs/ROADMAP.md:93`) pre-committed to Bubble Tea, but the *why* was unrecorded — and M6 just argued (ADR-0008) for *zero* third-party deps. Promoting one library family to first-class status deserves an entry.
2. **How the TUI sees the client.** Hold a `*client.Client` directly, or an interface? The CLI took the concrete type. The TUI's `Update` loop has to be unit-testable without a server, which makes the trade-off different.
3. **Where the refresh tick lives.** Bubble Tea idiom is `tea.Cmd`; raw idiom is a goroutine pushing into `tea.Program.Send`. Picking one closes off latent races early.

M6's "no third-party deps" budget (ADR-0008) is breaking here on purpose. The TUI was always the milestone where the budget was going to bend — the question is how far.

## Decision

**Bubble Tea (`bubbletea` + `lipgloss` + `bubbles/textinput`). `internal/tui.Model` holds a `Doer` interface, not a `*client.Client`. Refresh is a `tea.Cmd` returned by `Update`; no goroutine pushes into the program.**

Concretely:

- Dep tree: `github.com/charmbracelet/bubbletea v1.3.10`, `lipgloss v1.1.0`, `bubbles v1.0.0`. Transitive surface (`x/ansi`, `termenv`, `go-isatty`, etc.) is the cost of the choice and is owned at the lockfile, not re-vendored. `teatest` is dev-only.
- `internal/tui` introduces `type Doer interface { Do(argv ...string) (resp.Value, error) }` and `NewModel` accepts that, not `*client.Client`. Production wiring passes the real client (`cmd/toykv-tui/main.go`); tests pass a `fakeDoer` recording call argvs.
- Every poll cycle is `fetchRefresh(c, pattern, focused) tea.Cmd` → returns `refreshMsg{...}` → `Update` reschedules via `tickCmd`. The Bubble Tea program goroutine is the only thread touching `Model`; `Doer.Do` is invoked off it but its mutex (client side) serialises naturally.
- Two small refactors land alongside: `cmd/toykv-cli/print.go` → `internal/respfmt/` and `cmd/toykv-cli/parse.go` → `internal/cmdparse/`. Both CLI and TUI consume them; no duplicated printer or tokeniser exists post-M7.
- Status bar `fsync` field is sourced from a `-fsync` *display* flag the operator passes in, not a wire query. PRD §5.1 has no `INFO`; speculating one for the TUI was rejected.

## Consequences

**Positive**

- Bubble Tea pulls its weight: `bubbles/textinput` removes ~150 lines of cursor/edit code the TUI would otherwise hand-roll. `tea.Program` owns the alt-screen lifecycle and resize handling. `lipgloss` gives the two-pane layout for free.
- `Doer` injection makes `Update` testable in microseconds. `internal/tui/update_test.go` covers every PRD §5.5 keybinding (j/k, /, n, e, d, t, i, D, F, r, :, q) plus mode transitions without a single `net.Dial`. A real `client.Client` shows up only in the cmd-level smoke (`cmd/toykv-tui/smoke_test.go`).
- `tea.Cmd`-only refresh means there is no second goroutine touching `Model`. Bubble Tea's Update is single-threaded by construction; the client's mutex is on the *I/O* boundary. The two concurrency models compose cleanly — no "TUI pushes into program from a goroutine" pattern to audit.
- The CLI/TUI now share one printer (`internal/respfmt`) and one tokeniser (`internal/cmdparse`). When the post-M8 `toykv-go` SDK extracts client + printer for SDK users, both consumers come along unchanged.

**Negative**

- The dep budget moves from zero to ~20 modules (mostly transitive: `x/ansi`, `termenv`, `go-runewidth`, `lucasb-eyer/go-colorful`, `mattn/go-isatty`, `golang.org/x/sys`). The TUI binary's `go build` is no longer a five-second affair, and `go.sum` grows. The server binary is unaffected — `internal/tui` is not imported by `cmd/toykv`.
- The introduced `Doer` interface is a one-method shim layered over the client's exact signature. The shim is structural rather than semantic. If we ever add a `DoBytes` requirement to the TUI, the interface widens; if we add a second method, callers re-wire. Acceptable cost for testability.
- The `-fsync` display flag is honest about its lack of authority — the operator can mislabel it. Future-v2 `INFO` makes it queryable and the flag goes away.

**Neutral**

- 2 Hz refresh is N+2 calls per tick (`KEYS pat` + per-key `TTL` + `DBSIZE` + optional `GET focused`). At toy-scale this is fine. M8 may flag it under the integration-test load matrix; M7 does not.
- `KEYS *` is the only listing primitive available pre-`SCAN` (v2). The TUI's left pane therefore *cannot* paginate a large keyspace; this is a PRD-bounded limit, not a TUI defect.

## Alternatives considered

- **`tview` / `tcell` directly.** Rejected — both fit but Bubble Tea's update/view model is closer to how `internal/tui` wants to be tested (pure functions over `Model`). `tview` carries an internal event loop that's harder to unit-drive without a real screen.
- **Hold `*client.Client` concretely (CLI-style).** Rejected — the CLI's tests don't need a fake client because the CLI's tests are subprocess-driven (`os/exec`). The TUI's update tests *want* to live in-process and assert mode transitions; a real client there is a constant integration cost. The `Doer` shim costs one interface declaration to remove.
- **Goroutine-driven refresh via `tea.Program.Send`.** Rejected — adds a second writer to `Model` state via the program's message queue and creates a clean cancellation problem on quit (the goroutine has to learn the program shut down). `tea.Tick` returning a `tea.Cmd` is the idiom; using it preserves the single-thread invariant on `Update`.
- **Adding an `INFO` command for fsync/dbsize/uptime.** Rejected for M7 — it's a real PRD §5.1 extension, not a TUI implementation detail. Belongs in a v2 ADR if it lands.
- **Keeping `print.go`/`parse.go` inside `cmd/toykv-cli`.** Rejected — the TUI value pane needs the same RESP rendering rules as the CLI; the raw command prompt needs the same tokeniser. Two copies would drift. Lifting them is a six-file change with no behaviour delta.

## References

- PRD.md §5.5 — TUI layout and keybindings
- PRD.md §6 — third-party dep policy (this ADR is the recorded relaxation)
- ROADMAP.md §M7 — exit criteria
- LLD.md §7 — Model/Update/View shape
- ADR-0008 — CLI's stdlib-only choice (the framing this ADR diverges from)
- `internal/tui/commands.go` — `Doer` definition site
- `internal/client/client.go` — mutex contract the TUI relies on
