# 08 — M5 PR B merged (Rewriter + side-buffer mode)

**Date:** 2026-06-12
**Merge:** PR #14 (`feat/m5-aof-rewriter` → `main`), rebase fast-forward, 2 commits (`b5a9108`, `59ec3ba`).
**Tag:** none yet — M5 tag (`m5`) lands at PR C close.

## Decision / surprise

The interesting bug in this PR was *not* in the swap choreography I'd been bracing for. The rename + dir-fsync + fd-swap dance I'd written about in ADR-0005 worked the first time. What broke instead was the dual-write Append path — a `bytes.Buffer.Len() = 0` after what looked like a successful write.

Root cause: `resp.Writer` wraps the target writer in its own `bufio.Writer`. When you write into a fresh `resp.NewWriter(&scratch)` with a `*bytes.Buffer` target, the bytes sit in the resp.Writer's *internal* bufio until you call `mirror.Flush()`. I'd skipped the Flush because I was used to the existing `Writer.Append` path which doesn't need it — and *that* path doesn't need it because `bufio.NewWriter` is smart enough to short-circuit when its argument is already a `*bufio.Writer`. In Writer.Append the existing chain is `f → outerBufio → respWriter(outerBufio) → callers`, and `bufio.NewWriter(outerBufio)` returns `outerBufio` directly. One buffer, one flush.

When I wrote into `resp.NewWriter(&scratch)` the argument was a `*bytes.Buffer`, not a `*bufio.Writer`, so a fresh inner bufio got created. Two buffers, two flushes needed.

This is the kind of bug where the printf trail tells you exactly what's wrong (sideBuf.Len=0 after w.sideBuf.Write(scratch.Bytes())) but it takes a minute to grok because the function *names* read fine in isolation. The lesson I want to remember: any time I create a fresh `resp.Writer` against a non-bufio target, the next line is a `Flush()`. Add it to the type design later if it bites again.

## Why it mattered

ADR-0005 commits to "the canonical file is durable and consistent at every instant until the rename." That invariant depends on `Append` continuing to write the live bytes to the old file while the rewrite proceeds. A silently-empty mirror would have meant: side buffer empty (no problem), but *also* old-file writes empty (huge problem) because the bug ate them both. The replay test was the green-light proof that both sides of the dual-write are now actually dual-writing.

## Code / measurement

- Files: `internal/aof/rewriter.go` (164 lines new), `internal/aof/writer.go` (+115 lines, side-buffer mode + `Dir()` accessor), `internal/aof/format.go` (+TmpFilename), `docs/adr/0005-bgrewriteaof-dual-write-and-tmp-cleanup.md` (86 lines).
- Tests: 6 new rewriter tests + 1 stale-`.tmp` cleanup test. All pass locally and on Linux+macOS Go 1.25/1.26 in CI.

## Next

PR C: wire `BGREWRITEAOF` into the server dispatch table, add the SIGKILL crash test (three timing windows: pre-rename, post-rename pre-dir-fsync, post-swap during fresh appends), mark M5 ✅ on the roadmap, tag `m5`.
