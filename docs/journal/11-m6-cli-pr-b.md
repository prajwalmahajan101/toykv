# 11 — M6 PR B (toykv-cli wired)

**Date:** 2026-06-15
**Branch:** `feat/m6-cli` (commit `9968f9e`, not yet merged)
**ADR:** [`docs/adr/0008-cli-stdlib-and-mode-detection.md`](../adr/0008-cli-stdlib-and-mode-detection.md), status *Accepted*.
**Trigger:** PR A (`internal/client`) was sitting on `main` with no consumer. PR B is the first real exercise of that surface: a stdlib-only CLI matching PRD §5.6's three-mode contract.

## Decision / surprise

The pleasantly small PR I expected. The CLI is ~280 lines across `main.go`, `parse.go`, `print.go`; the only non-trivial code is the hand-rolled tokeniser. Every "should I add X" temptation got resolved by re-reading PRD §6 — *"Stdlib only (`net`, `bufio`, `flag`, `os`); no readline lib in v1"* — and saying no. `peterh/liner` for history? No. `mattn/go-isatty`? No (`os.ModeCharDevice` is one stdlib call). `-mode {oneshot,repl,pipe}` flag? No — TTY-detect, same as `redis-cli`. The discipline made ADR-0008 nearly write itself: the negative-scope list *is* the decision.

The one design call worth recording properly was **exit-code semantics in REPL/piped**. PRD §5.6 specifies `0`/`1`/`2` for one-shot but is silent on multi-command modes. The call: each iteration resets the code, and the *process* exit reflects the *last* reply. That matches `bash` heredoc-pipe ergonomics — `echo … | toykv-cli; echo $?` reports the last command's outcome, which is what scripts will reach for. Documented in ADR-0008 so the next time this question comes up (it will, when the M8 integration harness drives the CLI in CI) the rationale is already on disk.

The other small surprise: the subprocess test ended up *simpler* than M3/M5's crash-injection tests. There's no self-re-exec, no SIGKILL coordination — just `go build` the CLI at `TestMain`, boot the server in-process via `server.New`, exec the binary with `os/exec`. The shared client (PR A) absorbed all the network complexity; the test only has to assert stdout shape and exit codes. That's the dividend of M6 PR A's "one job, well-tested" split paying off on day one.

## Why it mattered

Three things this PR locks in:

1. **`internal/client` is now load-bearing.** Until this PR, PR A was an unloaded gun — tests-pass-but-no-real-consumer. The CLI exercises `Dial` / `Do` / `Close` / `ErrClosed` under realistic line-rate I/O. Any future regression to the client surface will fail a CLI subprocess test, which is the right blast radius.

2. **The `toykv-go` SDK seed is *de facto* fixed.** Per ADR-0007, `toykv-go`'s first cut will be the post-M8 extraction of `internal/client`. PR B is the first proof that the package's exported surface (`Dial`, `DoBytes`, `ErrClosed`) is enough to build a real consumer with zero extra wrapping. If the SDK extraction in two milestones' time needs more than a `s/internal\/client/client/g`, that's a signal something here was wrong; right now nothing flags.

3. **M6's two remaining bits are tiny.** PR C (this journal + roadmap flip + tag) is paperwork. M7's TUI then sits on the same client with no new wire surface — Bubble Tea on a known-good RESP plumbing. The v1 release-path got noticeably shorter today.

## Code / measurement

- `cmd/toykv-cli/main.go` — 193 lines (flag parsing, mode dispatch, REPL/piped loops, isTTY).
- `cmd/toykv-cli/parse.go` — 87 lines (shell-style tokeniser + escape decoder).
- `cmd/toykv-cli/print.go` — 88 lines (pretty + raw printer for all RESP2 kinds).
- `cmd/toykv-cli/main_test.go` — 119 lines (tokeniser + printer table tests).
- `cmd/toykv-cli/cli_subprocess_test.go` — 188 lines (boots server, execs binary, covers SET/GET/INCR/missing/error/dial-fail/raw/piped/exit-mapping).
- `make ci` green: fmt-check + vet + lint + race-tests, all packages.
- ADR: 0008, ~85 lines.
- Roadmap: M0–M5 ✅, M6 PR B done (PR A + PR B; PR C pending).

## Score

- Zero third-party deps introduced. PR A added none either. The CLI binary's `go.sum`-relevant footprint over M6 stays at zero. (TUI in M7 will be the first to bring Bubble Tea in.)
- One ADR over budget (0008 — second budget break after 0007). Both breaks have plausible defences; the pattern to watch is whether M7/M8 try to add 0009/0010, at which point the README's budget needs honest revision rather than serial exceptions.
- One unstated PRD detail resolved on the record (exit code in REPL/piped). Better to write the ADR than to ship the binary with the answer buried in `main.go`.
- One gitignore change: `docs/journal/` is no longer ignored. The journal is now part of the repo, which retroactively makes entries 00–10 commit-eligible alongside this one.

## What's next

- **Immediate:** open PR `feat/m6-cli` → `main`. Rebase-merge. Update this entry's SHA refs post-merge (the entries 03–09 pattern). Then PR C: this journal + ADR-0008 + ROADMAP M6 status flip + optional `m6` tag.
- **M7:** TUI branch (`feat/tui`). Same `internal/client` consumer story, plus Bubble Tea. The client's single-connection contract will need re-validating once the TUI starts issuing concurrent `Do` from multiple Bubble Tea components — that's a real test of the mutex behaviour `client.go:22-25` promises.
- **Open question for M8:** does the integration suite drive `toykv-cli` itself (subprocess test, similar to this PR's pattern) or just `redis-cli`? Both, probably — the CLI is a first-party artefact whose own protocol compat is worth pinning. Tracked as an open item for M8 PR planning.

## Blog-worthy?

Maybe one thread:

- **"How `redis-cli` decides what mode it's in"** — one-paragraph dive into `Mode()&os.ModeCharDevice`, why it beats a `-mode` flag, and the edge case it doesn't cover. Light, technical, exactly the size of a Hashnode post that doesn't need a punchline. Pair it with the broader stdlib-discipline argument if M7 doesn't tank that story by needing `mattn/go-isatty`.
