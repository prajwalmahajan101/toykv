# 33 — M22: TUI v3 cluster view, and the milestone that was mostly already shipped

**Date:** 2026-09-09
**Context:** M22 complete on `feat/tui-cluster-view`. The TUI gains a cluster pane that
surfaces the `# Replication` topology already carried by the `INFO` body it polls every
tick, and reflects a leadership change without any user interaction. No ADR — a pure
consumer of M20 (`ClusterClient`) and M21 (`INFO replication`), per the M13/M14 precedent.

## Decision / surprise

**Half the milestone was already done.** The roadmap's M22 line bundles "AUTH +
redirect-aware connect" with the cluster view. But the TUI has dialled through
`ClusterClient` since M20 and has handled the `NOAUTH` masked-password prompt since M14 —
so the "connect" half needed zero work. M22 collapsed to a single job: render the
replication state the model already receives.

That reframing is what kept the diff small. `doInfo()` already fetches the full `INFO`
body (my M21 code gates the `# Replication` section under the default section set), and
`parseInfo` already walks it line by line. The cluster view is a *parse-and-render*
feature end to end — four new `parseInfo` cases, one `replStatus` field on the model, one
`renderCluster` function, and the layout wiring. No new command, no new round-trip, no
client change.

**Standalone must stay byte-identical to v2.** The pane is gated on `m.repl.role != ""`.
A non-replicated server never emits a `# Replication` section (the server gates it on
`s.replicated`), so `parseInfo` leaves `role` empty, `clusterVisible()` returns false, and
every layout branch falls through to the exact two-pane code v2 shipped. The
`TestClusterPane_StandaloneHidesPane` test pins this: no `role:` line ⇒ no `cluster`
pane in the rendered frame.

## Why it mattered

- **teatest was the wrong tool, so I didn't use it.** The roadmap named `teatest` for the
  owned-risk smoke, and it would have been the repo's first use. But `View()` is a pure
  function of model state: the leadership-change reflection is fully observable by sending
  a `role:master` `refreshMsg`, asserting the peer rows, then sending a `role:slave`
  `refreshMsg` and asserting the flip — all synchronously through the suite's existing
  `runMsg` + `View()` harness. teatest would have added a goroutine and `WaitFor` timing
  (a `-race` flake surface) for zero extra coverage. Deviation recorded here and in the
  migration report; the exit criterion (leadership-change reflection) is asserted either
  way by `TestClusterPane_LeadershipFlipReflected`.

- **The `slaveN:` line has no node ID.** Redis formats replica lines as
  `slaveN:ip=…,port=…,state=…,offset=…,lag=…` — no stable node identifier. The pane uses
  `ip:port` as the peer label and drops the ordinal `N`. `parseSlaveLine` splits the
  comma-separated `k=v` fields and rejects a line with no `ip` field, so a malformed line
  is skipped rather than rendered as a blank row.

- **The cluster pane steals width from the value pane, not the key list.** In the Wide/Mid
  breakpoints the pane is a fixed 30-col third column and the value pane yields the space
  (`width - leftWidth - clusterColWidth`); the key list keeps its width. In Narrow it drops
  to a short strip under the two-pane row, and in Stack it becomes a third stacked section.
  Tiny is untouched. One `clusterColWidth`/`clusterStripH` pair centralises the sizing.

## What's next

M23 — bench + dogfood report + polish + v3.0.0. Finalises
`docs/TOYRAFT-MIGRATION-REPORT.md` (M22's entry: clean, no new ToyRaft findings — the view
consumes M21's already-shaped `INFO` output) and verifies ADRs 0018–0021 before the v3.0.0
tag.
