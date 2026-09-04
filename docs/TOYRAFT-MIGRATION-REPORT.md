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

_To be appended when M19 integrates the HTTP transport, file storage, and
election against a real 3-node cluster._

## M20 — Client routing: write redirect + read model

_Pending._

## M21 — WAIT + INFO replication + cluster observability

_Pending._

## M22 — TUI v3: cluster view

_Pending._
