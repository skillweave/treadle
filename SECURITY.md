# Security policy

## Reporting a vulnerability

Email `a@c4.io` with subject line starting `treadle security:`. Include:

- Affected version(s) (the binary's `treadle version` output).
- Reproduction steps — commands, inputs, expected vs. actual behavior.
- Impact — what gets read, written, executed, or escaped outside the
  caller's intended scope (path-traversal, lock bypass, atomic-rename
  corruption, trace-log injection, etc.).

Acknowledgement within 72 hours; triage within seven days. Please do not
open a public GitHub issue for security-sensitive reports.

## Supported versions

Only the latest tag receives security fixes. The loom plugin pins a
specific treadle version via `bin/VERSION`; upgrading that pin is the
user-facing upgrade path.

| Version | Supported |
|---|---|
| Latest tag | Yes |
| Anything older | No |

## Signed releases

Every release tag is GPG-signed (`git tag -s`). The maintainer's public
key is committed at `docs/ops/maintainer-pubkeys.asc`; the release
workflow imports it before running `git tag -v` so CI actually verifies
the signature.

Release artifacts (per-platform tarballs) are accompanied by a
`checksums.sha256` file. The loom shim verifies the downloaded tarball's
checksum against this file before extracting + executing.

## Trust model

Treadle runs inside the caller's shell — invoked from loom skills' Bash
tool calls. It does not fetch anything itself, does not parse untrusted
network data, and writes only to paths the caller explicitly provides.

Inputs treadle treats as trusted:

- Paths passed as CLI args or stdin.
- Frontmatter / body contents of `.md` files it parses.

Inputs treadle defends against:

- Path-traversal via `state_key` (rejects anything outside `[a-z0-9_-]+`).
- Lock squatting (session-id ownership check on release).
- Template-drift state corruption (quarantine on hash mismatch).
- Duplicate sections / malformed frontmatter (explicit parse errors).
