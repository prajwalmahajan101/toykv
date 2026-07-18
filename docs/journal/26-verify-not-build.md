# 26 — The "polish" milestone was the first verification pass — and that's where the bugs were

**Date:** 2026-07-18
**Context:** M17 (release + `v2.0.0`), companion to entry `25-m17-release-v2.md`. This one isolates the through-line, because it's the most transferable thing the whole v2 arc taught.

## Decision / surprise

M17 was scoped as **bench + polish + tag** — the milestone you expect to rubber-stamp. Instead it surfaced the **two most interesting bugs of the entire v2 cycle**, and both were *already-shipped* code from M1 and M16. The reason is simple and worth internalising: **M17 was the first time the v2 surface was *verified* rather than *built*.** Every prior milestone proved its own feature works; nobody had gone back and adversarially checked the seams between them, or run the parity benchmark the gate assumed was green.

The two bugs, both found + fixed before the release merged:

- 🔴 **Two pre-auth RESP-codec DoS vectors** (`ef8a423`). The security review was scoped at the M12/M15 auth/TLS/protected-mode surface — which came back **clean**. But it looked one layer down, at the codec that runs *before* the auth gate, and found `readArray` doing `make([]Value, n)` with `n` unbounded (single-packet memory-amplification OOM) and unbounded recursion depth (stack-exhaustion *fatal* panic). One tiny packet from any peer that can reach the port, no credentials. This defeats the exact "safe-by-default" claim that earns the `2.0.0` major. Fixed with `MaxArrayLen` (1<<20) + `MaxDepth` (32), rejecting before allocation. **A clean bill on the *named* surface is not a clean bill on the *reachable* surface.**

- 🔴 **~20% OTel-off throughput regression** (`980dceb`, ADR-0017 amended). The gate required off-by-default OpenTelemetry to be benchmark-parity with the pre-M16 binary. The parity benchmark *existed* — but nobody had run the actual A/B against pre-M16. Doing it showed SET −21% / GET −18%, consistent across every round. Root cause: **building an `attribute.Set` or a `metric` option allocates even against no-op providers**, so `observeCommand` rebuilding them per command taxed the disabled path. ADR-0017's "no-op providers make it free, no guard needed" was simply false. Fixed by memoizing per-command instrument attributes — **no `if enabled` hot-path guard added** (maintainer's call to keep the guardless design) — 29 → 14 allocs/op, SET back to parity.

## Why it mattered

- **Verification is a distinct activity from building, and it has to actually run.** Both bugs would have shipped in `2.0.0` if I'd trusted the roadmap's "gates green." The recall lesson said it outright — *a roadmap records intended, not verified, state* — and this milestone is the proof: two gate items marked green were regressions.
- **The gate is only worth the evidence you produce this session.** The OTel parity benchmark was in the repo the whole time; it was worthless until someone ran the A/B. A gate you re-*read* instead of re-*run* is theatre.
- **Scope the review one layer wider than the named surface.** The auth review found the DoS precisely because it didn't stop at auth — it asked "what runs before this?"

## Blog-worthy?

Yes — this is the headline. "The milestone I expected to rubber-stamp is where I found the two scariest bugs, because it was the first time I *checked* instead of *built*." Pair it with the concrete OTel lesson ("no-op providers are not free — attribute/option construction allocates regardless") and the security lesson ("a clean audit of the named surface missed a pre-auth DoS one layer down"). The meta-point generalises past this project: **budget real verification time as its own phase; don't fold it into "polish."**
