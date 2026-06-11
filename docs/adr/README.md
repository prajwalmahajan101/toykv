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
| 0005 | TUI-over-RESP (no in-process coupling) | After M7 lands | M7 |

*(Numbers track chronological landing order. TTL slotted in at 0004 when M4 closed — the version-byte bump and absolute-deadline persistence rule were both real architectural decisions worth recording; TUI bumped to 0005.)*

**Budget: 5.** Already broke the "no more ADRs than toymq" parity rule (toymq landed 3) when 0004 was the TUI binary; now sitting at 5 with TTL slotted in. Justifications: (a) the TUI is a second binary sharing the wire protocol — a real architecture decision; (b) the AOF v2 format and the absolute-deadline rule for TTL persistence had design-time alternatives (relative PX, separate binary expiry field) that we want to record the rejection of. Both are intentional parity breaks.

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
