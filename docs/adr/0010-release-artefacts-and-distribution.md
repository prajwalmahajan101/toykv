# ADR-0010: v1 release artefacts — Goreleaser, three binaries, SHA256SUMS, no third-party channels

- Status: Accepted
- Date: 2026-06-17
- Milestone: M9
- PR: `feat/release-v1`

## Context

M9 cuts `v1.0.0` and ships it as a tagged GitHub Release. Until now every binary the project produced was either built locally with `make build` or rebuilt by every CI job from source. The shipped surface for end users was effectively "clone the repo and run `make build`", which only the M0–M8 contributor cohort could be expected to do.

The roadmap (`docs/ROADMAP.md:108-113`) and `docs/RELEASE_PLAN.md:46-50` both pre-commit to **goreleaser, GitHub Releases only, four platforms, three binaries per archive, SHA256SUMS alongside**. That's the policy. This ADR records the *why*, since each "no" closes off a real alternative the project may want to revisit at v2.

Three calls had real alternatives worth recording:

1. **Build tool.** Goreleaser vs. a hand-rolled `make release` matrix vs. GitHub Actions `matrix:` doing the cross-build directly.
2. **Distribution channels.** GitHub Releases only vs. Homebrew tap (`prajwalmahajan101/tap`) vs. Arch AUR / apt PPA vs. Docker image.
3. **Archive shape.** One archive per binary per platform (12 archives total) vs. one archive per platform carrying all three binaries (4 archives total).

The v1 spec is emphatic about scope creep — "that's how you end up half-building Redis instead of finishing tinykv." This ADR is the second-order version of that discipline applied to release plumbing.

## Decision

**Goreleaser drives the release. Four platforms (`{darwin,linux} × {amd64,arm64}`). One archive per platform carrying all three binaries (`toykv`, `toykv-cli`, `toykv-tui`) plus a small docs subset. A single `SHA256SUMS` file alongside the archives. GitHub Releases is the only distribution channel for v1.**

Concretely:

- `.goreleaser.yaml` configures three `builds:` entries — one per binary — each with `goos: [linux, darwin]`, `goarch: [amd64, arm64]`, `CGO_ENABLED=0`, `-trimpath`, and `-s -w -X main.version=... -X main.commit=... -X main.date=...` ldflags. Static binaries; no glibc surprise.
- Archive name template: `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}.tar.gz`. Each archive contains `toykv`, `toykv-cli`, `toykv-tui`, `README.md`, `LICENSE`, `CHANGELOG.md`, `docs/PRD.md`, `docs/ROADMAP.md`, `docs/SECURITY.md`. A user extracting the archive has the binaries *and* the security caveats in the same directory — `SECURITY.md` should not require a separate fetch.
- Checksum file: `SHA256SUMS`, SHA-256, written next to the archives. Format matches `sha256sum` so verification is `sha256sum -c SHA256SUMS`.
- Release is published on every `v*` tag push by `.github/workflows/release.yml` using `goreleaser/goreleaser-action@v6`. CI (`ci.yml`) is the gate before tagging; `release.yml` only runs post-tag and is independent.
- Snapshot builds (`goreleaser release --snapshot --clean`) reproduce the same archive shape locally with a synthetic version (`0.0.1-SNAPSHOT-<short>`). Every PR can verify the release pipeline without pushing a tag.
- Release-notes highlights live in `docs/release-notes/v1.0.0.md` (hand-curated). Goreleaser's auto-changelog (`use: github`, filtered) lands as the body; the highlights are pasted manually after the workflow finishes. `RELEASE_PLAN.md:63` explicitly calls for hand-curated highlights.

## Consequences

**Positive:**

