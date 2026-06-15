# Implementation journal

Raw notes, decisions, failed approaches, micro-benchmark numbers, race-detector findings, and anything that would lose flavour if recalled later. This directory is **gitignored**. Nothing here ships; everything here is potential blog material.

## Rules

- **Every PR merge gets a journal entry.** No exceptions — even a one-paragraph "merged, nothing surprising" entry counts. The merge moment is when hindsight is freshest; capture it before the next branch starts. Naming: `NN-<slug>-merged.md` or `NN-<topic>.md` if the entry is broader than the merge.
- One file per session or topic. Kebab-case names with a leading ordinal so they sort: `00-kickoff.md`, `01-store-shape.md`, `02-incr-overflow.md`, …
- Entry skeleton (don't be precious — bullets > prose):
  - **Date**
  - **Decision / surprise** — one line.
  - **Why it mattered** — the bit a reader cares about.
  - **Code or measurement** — actual snippet / numbers / commit-hash if useful.
  - **Blog-worthy?** — one-line note on what to quote.
- Capture liberally: things that felt obvious in the moment go bland in a post written a month later. The journal is the antidote.
- **No secrets, no API keys, no full machine paths beyond the working dir.** If a stack trace exposes something, redact before saving.
- Don't edit old entries to look smarter in hindsight. Add a new entry instead — "I was wrong about X because Y" is the best kind of blog material.

## What belongs here vs. elsewhere

| If it's… | Goes to… |
|---|---|
| An architectural decision (we'll defend in a year) | `docs/adr/000N-<slug>.md` (committed) |
| A user-facing design constraint | `docs/PRD.md` / `docs/HLD.md` / `docs/LLD.md` (committed) |
| A milestone status flip | `docs/ROADMAP.md` (committed) |
| A raw note, a failed approach, a benchmark snapshot, an "I was surprised when…" | **here** (uncommitted) |
