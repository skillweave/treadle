# treadle

Go helper binary for [loom](https://github.com/skillweave/loom) — the SkillWeave universal Claude Code plugin.

Provides deterministic file parsing, atomic I/O, advisory locking, schema migration, and
filesystem-locality detection that loom's skills invoke via the Bash tool.

## Status

Pre-v0.1.0 — minimal scaffolding for Phase 0 prereq validation. Full CLI ships with
loom v0.1.0.

## Install

Users don't install treadle directly. The loom plugin ships a shim in its `bin/` that
fetches the appropriate binary from this repo's GitHub releases on first use.

## License

MIT
