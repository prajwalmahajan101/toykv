# 16 — Roadmap: commit to the full v2.0.0 arc

**Date:** 2026-07-13
**Branch:** `docs/roadmap-v2`
**PR:** [#24](https://github.com/prajwalmahajan101/toykv/pull/24) — rebase-merged onto `main` at `0f67df3`
**Trigger:** v1.0.0 shipped 2026-06-17 with the v2 section of `ROADMAP.md` deliberately framed as *"proposed, optional — not committed."* The roadmap's own [Honest framing](../ROADMAP.md#honest-framing--pick-one-trajectory) table said the trajectory decision would be *"reviewed after v1 sees real use, not before."* Roughly four weeks of real use later, that review is due — and the answer is to commit to the full **M10–M15** arc (Option B: v1 → v2) rather than leave v2 in permanent maybe-land.

## Decision / surprise

The change is entirely documentation — no code — but it records a real decision, so it earns an entry.

1. **Option B over A or C.** The roadmap always carried three honest paths: **A** (ship v1, stop), **B** (run the v2 arc to "usable single-node"), **C** (skip/trim v2, jump straight to the Raft-distributed v3). The default-on-paper was **A**. This PR flips that to **B**, decided 2026-07-13. The deciding factor wasn't feature-lust; it was that the three gaps that actually bite in real use — no auth, string-only values, and `KEYS *`-only iteration — are exactly what M12 / M11 / M13 close. C stays live the moment `ToyRaft` ships as a vendorable library; nothing here forecloses it.

2. **RESP3 is additive, not a migration.** The most misreadable line in the plan is "moving to RESP3." It is opt-in via `HELLO 3`; RESP2 stays the default and is never broken, and M10 owns a dual-protocol compat sweep proving RESP2 golden replies are byte-identical. RESP3 lands *first* (M10, before types) only so M11's typed replies (`HGETALL` → map) exercise its frames as a real second use case — the same bottom-up logic that put AOF before TTL in v1.

3. **Commitment ≠ irreversibility.** The optional framing wasn't deleted, it was subordinated. The backlog tracker's "minimal v2 = AUTH+TLS only" is preserved as an explicit fallback: if scope tightens mid-cycle, v2 can retreat to M12-only without abandoning the release. Recording the escape hatch alongside the commitment is what keeps the commitment honest.

## Why it mattered

- **A roadmap that says "optional, undecided" indefinitely is a roadmap nobody plans against.** Flipping the header, banner, status table, and honest-framing default from "proposed/optional" to "committed" turns M10–M15 from a wishlist into a sequence with a start. The status table cells now read `⏳ Planned (committed)`.
- **Per-milestone dependency + ADR ownership was the substantive add, not just reframing.** Each of M10–M15 now names what it depends on and which ADR it owns (RESP3 negotiation → M10, tagged-union store + AOF v3 → M11, AUTH/TLS → M12). This makes ADRs a per-milestone deliverable written when the decision is fresh, rather than a batch of four hand-waved at release time in M15 — mirroring v1's discipline (`docs/adr/README.md`: ADRs land after their owning milestone merges).
- **The diff is self-documenting.** Two entries appended to "Changes from the previous roadmap" record both the commitment decision and the dependency/ADR annotations, so a future reader diffing the roadmap sees *why* it changed without archaeology.

## Code / measurement

- `docs/ROADMAP.md` only — +114 / −24.
- Banner + `## v2.0` header: `(proposed, optional — not committed)` → `(committed — the active plan post-v1)`.
- "This whole milestone is optional" blockquote → a decision-recorded note (Option B, 2026-07-13) with the caveats kept as subordinate fallback lines.
- Honest-framing trailer: `Default … Option A` → `Decided 2026-07-13: Option B`, three-option table preserved.
- Status table: six `⏳ Planned (optional)` → `⏳ Planned (committed)`.
- M10–M15 each gained a **Depends on** + **ADR** annotation line; M15 now *verifies* the four ADRs landed rather than batch-writing them.
- CI on the PR: `lint`, both macOS cells, ubuntu Go 1.25.x, `chaos-smoke`, GitGuardian all green. One red cell — `test (ubuntu-latest / go 1.26.x)` — was `TestAOF_CrashInjection_DuringRewrite/late-kill`, a timing-sensitive SIGKILL-during-rewrite crash test that passed on the three other matrix cells. Flaky, pre-existing, and untouchable by a docs-only diff; `mergeStateStatus` was `UNSTABLE`, not blocked. Merged on that basis.

## Follow-ups

- **M10 kicks off the arc.** First real code branch is `feat/resp3` — `HELLO` negotiation + per-connection protocol state + the RESP3 encoders, with the dual-protocol compat sweep as its owned risk test.
- **ADR-per-milestone starts at M10.** The RESP3-negotiation ADR is the first of the four v2 ADRs; write it after `feat/resp3` merges, not at M15.
- **Revisit C when `ToyRaft` tags a vendorable release.** If that lands before M13, seriously weigh trimming v2 to AUTH+TLS and pivoting to v3 — the tracker is explicit that v3 (multi-node) is the real downstream dependency, v2 is polish.

## Reflection

The useful pattern here is that "decide later" was itself a recorded, dated commitment in the v1 roadmap — not a vague intention — so honoring it was mechanical: the trigger condition ("v1 sees real use") was met, so the review happened on schedule instead of drifting. Roadmaps that bury their decision points in prose let those points rot; roadmaps that write them as explicit gates ("reviewed after v1 sees real use") get honored. The other thing worth keeping: committing to a plan and keeping the exit hatch documented are not in tension. The minimal-v2 fallback and the live Option C aren't hedging that weakens the commitment — they're what let the commitment be made confidently, because the cost of being wrong is bounded and written down.
