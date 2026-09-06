# ToyRaft Migration & Dogfooding Report

**What this is.** toykv v3.0 embeds [ToyRaft](https://github.com/prajwalmahajan101/toyraft)
(`v1.0.0-rc.1`) as its consensus library. ToyRaft's own roadmap gates its `v1.0.0`
tag on a real consumer embedding it — toykv's running cluster is that consumer.
This report is the reciprocal half of that mutual unblock: the findings toykv
surfaces against the frozen public API — confirmed bugs (with repros), API
friction, docs gaps, feature requests, and confirmed-working behaviour — recorded
**incrementally as each milestone integrates**, not batched at the end.

**How to read it.** One section per milestone (M18–M22). Each finding is tagged:

- 🐞 **Bug** — behaviour contradicts the documented contract (repro included).
- 🧭 **API friction** — the contract holds but the shape forced a workaround.
- 📄 **Docs gap** — correct behaviour that was undocumented or hard to discover.
- 💡 **Feature request** — a v2 capability that would remove a current workaround.
- ✅ **Confirmed** — a documented guarantee that held under real integration.

Severity is the *consumer* impact: **high** = blocked/worked-around a core path,
**medium** = friction with a clean workaround, **low** = cosmetic/discoverability.

Findings are delivered to ToyRaft as `v1.0.0` dogfood-gate feedback; the
dependency bumps `rc.1 → v1.0.0` at M23 once ToyRaft tags off this integration.

---

## M18 — Raft embedding + single-node replicated path

**Scope integrated.** `pkg/raft` (`Node`, `Config`, `StateMachine`, `Entry`,
error sentinels), `pkg/storage/memory`, and the `raft.Transport` interface, wired
into toykv's single dispatch chokepoint. Single-node cluster (`Peers=[self]`),
in-memory Raft log, AOF as the durability source. Against **`v1.0.0-rc.1`**,
toykv commit range `feat/raft-embed`.

### 🧭 API friction — `Node.Propose` discards `StateMachine.Apply`'s result *(high)*

`Node.Propose(ctx, data) (Index, Term, error)` blocks until the entry is applied
(SC3) and propagates `Apply`'s **error**, but the non-error `result any` returned
by `StateMachine.Apply` is **dropped at the Node boundary** — the caller only
gets back `(Index, Term, error)`.

For a KV whose replies are *computed inside* Apply (INCR's new value, LPUSH's
length, DEL's removed-count), this is load-bearing: the proposing client needs
that exact value, and it cannot be reconstructed by a follow-up read (the value
is the outcome of the mutation, not the current state). The reference
`cmd/toyraftd` sidesteps this because it only needs GET-after-SET via a
leader-local read — which does not generalise.

**Workaround (shipped).** toykv's `StateMachine.Apply` stashes each reply in a
`map[raft.Index]resp.Value`, and its `Node.Propose` wrapper collects it by the
index `Propose` returned. It is race-free *only* because `Propose` returns after
Apply has run (SC3), so the entry is guaranteed present. See toykv
`internal/cluster/{statemachine,node}.go` and ADR-0018.

**Repro (conceptual).**
```go
sm := myStateMachine{} // Apply returns the command's reply as `result any`
idx, _, err := node.Propose(ctx, encode(cmd)) // err is delivered; result is not
// There is no node.Result(idx) / no result on the Propose return — the value is gone.
```

**Requests for v1/v2.**
- 💡 Return the applied result from `Propose`: `Propose(ctx, data) (Index, Term, any, error)`, **or**
- 💡 expose an accessor keyed by index (e.g. `Applied(idx) (any, bool)`), **or**
- 📄 at minimum, document explicitly in the `StateMachine.Apply` godoc that the
  `result` is delivered to the proposer's `Propose` return — the current
  `transport.go` comment ("delivered back to the proposing client if the
  proposal was local") *implies* delivery that the `Node.Propose` signature does
  not actually provide. This mismatch cost real investigation time.

### 🧭 API friction — `pkg/transport/inproc` is not constructible by an external consumer *(medium)*

`inproc.NewHub(HubConfig)` **requires** a non-nil `HubConfig.Clock` (returns
`"inproc: HubConfig.Clock required"` otherwise), and `Clock` is typed
`internal/clock.Clock`. An external module cannot import
`github.com/prajwalmahajan101/toyraft/internal/clock`, and there is no public
constructor for a `clock.Clock`, so **the inproc transport cannot be built
outside the toyraft module** — it is effectively test-only.

**Impact.** The natural choice for a single-node embed (an in-process hub) is
unavailable. Note the asymmetry: `raft.Config.Clock` is *also* `internal/clock`
typed but **optional** (nil → real clock via `applyDefaults`), so it's a
non-issue for `raft.Config`; `HubConfig.Clock` being **required** is the trap.

**Workaround (shipped).** A single-node cluster fans out no messages, so toykv
implements a ~5-line no-op `raft.Transport` and skips inproc entirely. Correct
here, but it means the shipped inproc transport provides no value to an external
single-node (or in-process multi-node test) consumer.

**Requests for v1/v2.**
- 💡 Expose a public clock constructor (e.g. a `pkg/clock` with `NewReal()` /
  `NewFake()`), **or**
- 💡 let `HubConfig.Clock == nil` default to a real clock, mirroring
  `raft.Config`'s own `applyDefaults` behaviour — the inconsistency between the
  two configs is itself surprising.

### 📄 Docs gap — the intended single-node embedding pattern is undocumented *(low)*

Confirming that a single-node cluster needs **no** real transport required
reading `candidate.go` (the ELEC-04 "single-node fast path: the self-vote may
already be a quorum" comment and the self-excluding vote fan-out). A short
"embedding a single node" note — "`Peers=[self]`, a no-op transport is
sufficient, self is leader within one election timeout" — would have saved that
source dive. This is the smallest, most common first embed; it deserves a
paragraph in the package docs or README.

### ✅ Confirmed — guarantees that held under integration

- **SC3 (Propose blocks until applied).** Relied on directly for the reply-by-index
  workaround above; held in every test including the crash-injection suite.
- **ELEC-04 single-node fast path.** `Peers=[self]` elects self leader within one
  election timeout with no transport traffic; `WaitLeader` polling `Status().Role`
  observed `Leader` reliably (~150–300ms real clock).
- **Deterministic, single-goroutine, in-order Apply (API-05).** toykv's determinism
  test (same envelope stream applied twice ⇒ identical `store.Snapshot()`) passed;
  no internal synchronisation needed in `Apply`.
- **`memory.New()` + the frozen `Config`/`StateMachine`/`Entry` API.** Integrated
  with no surprises; `Config` zero-value defaults (`applyDefaults`) behaved as
  documented; `ErrSnapshotUnsupported` is a clean v1 sentinel to return from
  `Snapshot`/`Restore`.
- **Clean `Stop()` drain.** Stopping the node before closing the AOF let in-flight
  applies complete; no lost or torn writes across shutdown.

### Net for M18

No 🐞 correctness bugs — the consensus core behaved exactly as specified. The two
🧭 frictions are both **API-shape** issues (result delivery on `Propose`; inproc
Clock visibility) rather than defects, each with a shipped workaround, and each
with a concrete v1/v2 suggestion above. This is encouraging for the `v1.0.0`
gate: the hard part (consensus correctness, apply-once, determinism, durability
through the new path) is solid; the friction is at the embedding ergonomics
layer, which is the cheapest kind to fix.

---

## M19 — Multi-node replication + leader election

_Complete. M19.1 (cluster wiring + happy path) integrated `pkg/transport/http` +
`pkg/storage/file` against a real in-process 3-node cluster; M19.2 (failover +
partition correctness) injected network faults via a chaos-wrapped transport; M19.3
(linearizability) checked concurrent histories with Porcupine at N = 3/5. Findings
below, closed by [Net for M19](#net-for-m19)._

### 🐞 `pkg/transport/http` was not constructible by an external module — **high**

**Contract.** The LLD marks `pkg/transport/http` as a **Stable** public package —
a consumer is expected to `http.New(http.Config{…})` to get a `raft.Transport`.

**What we hit.** `http.Config.Clock` is typed `internal/clock.Clock`, and
`Config.Validate()` **hard-rejected a nil Clock** (`config.go:67`). An external
module cannot satisfy it: there is no public constructor for a clock, and the
interface can't be implemented from outside because its methods return the
un-nameable `internal/clock.Timer`/`Ticker`. So the one transport a real cluster
needs could not be built by a real consumer — the exact thing the `v1.0.0`
dogfood gate exists to catch. (M18 foreshadowed this for `pkg/transport/inproc`;
M19 confirmed it on the load-bearing path.)

**Repro (from toykv, at rc.1).**
```go
_, err := http.New(http.Config{
    NodeID: "n1", ListenAddr: ":7001",
    PeerURLs: map[raft.NodeID]string{"n2": "http://127.0.0.1:7002"},
    // Clock: ??? — cannot construct internal/clock.Clock from outside the module
})
// err: "http.Config: Clock must be non-nil (use internal/clock.Real)"
```

**Fix delivered (ToyRaft `v1.0.0-rc.2`).** `http.New` now defaults a nil `Clock`
to `clock.NewReal()` before `Validate()` — parity with what `raft.Config`
already did (`config.go:134`). Non-breaking (nil newly *permitted*, type
unchanged), landed with ADR-0023 + a nil-clock construction test. toykv builds
against `rc.2` and leaves `Clock` unset. **This is the first concrete `rc.1 →
rc.2` outcome of the mutual unblock.**

**Suggestion for `v1.0.0`.** Consider the same nil-default on
`inproc.HubConfig.Clock` to close the class entirely, and/or extract the narrow
public `pkg/raft.Clock` the `internal/clock` doc already anticipates (phase-5) so
external consumers can inject a fake clock for their own deterministic tests.

### ✅ `pkg/storage/file` — constructs and replays cleanly — **confirmed**

`file.New(dir)` is a plain public constructor; no internal-type barrier. Wired as
each node's Raft-log store with a per-node `-raft-dir`; the 3-node happy-path test
runs green under `-race` with no storage-layer friction.

### ✅ HTTP transport (post-rc.2) — election + replication work first try — **confirmed**

Once constructible, `pkg/transport/http` needed no further coaxing: three
in-process nodes on localhost ports elect a leader and replicate a mutating
command stream to all followers, with `Status().Role`/`LeaderHint()` and
`ErrNotLeader` (on a follower `Propose`) behaving exactly as documented. No 🐞 in
the consensus/transport path — only the constructibility blocker above.

### 🧭 `pkg/transport/inproc` still not externally constructible at rc.2 — blocked failover-test plan *(medium)*

_M19.2 (failover + partition correctness)._

The M18/M19 suggestion (nil-default `inproc.HubConfig.Clock`, mirroring the
`http.New` rc.2 fix) was **not** applied to `inproc` — `NewHub` still hard-rejects a
nil `Clock`, and `HubConfig.Clock` is still typed on the un-importable
`internal/clock`. M19.2 planned to reuse the Hub's `Partition`/`Heal` chaos knobs
(the exact surface ToyRaft's own `figure8_test.go` uses) to prove failover and
partition correctness — but a consumer still cannot build a Hub, so that plan was
blocked at the same wall as M19.1's http transport, one release later.

**Workaround (shipped).** toykv wrote a ~40-line `chaosTransport` that *wraps the
real `pkg/transport/http`* and drops messages crossing a partition cut (outbound in
`Send`, inbound in the registered `step`), toggled by a shared `partitionState`.
This tests partitions over the **production** transport — arguably better than
inproc — at the cost of the seeded delivery determinism inproc would have given.
See toykv `internal/cluster/failover_test.go`.

**Confirmed correctness under real partitions.** With chaos injection, ToyRaft held
every failover guarantee: leader-kill loses no acked write (the REPL-06 current-term
commit rule flushes the acked prior-term prefix once the new leader commits in its
term — exactly as `commit.go` documents); a partitioned minority leader cannot
advance `CommitIndex`; on heal the minority's uncommitted tail is discarded and the
cluster reconciles to the majority history; and no two leaders ever share a term
(the isolated stale leader keeps `Leader` at its old term, never the new one).

**Request for `v1.0.0` (restated, now higher priority).** Nil-default
`inproc.HubConfig.Clock` and/or expose the narrow public `pkg/raft.Clock` so
consumers can drive deterministic failure tests with the shipped chaos surface
instead of re-implementing one. The chaos knobs (`Partition`/`Heal`/`DropRate`/
`Delay`/`Reorder`) are genuinely valuable to a consumer — they are just unreachable.

### ✅ Replicated register is linearizable under concurrent load — **confirmed**

_M19.3 (linearizability harness)._

A Porcupine harness drove 4 concurrent clients issuing `SET`/`GET`/`INCR` against a
running in-process cluster (real HTTP transport; writes via `Node.Propose`, reads
from the leader's applied state) and checked the recorded call/return history
against a single-integer-register model. The history is **linearizable under `-race`
at N = 3 and N = 5** — the strongest correctness statement in the M19 series. The
harness needs **nothing from ToyRaft beyond the existing `Propose`/`Status`
surface**; no new friction. Combined with M19.2's failover proof, this closes the
distributed-core dogfooding: ToyRaft's consensus behaves correctly under
concurrency, failover, and partition when embedded by a real consumer.

### Net for M19

The distributed core is done, and the split between where ToyRaft *shone* and where it
*chafed* is now unambiguous. **The consensus core was flawless** under the hardest tests
toykv can throw at it: leader election and replication first-try (M19.1); no acked-write
loss across leader kill, correct divergent-tail discard on partition-heal, and a clean
per-term split-brain guard (M19.2); and a linearizable register under concurrent load at
N = 3 and N = 5 (M19.3) — all under `-race`. Two behaviours that first looked like bugs
were ToyRaft being *correct*: the REPL-06 current-term commit rule (a new leader flushes
the acked prior-term prefix only once it commits in its own term) and an isolated leader
retaining `Leader` at its stale term — both documented invariants the tests had to be
written *around*, not against.

**All friction was at the embedding-ergonomics layer, and it clustered on one root cause:**
the `internal/clock`-typed, required, externally-un-constructible `Clock`. It surfaced three
times on an escalating path — `inproc.Hub` (M18, worked around), `pkg/transport/http` (M19.1,
*fixed* in rc.2), and `inproc.Hub` again (M19.2, still unfixed, forcing the `chaosTransport`
workaround over the real HTTP transport). **The single highest-value `v1.0.0` action from
this integration:** generalize the rc.2 nil-Clock default to `inproc.HubConfig.Clock`, and/or
expose the narrow public `pkg/raft.Clock` the `internal/clock` docs already anticipate — this
closes the entire class and unlocks the shipped chaos surface (`Partition`/`Heal`/`DropRate`/
`Delay`/`Reorder`) for every external consumer. No 🐞 correctness defects across the whole
M19 arc.

## M20 — Client routing: write redirect + read model

_Pending._

## M21 — WAIT + INFO replication + cluster observability

_Pending._

## M22 — TUI v3: cluster view

_Pending._