- A user with curl and tar can install `v1.0.0` in one line — the bar the README's "Install" section assumes.
- The build matrix lives in source-controlled config (`.goreleaser.yaml`), not in CI YAML. Switching CI providers later is one file move.
- Snapshot mode lets PR reviewers verify the release pipeline locally before any tag is pushed. The "manual smoke" exit criterion in `docs/RELEASE_PLAN.md:27-31` can be done against a snapshot archive.
- Three binaries in one archive matches how users think about toykv: "the project," not "the server, the CLI, and the TUI as separate products." First-time TUI users don't have to discover that a second download exists.
- `SHA256SUMS` opens the door to signing in v1.x without breaking the layout — the file just gets a `.asc` neighbour.

**Negative:**

- Pinning to goreleaser ties the release path to one tool's lifecycle. Mitigation: snapshot mode reproduces archives locally without GitHub, so a one-off cut without goreleaser is `tar -czf` away — at the cost of the changelog and checksums automation.
- No Homebrew tap means macOS users do not get `brew install toykv`. Documented in `docs/RELEASE_PLAN.md:50` as a v1 cut; revisitable in v2 once AUTH / TLS land (per the v2.0 cut criteria in `docs/ROADMAP.md:140`).
- No Docker image. Users who want a containerised toykv build their own `FROM scratch` from the static binary. Acceptable — toykv is a single-node KV; the Docker story is more interesting once `tinyraft` lands and clusters appear.
- Four platforms drops Windows. Stdlib-only server should build for `GOOS=windows` without changes, but the TUI's terminal handling is unverified there. Marked v2 work.

**Neutral:**

- Archive size: ~9 MB per platform (three static Go binaries + a handful of MD files). Below GitHub's per-release size budget by three orders of magnitude.
- Tag format: SemVer, lowercase `v` prefix (`v1.0.0`). Matches `docs/RELEASE_PLAN.md:6-10` and what `goreleaser` expects.

## Alternatives considered

**Hand-rolled `make release`.** Reject. The cross-build matrix is exactly the code goreleaser eats; reinventing it gives us 3 binaries × 4 platforms × { `go build -ldflags=-X` + `tar -czf` + `sha256sum >>` } in a Makefile. A future format change (signed checksums, SBOM, Cosign) becomes a YAML diff under goreleaser; under hand-rolled Make it becomes a Makefile rewrite each time.

**GitHub Actions `matrix:` doing the cross-build inline.** Reject. Moves the build matrix from source control (`.goreleaser.yaml`) into CI YAML. The release pipeline becomes unreproducible locally — a regression specifically caused by M8's "build the binary in `TestMain`" lesson, where reproducibility paid for itself within one milestone.

**Homebrew tap.** Defer to v2. Brew tap requires maintaining a separate repo and a manual `brew update` cadence. The cut criterion in `docs/ROADMAP.md:140` — "AUTH + lists + hashes + INFO + SCAN shipped" — is a reasonable bar before bringing on a packaging channel that implies "production usable."

**Docker image.** Defer. A `FROM scratch` Dockerfile would ship a 9 MB image, which is fine, but a containerised toykv is uninteresting until the wire protocol grows AUTH (otherwise the natural deployment is "expose port 6390 to the network" which directly contradicts SECURITY.md). The user can write the three-line Dockerfile themselves; v1 will not ship it.

**Twelve archives (one per binary per platform).** Reject. Splits the project's public surface artificially. Confuses first-time users into thinking they need to choose. The all-in-one archive is also closer to how comparable Go tools ship (e.g. `goreleaser` itself; `gh`).

## References

- `docs/ROADMAP.md:108-113` — M9 scope (Goreleaser; darwin/linux × amd64/arm64; three binaries; tag `v1.0.0`).
- `docs/RELEASE_PLAN.md:22-50` — exit criteria; SemVer; distribution promise; no homebrew/apt for v1.
- `.goreleaser.yaml` — the actual config.
- `.github/workflows/release.yml` — tag-driven publish workflow.
- Related ADRs: [[0008]] (CLI stdlib + mode detection; sister "no third-party deps in shipped binaries" line) and [[0009]] (TUI's accepted third-party dep tree — the only one that survives into shipped artefacts).
