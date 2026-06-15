# 02 — AOF persistence + crash injection (M3)

**Date:** 2026-06-11

## What landed

- `internal/aof/` — `Writer`, `Replay`, `FsyncPolicy{Always|Everysec|Never}`, `ReplayError{Offset,Err}`, `format`/`errors`/`writer`/`replayer` files.
- `internal/server/` — `Config.Dir` + `Config.FsyncPolicy`, replay on `New`, append-on-success on every mutating handler, `Close()` drain.
- `cmd/toykv/` — `-dir`, `-appendfsync` flags wired in.
- The milestone-owned **crash injection test** under `internal/server/aof_crash_test.go` — self-re-exec child server, SIGKILL mid-stream, restart, verify every acked SET survived under `appendfsync=always`.
- **ADR-0003** committed alongside the code.

## Things worth quoting later

### 1. "Write the bytes you'd send"

The whole AOF format is "RESP arrays on disk." `Append` calls the existing `resp.Writer` against a buffered file handle; `Replay` calls the existing `resp.Reader`. One codec, two consumers. The only thing the AOF package owns is the 8-byte header (`TOYKV\x00\x00` + version) and the fsync policy.

This is the single decision that makes M3 small. The journal-worthy framing: "the on-disk format is the wire format" — and that means every future command added to RESP is automatically persistable with zero AOF code changes. M4 will add TTL and bump the version byte, but the *records* will still be RESP arrays — `SET k v PX 5000` is just a bigger array.

### 2. The replay path reuses live dispatch — for free

`replayApply(argv)` calls `s.dispatch(argv)` and checks the returned RESP value for an error frame. Same handler table, no second code path. The trick is that during replay `s.aof == nil` (we open the writer *after* replay returns), so the post-success `appendIfLive` call inside each mutating handler is a silent no-op.

The implication: when we add commands in M4/M5, the AOF "just works" if the command goes through the standard handler pattern. No second table to keep in sync. **This is the kind of design that pays you back when you forget about it for a month.**

Worth a sentence in the blog: "the same nine lines of Go handle both real traffic and crash recovery."

### 3. Append-on-success, canonical form

`SET k v NX` against an existing key returns `(nil)` and writes **nothing** to the AOF. `SET k v` against any state writes a record. When the conditional *does* succeed, we deliberately append the **canonical 3-arg form** (`SET k v` — strip the NX/XX token) so replay against an empty store can apply it unconditionally. Without this, a successful `SET k v NX` would be re-evaluated as NX during replay; if the AOF earlier contained a `SET k <other>` for the same key, the NX would now fail, and replay would diverge from live state.

The empty-store-NX-replay invariant cost two lines of code (`[]byte("SET"), argv[1], argv[2]`) and saved an entire class of replay bugs. Found by thinking about the corner — not by a test.

### 4. The everysec ticker

`FsyncEverysec` spawns a goroutine on `Open` that runs `time.NewTicker(1*time.Second)`. Each tick takes the writer's mutex and, if `dirty` is true, calls `f.Sync()` and clears the flag. Append sets `dirty=true` under the same mutex. `Close` signals `stopCh`, waits for `doneCh`, then does a final flush+fsync.

The test for it doesn't try to fake the clock — it sleeps with a 3-second tolerance and observes the `dirty` flag. The everysec contract is "at most ~1s of acked writes lost on crash"; the test asserts the looser bound exactly. Tolerance-based timing tests have a reputation for flake but the right thing here is *not* to mock — we want the real ticker to actually fire.

### 5. The crash test, end to end

The risk test (`TestAOF_CrashInjection_Always`) is a self-re-exec subprocess test:

- `TestMain` looks at `TOYKV_AOF_CHILD`. If set, it runs `runChildServer()` — a toykv server bound to a port from env, with AOF in a tempdir, `fsync=always`.
- Parent forks the same test binary with `-test.run=TestAOF_CrashInjection_Always` and the env var set.
- Parent dials the child, writes ~90 SETs, SIGKILLs the child mid-stream, then opens a fresh in-process server against the same tempdir and verifies every SET it saw a `+OK` for is present.

What this actually proves: the fsync-before-reply ordering is *not violated*. Every byte the parent received as `+OK` corresponded to a record that had been written *and* fsynced. The proof is operational: if even one was missed, the test fails.

I ran it `-count=10` after the first pass. No flakes. The single bug found in development was the child printing `s.Addr()` *before* `s.Run` bound the listener (returned empty string); the fix was to spin up Run in a goroutine and poll Addr until non-empty, then signal ready over stdout. Real-world echo of "the readiness signal has to come *after* the side effect, not before" — same pattern as kubelet probes, same trap.

### 6. End-to-end smoke

Live binary, `appendfsync=always`:

```
./bin/toykv -addr :16390 -dir /tmp/toykv-m3 -appendfsync always &
# SET k hello / INCR ctr / INCR ctr via raw RESP over nc
kill -9 $!

./bin/toykv -addr :16390 -dir /tmp/toykv-m3 -appendfsync always &
# GET k → $5\r\nhello\r\n
# GET ctr → $1\r\n2\r\n
# DBSIZE → :2
```

Startup log: `aof ready dir=/tmp/toykv-m3 fsync=always replay_records=3 replay_bytes=85 replay_duration=427µs`. Three records replayed in under half a millisecond on this machine. That number's a baseline I'll keep an eye on through M4/M5.

### 7. Things to journal next

- **Latency numbers** under `appendfsync=always` vs `=everysec` once we have `redis-benchmark` wired in M9.
- The TTL replay bump in M4 — first real test of the version byte. Capture how big the diff actually is (prediction: small).
- If anyone reports an `aof: replay failed at offset N` in the wild, what was the file state and how did they recover. v1 says "operator opt-in to truncate"; let's see how that plays.

## What I deliberately did not do

- **No auto-truncate.** Fail fast with the offset, let the operator decide. We have no operators yet; this is the moment to be strict.
- **No retry on `aof.Append` errors.** On failure the handler returns `-ERR aof append failed` and logs. LLD §5.4 says "drop conn + log + process exit"; I picked "drop conn + log" for v1 — `os.Exit` from inside a handler is awkward for tests, and the contract is preserved (the client never saw `+OK` for a failed append, so disk is the source of truth on restart). Add a graceful-shutdown channel in M3.1 if real ops feedback demands it.
- **No -aof-truncate flag** — out of scope for v1 per the LLD.
- **No record-level checksums.** RESP framing already catches the common corruption modes (truncated bulk strings, mismatched array lengths). A CRC per record would be belt-and-braces for a learning artefact. Defer to "if a real bug ever slips past RESP-level parsing."
