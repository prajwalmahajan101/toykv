# ADR-0008: `toykv-cli` — stdlib-only, TTY-detected modes, single shared connection

- Status: Accepted
- Date: 2026-06-15
- Milestone: M6
- PR: `feat/m6-cli` (M6 PR B)

## Context

M6 ships `toykv-cli`, a line-oriented RESP2 client modelled on `redis-cli`. PRD §5.6 fixes three modes (one-shot, REPL, piped), pretty-printed replies with a `-raw` opt-out, and exit-status mapping (`0` ok, `1` `-ERR`, `2` conn/parse failure). PRD §6 pins the CLI to stdlib only (`net`, `bufio`, `flag`, `os`) — explicitly *"no readline lib in v1"*.

Three calls had real alternatives worth recording:

1. **REPL ergonomics with no readline library.** No history-search, no line editing, no completion in v1.
2. **How modes are selected.** No `-repl` / `-pipe` flag — the binary infers the mode from stdin.
3. **Connection lifetime.** REPL/piped reuse one `net.Conn` across all commands; a transport error is fatal to the process.

This is also the first real consumer of `internal/client`, so the contract surface gets stress-tested here.

## Decision

**Stdlib-only; mode selected by `isTTY(os.Stdin)`; one `*client.Client` for the process lifetime; transport errors are fatal (no auto-reconnect).**

Concretely:
- Modes: `flag.NArg() > 0` → one-shot · stdin is a char-device → REPL · otherwise → piped. No mode flag.
- REPL has *no* history, *no* line editing — `bufio.ReadString('\n')` plus a prompt. `quit` / `exit` / Ctrl-D exit cleanly.
- Tokeniser is hand-rolled: whitespace split, `"…"` with `\n \t \r \0 \a \b \f \v \\ \"` escapes, `'…'` literal (matching `redis-cli`'s convention). Parse errors print `(error) parse: …` on stderr.
- One shared `*client.Client` is dialled once and reused. `client.ErrClosed` propagates to the user and exits with `2`. No retry loop, no reconnect.
- Exit code in REPL/piped reflects the **last** reply, reset per iteration; parse errors yield `1`, transport errors `2`.
- TTY detection: `os.Stdin.(*os.File).Stat()` → `Mode()&os.ModeCharDevice` — no `golang.org/x/term`.

## Consequences

**Positive**
- Zero third-party deps in the v1 client binary — same `go build ./cmd/toykv-cli` story as the server. Matches PRD §6.
- Mode selection is invisible to the user — `toykv-cli SET k v`, interactive shell, and `echo … | toykv-cli` all "just work" without flag-tuning.
- A single shared connection mirrors PRD §5.6 *"Connection is a single `net.Conn` reused across commands"* and exercises `internal/client`'s "fail-and-stay-failed" contract (`client.go:17-20`) end-to-end — the exact behaviour any future `toykv-go` SDK consumer will inherit.
- Fatal-on-transport-error keeps the failure model simple: a flaky CLI session is a flaky network, surfaced loudly, not papered over with silent reconnects that could mask AOF-replay or server-drain issues.

**Negative**
- No REPL history, no arrow-key recall, no Ctrl-R search. Users who want `redis-cli` ergonomics will feel the gap; mitigated by `rlwrap toykv-cli` as a known workaround.
- No reconnect after a server restart — the user has to relaunch the CLI. Acceptable for v1 (single-node, single-user); a future-v2 SDK with retry policy belongs in `toykv-go`, not in the CLI.
- Mode auto-detection has one edge case: a TTY-attached process that *also* has data piped from a heredoc-with-no-redirection is still classified as TTY. Not a real-world failure mode; documented here for honesty.

**Neutral**
- Tokeniser semantics are *close to* but not byte-identical with `redis-cli`'s shell parser (e.g. `redis-cli` accepts `\xNN` hex escapes; this one doesn't). Cross-binary command lines that rely on advanced quoting will need adjustment; the supported subset covers every command in PRD §5.1.

## Alternatives considered

- **`peterh/liner` or `chzyer/readline` for REPL history.** Rejected — drags in a transitive C/Win32 surface for a feature explicitly out of v1 per PRD §6. Costs more than it earns at this stage.
- **`-mode {oneshot,repl,pipe}` flag.** Rejected — mode-by-stdin is a `redis-cli` convention users already know, and adding a flag invites `--mode repl < file.txt` weirdness. TTY detection is one stdlib call.
- **Auto-reconnect on transport error.** Rejected — hides server crashes and AOF-replay windows. The user's mental model should be "the CLI is a thin shell over a single TCP connection"; reconnect logic belongs in the SDK retry layer (ADR-0007), not here.
- **Pool of connections / pipelining.** Rejected — `internal/client.Client` serialises `Do` calls behind a mutex by design (`client.go:22-25`), and pipelining is explicitly out of scope for v1 (`internal/client/client.go:1-4`). One connection is the right shape for both REPL throughput (interactive) and piped streams (single producer).

## References

- PRD.md §5.6 — CLI behaviour spec
- PRD.md §6 — stdlib-only CLI deps
- ADR-0007 — SDK strategy (the CLI's plumbing is the seed for `toykv-go`'s extracted package)
- `internal/client/client.go` — single-connection, fail-and-stay-failed contract
- ROADMAP.md §M6 — exit criteria
