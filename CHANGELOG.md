# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Pre-v1.0.0 milestones are tagged `v0.M.0` after each milestone PR merges
on `main` (see [`docs/ROADMAP.md`](./docs/ROADMAP.md)).

## [Unreleased]

### Added

### Changed

### Fixed

## [v0.0.0] — 2026-06-11

### Added

- Repo scaffold: Go module `github.com/prajwalmahajan101/toykv`, Go 1.26.
- Package skeletons under `internal/{resp,store,aof,server,client,cli,tui}`.
- Three binary entrypoints: `cmd/toykv`, `cmd/toykv-cli`, `cmd/toykv-tui`
  (all M0 placeholders printing `--help`).
- `Makefile` with `build`, `run`, `cli`, `tui`, `fmt`, `vet`, `lint`,
  `test`, `bench`, `ci`, `hooks`, `clean` targets.
- `.golangci.yml` (10 linters; security + correctness blocking; style
  enforced in CI).
- `.githooks/pre-commit` (gofmt + go vet on staged files; install via
  `make hooks`).
- GitHub Actions CI: lint + test matrix (ubuntu+macos × Go 1.25/1.26) +
  build.
- Documentation set under `docs/`: PRD, ROADMAP, HLD, LLD, TESTING,
  BENCHMARKS, RELEASE_PLAN, SECURITY, CONTRIBUTING, ADR index.
- MIT licence.

[Unreleased]: https://github.com/prajwalmahajan101/toykv/compare/v0.0.0...HEAD
[v0.0.0]: https://github.com/prajwalmahajan101/toykv/releases/tag/v0.0.0
