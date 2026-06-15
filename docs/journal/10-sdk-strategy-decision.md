# 10 — SDK strategy decision (ADR-0007), pre-code

**Date:** 2026-06-15
**Branch:** `main` (no code yet — decision-only entry)
**ADR:** [`docs/adr/0007-sdk-strategy.md`](../adr/0007-sdk-strategy.md), status *Proposed*.
**Trigger:** scoping ToyMessenger — an E2E-encrypted Go TUI chat that will consume both toykv and toymq (incl. their Raft variants). The "do I need a client library" question forced the SDK-strategy decision.

## Decision / surprise

Ship `toykv-go` as a **separate repo**, first tag **after M8** (when M8 locks the wire format), version-locked to **toykv v1.0** at M9. Python and Java SDKs are deferred until a non-Go consumer exists — building them now would be three SDKs maintained for one consumer.

The non-obvious bit is the *timing*. Reflex would have been "start the SDK now while I'm thinking about it." But M8 is literally the protocol-compat milestone — releasing an SDK before that lock would burn the SDK's whole v0 cycle on churn the server hasn't finished. Worse, it would weaken the portfolio story: a v0 → v1 → v2 SDK shipped against a moving server reads as instability, not iteration. Holding the SDK until M8 makes its v1.0 land *simultaneously* with the server's v1.0, which is the version-discipline signal that's worth more than the head start.

The other resisted move: bundling SDKs into the server repo as a `sdk/` subdirectory. Single-repo is genuinely simpler, but every Redis-ecosystem client (`go-redis`, `redis-py`, `jedis`) lives in its own repo for a reason — decoupled release cadence, independent semver, language-specific CI. Cloning that convention costs nothing and signals "I read how the ecosystem actually works," which is the entire point of the exercise.

## Why it mattered

Three downstream effects:

1. **ToyMessenger architecture is now decidable.** Without the SDK decision, the chat client would have either (a) duplicated raw RESP plumbing per consumer, or (b) prematurely extracted a half-baked client mid-feature. The plan is now: start with `internal/toykvclient` *inside* toymessenger, let it stabilise against real consumer pressure, then extract to `toykv-go` once the API stops moving. That sequencing means the SDK's first public release is *already validated by a real consumer*, not designed in a vacuum.

2. **The portfolio shape is now five repos, not one.** `toykv` + `toykv-go` + `toymq` + `toymq-go` + `toymessenger`, with the optional `toy-stack` meta-repo for a docker-compose demo. That's a "I designed a distributed stack with multi-repo release engineering" artifact, not a "I built a server" artifact. The same code, framed differently.

3. **Raft becomes the planned v2.0 break.** Recording now that Raft (toykv v3.0 milestone) will bump `toykv-go` to v2.0 means the breaking-change budget is *expected*, not a surprise. Leader-redirect, follower-read options, retry semantics — all flagged for v2 before any of it gets prematurely modelled in v1.

The ADR was written **before** the code per `docs/adr/README.md`'s explicit "ADRs are written *after* the code lands" rule — broken intentionally here, with status `Proposed` rather than `Accepted`, because the decision is cross-project (it shapes ToyMessenger and toymq simultaneously) and waiting for code would mean three repos drifting. The README's rule survives — most ADRs in this project still wait for code — but this one is a deliberate exception.

## Code / measurement

- ADR-0007 created at `docs/adr/0007-sdk-strategy.md` (~70 lines, status *Proposed*).
- No code changes. No commits yet.
- Roadmap unchanged: still M0–M5 ✅, M6–M9 pending. The SDK timing slots between M8 and M9, so this doesn't reorder any phase.

## Score

- One decision recorded before it could quietly drift into "well, I guess we'll just figure it out when toymessenger needs it."
- One explicit deferral (Python/Java) — the kind of negative scope that's easy to skip recording and painful to argue later.
- One cross-project linkage (toymq parallel decision) noted, so the sibling repo's ADR can cite this one when it lands.

## What's next

- **Immediate:** finish toykv M6 (CLI). No SDK work until M8.
- **At M8 close:** spin up `toykv-go` repo, port `internal/client/` from M6 as the seed package, tag v0.1.
- **At M9 close:** version-lock `toykv-go v1.0.0` to `toykv v1.0.0`, set up release-tag-triggered CI between the two.
- **Open question for toymessenger kickoff:** does the chat protocol live in a third repo (`toymessenger-proto`?) or stay internal? Defer until E2E crypto design is sketched — the protocol decision and the SDK decision are independent, but both will want ADRs of their own.

## Blog-worthy?

Yes — two threads:

1. **"Why I waited to release my client library"** — concrete example of resisting the reflex to ship the SDK first. The version-discipline argument (SDK v1.0 ⇔ server v1.0) is the kind of thing junior-me would have skipped past and is exactly what makes a small project look professionally maintained.

2. **"How one chat app forced five repos"** — the ToyMessenger consumer pressure is what makes the SDK split a real architectural decision instead of cargo-culted Redis-ecosystem mimicry. Worth a post once ToyMessenger actually consumes the first `toykv-go v0.1`, so the receipts are real.
