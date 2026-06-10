# Architecture Decision Records

> **ADRs are written *after* the code lands**, not before. Pre-code decisions live in [`HLD.md`](../HLD.md) and [`LLD.md`](../LLD.md). An ADR is the post-hoc, link-from-the-PR record of *why a call was made*, written when the call survives the first implementation pass.

## Planned ADRs

These will be created as the corresponding milestones complete. **Numbers are reserved, files are not.**

| # | Title | When written | Milestone |
|---|---|---|---|
| 0001 | RESP2 subset and error-reply shape | After M1 lands | M1 |
| 0002 | AOF format and version byte | After M4 lands | M4 |
| 0003 | Single `sync.RWMutex` over the store | After M2 lands | M2 |
| 0004 | TUI-over-RESP (no in-process coupling) | After M6 lands | M6 |

**Budget: 4.** This breaks the "no more ADRs than toymq" parity rule in the source spec (toymq landed 3). Justification: adding a second binary that shares the wire protocol is itself an architecture decision worth recording. Documented here so the parity break is intentional, not accidental.

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
