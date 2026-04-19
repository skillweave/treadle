# Changelog

All notable changes to `treadle` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.3] -- 2026-04-19

### Added

- `spec-review-prep` subcommand that folds the per-call prep work the
  loom `spec-review` skill used to do in shell into one JSON-in/JSON-out
  call: walk-up project-root discovery with git-toplevel fallback, spec
  path canonicalization (refuse escape, refuse symlinks on path),
  `.loom/project.md` parse + context-block assembly, spec content
  read, state_key computation, full dispatch-team args payload
  assembly. Returns everything the SKILL.md needs to call
  `Skill(loom:dispatch-team, args: <blob>)` directly. Recoverable
  errors (`missing_project_md`, `spec_not_found`, `spec_escape`,
  `symlink_on_path`, `parse_error`, `validation_error`) surface as
  `ok:false` rather than non-zero exits so the SKILL.md only needs
  one Bash call to discover them.

### Changed

- Smoke #4 alpha.6 re-run target captured in the loom runbook: the
  alpha.6 rewrite compressed `dispatch-team` to under 10 Bash calls,
  but `spec-review` itself still burned 8 Bash calls before
  dispatch-team fired, missing the overall "<=10 full dispatch /
  <5 pre-first-review" target. `spec-review-prep` is the v0.1.3
  response: expected end-to-end Bash budget after the next loom
  release is ~10 total (2 for spec-review + ~8 for dispatch-team).

## [0.1.2] -- 2026-04-19

### Added

- Four coarse-grained lifecycle subcommands that collapse the per-step
  work the loom `dispatch-team` skill previously had to chain across
  60 to 80 atomic helper calls per dispatch. Each new subcommand reads
  JSON on stdin, returns JSON on stdout, and bundles 5 to 15 atomic
  operations plus the relevant trace events:
  - `dispatch-init` -- args validate, team parse, policy resolve (with
    ceiling enforcement), state_dir compute + mkdir, fs-locality check,
    session-id mint, advisory lock acquire, prior-state load with
    template-hash quarantine, `dispatch-start` trace. Returns the full
    context the SKILL.md needs to drive rounds (session_id, state_dir,
    members, resolved_policy, prior_rounds, etc.). Lock-held / hash
    mismatch / validation errors surface as `ok:false` rather than
    non-zero exits so the caller only needs one Bash call to discover
    them.
  - `round-init` -- atomically emits `round-start` + N `agent-dispatch`
    + N `agent-kickoff` trace events and returns the `team_name` the
    SKILL.md uses for `TeamCreate`, plus deadlines for the 60s-renudge
    / 120s-degrade silence clock.
  - `round-finalize` -- severity-sorts LLM-produced findings, renders
    the per-round synthesis markdown (including the degraded banner
    when relevant), appends the findings-log block atomically, updates
    `meta.json` (append on normal round, replace on rerun), and traces
    `round-end`.
  - `dispatch-end` -- flattens findings across rounds with
    `first_surfaced_round` annotation, renders the final synthesis
    markdown, releases the advisory lock, and traces `dispatch-end`.
- `Finding`, `DegradedMember`, `RoundEntry` as the canonical structured
  shapes passed through round-finalize and stored in meta.json rounds.
- `SortFindings`, `RenderRoundSynthesis`, `RenderFinalSynthesis` as
  exported helpers for caller testing.

### Changed

- Lifecycle subcommand target: under 10 Bash calls per clean 2-round
  dispatch (previously 60 to 80 in alpha.5). This is the per-dispatch
  budget the loom `dispatch-team` SKILL.md now enforces.

## [0.1.1] — 2026-04-19

### Fixed

- CLI flag parsing now accepts `--flag=value` in any position relative
  to positional args. Previously, Go stdlib `flag` stopped parsing at
  the first positional, silently dropping any later flags. `trace`,
  `acquire-lock`, and `load-state` were affected — flags placed after
  positionals were ignored without error. Added `reorderFlags` helper
  and a table-driven test; the loom smoke-test surfaced this bug when
  `treadle trace <dir> <sid> <event> --json-fields=<j>` dropped the
  JSON fields.

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
