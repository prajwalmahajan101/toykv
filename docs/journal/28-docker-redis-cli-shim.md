# 28 — The §5 compat sweep was skipping, so I gave it a redis-cli made of Docker

**Date:** 2026-07-22
**Context:** Follow-on to entry 27. An agent ran the whole `docs/TEST_CASES.md` against a live
server and every section passed **except §5 (redis-client compat), which SKIPPED** — no
`redis-cli` on the box. PR #41 (`feat/compat-docker-tooling`) closes that gap.

## Decision / surprise

The e2e byte-compat test (`TestRedisCLI_ByteCompat`) does the honest thing — `exec.LookPath("redis-cli")`,
`t.Skip` if absent — so it stays green on machines without redis-tools. But "green because it
skipped" is exactly the verification blind spot entries 26/27 keep hammering: a skipped test
reads as a pass. §5 was the one section the agent couldn't actually exercise.

Given the choice (native `pacman`/`apt` install vs a Docker wrapper), the pick was **Docker,
no install** — the project already runs `valkey/valkey:8-alpine` for benchmarks, and that
image happens to ship a `redis-cli` compat symlink (→ valkey-cli 8.1.8). The trick that made
it drop-in: the Go test execs a *bare* `redis-cli`, so a `scripts/redis-cli` shim that wraps
`docker run --rm --network host … redis-cli "$@"`, plus `make compat` prepending `scripts/`
to `PATH`, makes the test's own `LookPath` resolve to Docker. **No test change, no native
dependency — the sweep now runs everywhere Docker does.** All 20 cases pass.

## Why it mattered

- **A skip is not a pass — give the test the tooling instead of accepting the gap.** The
  cleanest fix wasn't to weaken the test; it was to satisfy its one external dependency in a
  reproducible way.
- **`LookPath` + `PATH` is a clean injection seam.** Because the test resolves `redis-cli`
  through `PATH` rather than a hardcoded path, a shim earlier on `PATH` transparently
  redirects it to a container. Worth remembering for any "needs an external binary" test.
- **Host networking is what makes the container-as-CLI trick ergonomic.** `--network host`
  means the container's `127.0.0.1:<port>` is the host's, so the shim needs no port mapping
  and behaves byte-for-byte like a native client against a loopback server.

## Blog-worthy?

A small, satisfying one: "my test needed `redis-cli`; I gave it one made of Docker." The
transferable nugget is the `PATH`-shim-over-`LookPath` pattern for un-skipping
external-binary tests without a native install — and the meta-point, again, that a *skipped*
section is an unverified section, not a passing one.
