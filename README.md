# treadle

Go helper binary for [loom](https://github.com/skillweave/loom) — the SkillWeave
universal Claude Code plugin.

Treadle provides the deterministic file operations loom's skills need but
cannot safely perform inline: frontmatter + section-marker parsing,
atomic writes with `tmp-rename` semantics, advisory locking with stale-TTL
reclaim, filesystem-locality detection, template-evolution hash guards,
and JSONL trace logging. Each operation is a single CLI subcommand that
takes explicit input via args / stdin and emits JSON to stdout.

## Status

Pre-v0.1.0 development. Release artifacts are cross-compiled for:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

## Install

End users don't install treadle directly. The loom plugin ships a shim at
`bin/treadle` that fetches the pinned binary from this repo's GitHub
releases on first use, verifies its SHA256 checksum, caches it under
`~/.cache/loom/treadle-v<version>/<os>-<arch>/treadle`, and execs it. See
the loom README for the end-user flow.

For development:

```sh
go build -o treadle ./cmd/treadle
./treadle --help
```

## Subcommands

```
treadle version                        Print version and exit.
treadle parse-project <path>           Parse .loom/project.md; emit JSON.
treadle parse-team <path>              Parse a team template; emit JSON incl. template_hash.
treadle parse-agent <path>             Parse an agent .md; emit JSON frontmatter.
treadle validate-state-key <key>       Exit 0 iff [a-z0-9_-]+, no dots.
treadle compute-state-key <path>       sha256(<path>)[:16] → stdout.
treadle check-fs-locality <path>       Report fs type + locality; emit JSON.
treadle atomic-write <dest>            Read stdin; tmp-rename to dest.
treadle append-findings-log <dir>      Append stdin atomically to findings.log.md.
treadle acquire-lock [--session-id=X] <dir>
treadle release-lock <dir> <session-id>
treadle load-state [--expected-template-hash=H] <dir>
treadle save-meta <dir>                Read stdin JSON; atomically save meta.json.
treadle trace [--json-fields=J] <dir> <session-id> <event-type>
treadle new-session-id                 Fresh time-prefixed session id.
```

## Layout

```
cmd/treadle/main.go         CLI dispatcher (stdlib flag, no cobra).
internal/parser/            ParseProjectMd / ParseTeamMd / ParseAgentMd.
internal/dispatch/          Atomic I/O, locks, fs-locality, trace, state.
internal/migrations/        project.md schema-version migration registry.
```

All deps are standard library. No external Go modules.

## Development

```sh
go test ./...                  # run the full test suite
go test -timeout 30s ./...     # recommended: hard per-test timeout
go vet ./...
go build -o treadle ./cmd/treadle
```

## License

MIT — see `LICENSE`.
