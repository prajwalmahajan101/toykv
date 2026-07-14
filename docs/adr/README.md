# Architecture Decision Records

> **ADRs are written *after* the code lands**, not before. Pre-code decisions live in [`HLD.md`](../HLD.md) and [`LLD.md`](../LLD.md). An ADR is the post-hoc, link-from-the-PR record of *why a call was made*, written when the call survives the first implementation pass.

## Planned ADRs

These will be created as the corresponding milestones complete. **Numbers are reserved, files are not.**

| # | Title | When written | Milestone |
|---|---|---|---|
| 0001 | RESP2 subset and error-reply shape | After M1 lands | M1 |
| 0002 | Single `sync.RWMutex` over the store | After M2 lands | M2 |
| 0003 | AOF format and version byte | After M3 lands | M3 |
| 0004 | TTL canonical PXAT encoding (AOF v2) | After M4 lands | M4 |
| 0005 | BGREWRITEAOF — dual-write side buffer + atomic-rename swap | After M5 lands | M5 |
| 0006 | TUI-over-RESP (no in-process coupling) | After M7 lands | M7 |
| 0008 | `toykv-cli` — stdlib only, mode detection | After M6 lands | M6 |
| 0009 | `toykv-tui` — Bubble Tea, injectable `Doer` | After M7 lands | M7 |
| 0010 | v1 release artefacts — Goreleaser, three binaries, SHA256SUMS | After M9 lands | M9 |
| 0011 | RESP3 negotiation & per-connection protocol state | After M10 lands | M10 |
| 0012 | Tagged-union store model & AOF v3 format | After M11 lands | M11 |
| 0013 | AUTH model & TLS termination | After M12 lands | M12 |

*(Numbers track chronological landing order. TTL slotted in at 0004 when M4 closed and BGREWRITEAOF took 0005 when M5 closed — each was a real architectural decision worth recording; TUI bumped from 0004 → 0005 → 0006. 0008 and 0009 followed M6 and M7. 0010 lands with M9 because the release artefact policy — channels, archive shape, checksum surface — closes off real alternatives that the v2 roadmap may want to revisit.)*

**Budget: 10 written (0003, 0004, 0005, 0008, 0009, 0010, 0011, 0012, 0013 — 0006 was superseded by 0009 before being written). 0013 clears the bar as a contract pair: the dispatch-level gating invariant (no command reaches a handler unauthenticated; the {AUTH, HELLO, PING} whitelist as a documented Redis deviation) and the transport boundary (TLS as a listener wrap, config-pair-or-refuse). 0012 clears the bar the same way 0011 did: a store-model contract (tagged union + `WRONGTYPE` at the store boundary) and a format invariant (header version ≥ newest record format, enforced by the in-place upgrade on Open). 0011 opens the v2 ADR set: it clears the bar as a real contract (the RESP2-golden invariant + the single-downgrade-point boundary) that M11–M15 build on. The "no more ADRs than toymq" parity rule was intentionally broken once the TUI brought a real dep tree and the release decision turned out to have non-trivial alternatives. M8 took zero ADRs (protocol-compat tests, not architecture). The rule of thumb at the bottom of this file still applies — each new ADR has to clear "real architectural decision (boundary, contract, invariant)" or it's an LLD note.**

## Format

Each ADR is a numbered file: `0001-resp2-subset.md`, `0002-aof-format.md`, etc.

Template:

```markdown
# ADR-NNNN: <Title>

- Status: Accepted | Superseded by ADR-XXXX | Deprecated
- Date: YYYY-MM-DD
- Milestone: M<n>
- PR: #<n>

## Context
What problem are we solving? What constraints apply?

## Decision
The call. Stated as a single sentence first, then elaborated.

## Consequences
Positive, negative, and neutral. What does this make easy/hard?

## Alternatives considered
Each with a 1–2 sentence reason for rejection.

## References
- HLD.md §<n>
- LLD.md §<n>
- Related ADRs: [[NNNN]]
```

The template will land as `_TEMPLATE.md` when ADR-0001 is written.

## Rule of thumb

If you find yourself wanting a *fifth* ADR, ask:
1. Is it a real architectural decision (boundary, contract, invariant)?
2. Or is it an implementation detail dressed up?

If (2), it belongs in `LLD.md` or a code comment. Resist the proliferation.
