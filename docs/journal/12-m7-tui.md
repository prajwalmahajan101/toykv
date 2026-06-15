# 12 — M7 TUI (PR A + PR B, pre-merge)

**Date:** 2026-06-15
**Branch:** `feat/m7-tui` (PR A: `internal/tui` package; PR B: `cmd/toykv-tui` binary + smoke)
**ADR:** [`docs/adr/0009-tui-bubble-tea-and-injectable-doer.md`](../adr/0009-tui-bubble-tea-and-injectable-doer.md), status *Accepted*.
**Trigger:** M6 closed PR A (`internal/client`) and PR B (`toykv-cli`) but left the abstraction with only one consumer. The TUI is the second consumer — the first place that proves the client surface (`Dial`/`Do`/`Close`/`ErrClosed`) is enough to build a real-time UI on top of, not just a one-shot CLI.

## Decision / surprise

The expensive PR I expected. M7's dependency budget moves from zero (ADR-0008's "stdlib only" stance for the CLI) to ~20 modules (mostly transitive). ADR-0009 is the recorded relaxation; the README dep budget note now reflects reality rather than aspiration.

Three calls landed differently from the initial sketch:

1. **`Doer` interface, not `*client.Client`.** The plan said "Model holds a `*client.Client`" mirroring the CLI. Once I started writing `update_test.go` it was obvious: every keybinding test wanted to assert "Update issued SET foo bar to the client" without a real network. A one-method `Doer` shim turned all of `internal/tui` into pure unit tests. `cmd/toykv-tui/smoke_test.go` is the only place a real client shows up, and even there the test holds the binary's main wiring at arm's length by driving `tui.Model` directly. Net: ~150 lines of test infra that would otherwise have been a subprocess+PTY rig.

2. **Refresh as a `tea.Cmd`, not a goroutine.** Bubble Tea idiom, but the alternative was real: a goroutine pumping `KEYS *` results into `tea.Program.Send`. Rejected because the cancellation story on quit is annoying — the goroutine has to learn the program shut down. `tea.Tick` returning a `tea.Cmd` keeps `Update` as the single thread touching `Model`.

3. **The refactor was bigger than the TUI.** The PR A changeset is half new code (`internal/tui/*.go`) and half lift-and-shift (`cmd/toykv-cli/print.go` → `internal/respfmt/`, `cmd/toykv-cli/parse.go` → `internal/cmdparse/`). The TUI value pane needs `respfmt.PrettyString`; the raw-command prompt needs `cmdparse.Tokenise`. Doing it now prevents the printer/parser from drifting; doing it after M8 would be twice the work since `toykv-go`'s SDK extraction will want the printer too.

The one mid-implementation surprise: the smoke test in `cmd/toykv-tui/` initially hung for 30 seconds. My `drive(model, msg)` helper chained `Update → cmd() → next msg → Update`, which after a `refreshMsg` hit `tickCmd(2 * time.Second)` and blocked the test's single thread inside `tea.Tick`. Fix: split into `drive` (one Update, returns the Cmd) and `runReply` (executes the Cmd at most once). The test now only ever runs Cmds it expects to terminate — network round-trips, not timers. Documented inline.

## Why it mattered

Three things this PR locks in:

1. **`internal/client` is now load-bearing across two binaries.** The CLI proved it for line-rate one-shot/REPL traffic; the TUI proves it for sustained 2 Hz polling under a long-lived process. Any future regression to the client surface (mutex semantics, `ErrClosed` propagation, partial-frame handling) now fails *two* test surfaces, which is the right amount of redundancy for the abstraction every future consumer will inherit.

2. **The `toykv-go` SDK extraction path is now cleaner than ADR-0007 anticipated.** ADR-0007 said the SDK extraction would be `s/internal\/client/client/g` plus an exported printer. After M7 the *exported printer* exists at `internal/respfmt/` and the *exported tokeniser* at `internal/cmdparse/`. The SDK extraction post-M8 is now a three-package move (`client` + `respfmt` + `cmdparse`), each with its own public surface and tests already written for it. The risk that the SDK extraction reveals a "this only worked because main called it" coupling is materially lower than it was twelve hours ago.

3. **M8's protocol-compat job is genuinely just protocol compat.** With the CLI subprocess test (M6) and the TUI smoke test (this PR) both green, M8 doesn't have to invent end-to-end coverage from scratch — it inherits the in-process server boot, the random-port allocation pattern, and the mutating-command round-trip assertions. M8's only new surfaces are `go-redis/v9` (third-party Redis client compat) and `redis-cli` byte-compat across the PRD §5.1 command matrix. That is a much tighter mandate than "test everything".

