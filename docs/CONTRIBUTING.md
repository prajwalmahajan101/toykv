# Contributing to toykv

> This is a personal learning project. PRs from outside the author are welcome but not actively solicited. Issues are.

## Quick start

```bash
git clone https://github.com/prajwalmahajan101/toykv
cd toykv
make build       # builds bin/toykv and bin/toykv-tui
make test        # unit + integration with -race
make run         # starts server on :6390
make tui         # starts TUI against :6390
```

Go 1.26 required.

## Branching

- `main` is always green and tagged-ready.
- Feature branches: `feat/<slug>`. Bugfixes: `fix/<slug>`. Docs: `docs/<slug>`.
- **No direct commits to `main`.** PR + green CI, even for the author.

## Commit messages

Conventional Commits.

```
feat(store): add INCR overflow detection
fix(aof): handle short writes during rewrite
docs(adr): record single-mutex decision
refactor(server): extract dispatcher into its own file
test(store): cover TTL boundary cases
chore(ci): bump golangci-lint to v1.62
```

Subject ≤ 72 chars, imperative, no trailing period.

**Do not append `Co-Authored-By` AI attribution footers.** (Repo convention.)

## Pull requests

A good PR has:

1. One logical change. If you're touching two unrelated things, that's two PRs.
2. A description explaining the *why*. The *what* is in the diff.
3. Tests for the new behaviour. No tests = no merge unless the change is docs-only.
4. Green CI (`lint`, `unit`, `integration`, `e2e`).
5. Updated docs if the PR changes user-visible behaviour or architecture.

## Architecture changes

If the PR introduces a new component, changes a boundary, or alters a documented decision:

1. Update the relevant doc (`HLD.md`, `LLD.md`).
2. **Add an ADR** to `docs/adr/`. ADRs are post-decision records — write one when you make the call, not when you finish the code.
3. Link the ADR from the PR description.

ADR template lives at `docs/adr/_TEMPLATE.md` (added once the first real ADR lands).

## Coding conventions

- `gofmt`, `goimports`, `golangci-lint run` clean.
- Errors carry context (`fmt.Errorf("aof replay at offset %d: %w", off, err)`).
- No silent `try/except`-style swallowing. If an error is intentionally ignored, name it (`_ = f.Close() // best-effort on shutdown`).
- Public symbols (within `internal/`) get a doc comment when their purpose isn't obvious from the name. Stdlib comment style.
- No comments that restate what the code does.

## Testing

See [`TESTING.md`](./TESTING.md). Minimum bar for a PR:

- Unit tests for new logic in the leaf package.
- Integration test if the PR changes a wire-protocol command.
- Crash-injection test if the PR touches AOF write or replay.

## Code review

PRs are reviewed against:

- Does the PR match its description?
- Does the diff scope match the title?
- Do tests cover the new branches?
- Are docs updated?
- Are any new dependencies added? (Server: must be stdlib. TUI: must be `charmbracelet/*` or test-only.)

## Releases

See [`RELEASE_PLAN.md`](./RELEASE_PLAN.md). Tagged by maintainer only.
