# Tasks — Action Items Backlog

> Things we identified during planning/review that aren't blocking the current milestone but need to land later. Strike items as PRs merge.

Each task lists: **what** (the change), **why** (the trigger / reason), and **when** (the gating milestone or "any time"). Roll into the relevant milestone's PR or land as its own small PR — keep PRs single-purpose.

---

## CI & tooling

- [ ] **Enforce `gofmt -l` in CI** — currently gofmt only runs via golangci-lint, which means a malformed file passes if golangci-lint accepts it. toymq inlines `gofmt -l .` in the test job; mirror it.
  - *When:* any time before M2 lands. One-file edit to `.github/workflows/ci.yml`.
- [ ] **Verbose golangci-lint output** — add `--out-format=colored-line-number --print-issued-lines` to the lint job. M1's first PR failed lint on `builtinShadow` and the log was hard to parse; toymq's flags would have surfaced the exact line immediately.
  - *When:* same PR as the gofmt enforcement.
- [ ] **Add `release.yml` (goreleaser)** — triggers on `v*.*.*` tags, builds linux+darwin × amd64+arm64 archives + `SHA256SUMS`, ships `toykv`, `toykv-cli`, `toykv-tui`.
  - *When:* M9 (release milestone). Defer until then.
- [ ] **Add `chaos` CI job** — `go test -tags chaos -race -timeout 3m ./test/chaos/...` once chaos tests exist.
  - *When:* M8 (integration tests milestone). Defer until then.
- [ ] **Add `docker` CI job** — only after a Dockerfile exists.
  - *When:* v2 if at all. Not in v1 scope.

---

## ADRs (post-milestone follow-ups)

ADRs are written **after** the corresponding milestone PR merges, per [`docs/adr/README.md`](./docs/adr/README.md). Each ADR lands as its own small PR linked from the milestone PR description.

- [ ] **ADR-0001 — RESP2 subset & error-reply shape** — captures the wire-protocol scope and the `-ERR …` error format.
  - *Gated by:* M1 (done).
  - *Status:* outstanding, blocks "M1 fully closed."
- [ ] **ADR-0002 — Single `sync.RWMutex` over the store** — captures the writer-heavy contention tradeoff documented in HLD §7.
  - *Gated by:* M2 (store core).
- [ ] **ADR-0003 — AOF format & version byte** — captures the on-disk layout, version handling, and replay contract from LLD §4.
  - *Gated by:* M3 (AOF persistence — moved up from M4 per risk-first roadmap).
- [ ] **ADR-0004 — TUI-over-RESP (no in-process coupling)** — captures why the TUI is a plain RESP client and what we'd lose by sharing process state.
  - *Gated by:* M7 (TUI).

## ADR template — adopt toymq parity

- [ ] **Update `docs/adr/README.md` template** to match toymq's ADR-0001 structure:
  - Add `**Status:**` + `**Date:**` + `**Scope:** <file path>` header block.
  - Add `## Tests that lock this contract` section at the end (lists test names that would fail if the decision is broken).
  - *When:* before ADR-0001 lands (so the first written ADR uses the new shape).

---

## Documentation refresh

- [ ] **Update ROADMAP status table for M1** — flip M1 ⬜ → ✅ with PR (#3) and tag (`v0.1.0`) links. Same pattern as the M0 status update PR (#2).
  - *When:* now or before M2 PR opens.
- [ ] **README — install + 30-sec quickstart** — match toymq's shape: `go install` lines for all three binaries, a `redis-cli`/`nc` example showing PING/ECHO on the wire, link out to PRD/HLD/LLD.
  - *When:* any time post-M1. Grows again at each milestone that adds user-visible commands.
- [ ] **README — "What surprised me" section** — five-bullet personal-narrative section in toymq's style (e.g. "fsync latency dominates everything"). Distinguishes a portfolio repo from a Redis-clone tutorial.
  - *When:* M9 / v1.0.0 release. Defer until we actually have surprises to write about.
- [ ] **README — wire-protocol table on-page** — supported commands as a Redis-compat table inline in README (canonical list stays in PRD §5.1, but README should show it without a click).
  - *When:* grows with each command-adding milestone (M2, M3).
- [ ] **HLD / LLD frontmatter — flip "Draft — pre-implementation" → "Living — last sync vM<n>"** as each milestone updates the doc to reflect shipped code.
  - *When:* per milestone, in the same PR that touches the doc.
- [ ] **Reconsider splitting HLD/LLD** — toymq has separate `ARCHITECTURE.md`, `CONCURRENCY.md`, `FLOWS.md`, `PERSISTENCE.md`. We folded them into HLD+LLD. Revisit if HLD.md grows past ~600 lines.
  - *When:* M4 (AOF + replay) introduces real concurrency complexity. Decide then.

---

## CHANGELOG hygiene

- [ ] **Add `[v0.1.0]` entry to CHANGELOG.md** with the M1 highlights (RESP2 codec, PING/ECHO, slog wiring).
  - *When:* now or in the M1 follow-up PR.
- [ ] **Convention check** — each milestone PR should append entries to `## [Unreleased]`; release cuts move them under a versioned heading.
  - *When:* enforce manually on each PR until/unless a release-please-style bot is added.

---

## Repo hygiene

- [ ] **`.gitattributes`** — pin line endings (`* text=auto eol=lf`) so cross-platform clones don't churn diffs.
  - *When:* any time. Trivial.
- [ ] **`SUPPORT.md` / issue templates** — only worth adding if external users show up. Defer indefinitely.

---

## Possibly out of scope (don't add unless you change your mind)

These were considered and rejected for now — listed so future-you knows the decisions were intentional, not oversights.

- ❌ **`IDEA.md`** (toymq has one) — toykv's ROADMAP v2/v3 sections already cover the structured backlog. Adding `IDEA.md` would duplicate.
- ❌ **`docs/blog/`** (toymq has one) — fine to add if you write something, but no need to scaffold an empty directory.
- ❌ **Public `pkg/` directory** — LLD §1 commits to internal-only. No exported library surface in v1.
- ❌ **Dockerfile / docker-compose** — localhost-only v1. Reconsider only if v2 adds AUTH.
- ❌ **Observability stack** (Prometheus / OTel) — v2 work per ROADMAP.

---

## Quick reference: open follow-ups blocking "M1 fully closed"

These three should land before starting M2 so we don't accumulate doc debt:

1. ROADMAP status update (M1 ✅)
2. ADR-0001 (RESP2 subset & error shape)
3. CI: gofmt enforcement + verbose lint output

Everything else can wait.