## Code / measurement

PR A — `internal/tui` package + refactor:

- `internal/tui/doc.go` — 5 lines (package doc).
- `internal/tui/model.go` — 87 lines (Model, Mode, KeyInfo, StatusLine, NewModel, accessors).
- `internal/tui/messages.go` — 28 lines (tickMsg, refreshMsg, replyMsg).
- `internal/tui/commands.go` — 86 lines (fetchRefresh, runMutating, tickCmd, doKeys, doIntCmd, respErr).
- `internal/tui/update.go` — 165 lines (Init, Update, handleKey, handleNormalKey, handleConfirmKey, handleInputKey, submitInput, enterInput).
- `internal/tui/view.go` — 105 lines (View, renderLeft, renderRight, renderStatus, truncate, styles).
- `internal/tui/glob.go` — 42 lines (client-side `*`/`?` matcher).
- `internal/tui/format.go` — 45 lines (formatTTL, formatBytes, formatLatency).
- Tests: `update_test.go` (218), `view_test.go` (60), `glob_test.go` (35), `format_test.go` (50).
- Refactor: `internal/respfmt/respfmt.go` (108), `respfmt_test.go` (87); `internal/cmdparse/cmdparse.go` (76), `cmdparse_test.go` (49). `cmd/toykv-cli/print.go` + `parse.go` + `main_test.go` deleted; `main.go` rewired to import `respfmt`/`cmdparse`.

PR B — `cmd/toykv-tui` wiring:

- `cmd/toykv-tui/main.go` — 76 lines (flag parsing, dial, `tea.NewProgram`, exit-code mapping).
- `cmd/toykv-tui/smoke_test.go` — 140 lines (real-server SET/INCR round-trips + bad-addr exit-code).

CI: `make ci` (fmt-check + vet + lint + `go test -race`) green across all packages. `gofmt -s` clean.

## Score

- **Three** ADRs over the original budget (`0007`, `0008`, `0009`). The pattern noted in journal 11 — "watch if M7 adds 0009 without cause" — landed, but the cause (real dep-policy change, real injectable-test-surface decision) is documented rather than smuggled. The README's stated ADR budget is now formally wrong and should be revised in the README polish step of M9 rather than apologised for at every milestone.
- Zero changes to `internal/store`, `internal/aof`, `internal/server`, `internal/resp`, `internal/client`. The TUI is strictly a consumer; the existing surfaces did not need to flex. This is the test of M6 PR A's claim that the client was "the right shape" — it was.
- Two new shared packages (`respfmt`, `cmdparse`) cost ~340 lines total but prevent two divergence risks (printer drift between CLI and TUI; tokeniser drift between CLI raw input and TUI raw prompt). Net positive even before counting the SDK extraction discount.
- The teatest dep was pulled but not used. Initial plan called for snapshot tests; in practice `View()` is a function from `Model` to `string`, and `strings.Contains` assertions cover the same observable behaviour without the brittle-snapshot maintenance tail. `teatest` stays in `go.mod` as a candidate for M8 integration tests; if it's still unused by then, drop it.

## What's next

- **Immediate:** open PR A (`feat/m7-tui` → `main`) for the package + refactor; once merged, push PR B (the binary). After both merge, tag `m7` per the milestone-tagging convention.
- **README polish ahead of M9:** revise the ADR budget, document the TUI dep tree honestly, add a TUI screenshot/GIF capture step.
- **M8:** integration tests across `toykv-cli` (already subprocess-tested), `redis-cli` (new), `go-redis/v9` (new), and a TUI smoke via `teatest` (decide then whether to keep the dep). The TUI's smoke test pattern in `cmd/toykv-tui/smoke_test.go` is the template.
- **Open question for M9:** the `-fsync` status-bar flag is a placeholder for a real `INFO` query. v1 ships without; the README should call this out so users don't expect the label to reflect server config automatically.

## Blog-worthy?

One strong thread:

- **"Why my Bubble Tea Update tests stopped needing a network"** — the `Doer` injection trick is generalisable to any Bubble Tea program whose model talks to a service. One paragraph on the interface, one on the fakeDoer, one on the smoke test that *does* dial a real server. Lands a real piece of testing advice in a Bubble Tea ecosystem still light on prior art.

Possibly a second:

- **"When 'stdlib only' has to bend"** — read ADR-0008 ("CLI stays stdlib-only") next to ADR-0009 ("TUI adds Bubble Tea") and the discipline of writing *why* the budget moved instead of pretending it didn't. Short post; the meta-point about ADRs as a relaxation log is the value.
