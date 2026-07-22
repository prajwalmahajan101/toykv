# 27 — Writing the manual test guide was another verification pass — it found two more shipped bugs

**Date:** 2026-07-22
**Context:** Direct sequel to entry `26-verify-not-build.md`. M17 shipped `v2.0.0` and, as a
"local testing guide," `docs/TEST_CASES.md` (`a12f558`). Actually *running* that guide against
a live server surfaced two more already-shipped defects. PR #39 (`fix/resp3-reader-hash-order`)
fixed both with the gate tests that were missing.

## Decision / surprise

Entry 26's lesson was "verification is a distinct activity from building, and it has to actually
run." The test-cases doc is the purest example yet: **the act of authoring a manual test script
is a verification pass**, and the moment I ran it by hand two bugs fell out that the entire
automated suite — green across a 6-way OS/Go matrix — never caught.

- 🔴 **The RESP codec was asymmetric.** The writer encoded all seven RESP3 kinds
  (`% ~ , # _ = >`); `ReadFrame` decoded only RESP2. `internal/client` uses that reader, so
  `HELLO 3` under `toykv-cli`/`toykv-tui` died on the first RESP3 reply with
  `resp: unknown prefix '%'`. The server's RESP3 was correct and go-redis (its own reader)
  round-tripped fine in e2e — so **every existing test exercised a reader that wasn't ours.**
  The missing gate was the obvious one in hindsight: an **encode→decode round-trip** over the
  codec's own two halves. It never existed, so the asymmetry was invisible. Fixed by teaching
  `readFrame` the RESP3 kinds (preserving the M17 `MaxArrayLen`/`MaxDepth` DoS bounds) and
  adding the round-trip test as the permanent gate.

- 🔴 **HKEYS/HVALS/HGETALL didn't correspond.** The hash was a bare `map[string][]byte` and
  each reader ranged it independently, so `HKEYS[i]` and `HVALS[i]` came from two separate
  randomized iterations — a silent divergence from Redis's pairing guarantee. Fixed with an
  insertion-order `fieldOrder []string` on the entry, threaded through HSET/HDEL, AOF replay
  (free — records replay in order), and BGREWRITEAOF (a new `SnapshotEntry.HashOrder`).

## Why it mattered

- **Authoring a test guide *is* verification — treat the write-up as the run.** The bugs
  weren't found by a new test file; they were found by a human typing the documented commands
  into a real server. The doc was the harness.
- **A green suite can be green on the wrong surface.** Both RESP3 paths that mattered
  (`internal/client`, `internal/resp` reader) had zero RESP3 coverage; the e2e tests proved the
  *server* emits RESP3, using go-redis's reader, and everyone read that as "RESP3 works."
  Round-trip your own inverse operations — a writer test and a reader test that never meet
  can both pass while the codec is broken.
- **"Decoded" is not "done" — I re-learned entry 26 mid-fix.** After the reader fix the
  transport error was gone, but running the CLI showed `(unknown kind '%')`: the frame decoded,
  yet `internal/respfmt` couldn't render it. The fix wasn't real until the CLI *displayed* the
  map. So the plan grew a 9th change (RESP3 rendering) that only existed because I ran the
  binary instead of trusting the passing decode test. Verification found the gap in the fix
  the same way it found the original bug.

## Blog-worthy?

Yes — it's the sequel beat to 26 and strengthens the thesis. "The milestone I expected to
rubber-stamp is where I found the scariest bugs" (26) becomes "and then *writing the manual to
test it* found two more." The transferable line: **a codec needs a test that composes its two
halves; a writer-test and a reader-test that never meet will both stay green while the thing is
broken.** Plus the mid-fix reminder that decoding isn't displaying — verify the whole path a
user actually walks, not the layer you happened to change.
