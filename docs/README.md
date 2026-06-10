# toykv — docs

Single-node in-memory KV store in Go, RESP2 wire protocol, AOF persistence, TUI. Companion to `toymq`.

## Read order

| When | Read |
|---|---|
| First time on the project | [`PRD.md`](./PRD.md) — what it is, what done looks like |
| Planning the build | [`ROADMAP.md`](./ROADMAP.md) — milestones M0..M8 |
| Reviewing the architecture | [`HLD.md`](./HLD.md) — components, lifecycle, boundaries |
| Writing code | [`LLD.md`](./LLD.md) — types, signatures, byte layouts |
| Writing tests | [`TESTING.md`](./TESTING.md) — five-layer strategy |
| Cutting a release | [`RELEASE_PLAN.md`](./RELEASE_PLAN.md) — versioning, exit criteria |
| Deploying anywhere | [`SECURITY.md`](./SECURITY.md) — threat model, defaults |
| Contributing | [`CONTRIBUTING.md`](./CONTRIBUTING.md) — branches, commits, PRs |
| Reading perf numbers | [`BENCHMARKS.md`](./BENCHMARKS.md) — methodology, recorded runs |
| Why something was done | [`adr/`](./adr/) — architecture decisions (post-code) |

## Source of truth

This `docs/` tree mirrors the original spec at `~/work/project-todo/projects/tinykv.md` (note: the spec file is named `tinykv.md` for historical reasons; the project itself is named `toykv` to match the directory). If the two diverge, **this `docs/` tree wins** — it is updated as the code evolves; the spec is a frozen pitch.

## Document status

| Doc | Status |
|---|---|
| PRD | Draft — pre-implementation |
| ROADMAP | Draft — pre-implementation |
| HLD | Draft — pre-implementation |
| LLD | Draft — pre-implementation |
| TESTING | Draft — pre-implementation |
| BENCHMARKS | Empty — populated post-M8 |
| RELEASE_PLAN | Draft — pre-implementation |
| SECURITY | Draft — pre-implementation |
| CONTRIBUTING | Draft — pre-implementation |
| ADRs | None yet — written as milestones land |
