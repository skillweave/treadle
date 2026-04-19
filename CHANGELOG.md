# Changelog

All notable changes to `treadle` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] — 2026-04-19

### Added

- CLI dispatcher at `cmd/treadle/main.go` with 15 subcommands (parse-project,
  parse-team, parse-agent, validate-state-key, compute-state-key,
  check-fs-locality, atomic-write, append-findings-log, acquire-lock,
  release-lock, load-state, save-meta, trace, new-session-id, version).
- `internal/parser` — section-marker + frontmatter parser with 2 KB
  context-block cap and truncate-longest-section-first behavior.
  Computes 16-char `template_hash` for the template-evolution guard.
- `internal/dispatch` — tmp-rename atomic writes, advisory-lock
  acquire/release with 1h stale-TTL reclaim and session-id ownership
  check, `/proc/self/mountinfo`-based filesystem-locality detection on
  Linux, conservative pass-through on other OSes, JSONL trace writer,
  time-prefixed `NewSessionID` for chronological-sort observability.
- `internal/migrations` — empty registry + `CheckSupported` +
  `ApplyMigrations`; first real migration lands when project.md schema
  bumps past v1.
- GitHub Actions CI: `go test ./...` + `go vet` on every PR.
- Cross-compiled release artifacts: linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64, each with a `checksums.sha256`.
- Signed release tags via `git tag -s`.

## [0.0.1-prereq]

Minimal scratch scaffold used to validate Phase 0 Prereq 6 (loom plugin
shim can fetch + verify + exec treadle from a release artifact). Not a
user-facing release.
