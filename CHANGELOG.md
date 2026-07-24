# Changelog

Notable changes, in English, for people who use dsx rather than work on it. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Two records exist and they are not the same thing. This file is curated and covers what
changed for a *caller* — commands, flags, exit codes, on-disk formats. The release notes
GitHub publishes are generated from git history, which is complete, in Russian, and includes
every internal change; see [CONTRIBUTING.md](CONTRIBUTING.md#commit-messages) for why the
history is in Russian.

dsx speaks an undocumented endpoint. A server-side change can break a release that was correct
when it was cut, and that will not be visible here in advance.

## [Unreleased]

### Added

- Homebrew tap: `brew install somework/tap/dsx` on macOS, with `brew upgrade dsx` and shell
  completions after it. Casks are macOS-only, so Linux keeps the archive. Homebrew checks the
  archive against a SHA-256 in the cask, which says nothing about who built it — `gh attestation
  verify` still does, on the archive route.

### Changed

- The archive install in README reads the version off `releases/latest` instead of asking you to
  paste one in.

## [0.1.0] — 2026-07-25

First release, and the first one with something to download: prebuilt binaries for macOS and
Linux on `amd64` and `arm64`, each archive carrying build provenance signed by GitHub. Before
this the only way in was `go install`, which is still there and still the answer for anyone who
would rather compile what they can read.

Being 0.x is the honest statement about the surface below, not modesty. dsx speaks an
undocumented endpoint, so a release that was correct when it was cut can break without a commit
touching it.

### Commands

Sync verbs `clone`, `pull`, `push`, `status`, `fetch`, `diff`, `pin`, `unpin`. Only `clone` and
`pin` name a project; `unpin` may name a directory; every other verb acts on the tree it is
standing in and finds its ledger by walking up. `dsx -C <dir>` moves first.

Every MCP tool the server exposes is reachable, addressed as `dsx <noun> <verb>` — `project`,
`ds`, `files`, `plan`, `conv`, `comment`, `member` — with `dsx raw <tool> '<json-args>'` for
anything the named commands do not wrap. `dsx auth`, `dsx doctor`, `dsx version`,
`dsx completion` for diagnostics.

### For callers

`--json` makes stdout exactly one JSON document on every command. Exit codes distinguish the
responses that differ: `0` done, `1` failed, `2` bad invocation, `3` conflict, `4` transport,
`5` auth. A dry run carries the same code as the run it previews. Errors go to stderr with a
stable `error` token an agent can branch on.

### On disk

Two files, both under `.dsx/` in the synced directory: the ledger `state.json` and the fetch
baseline `baseline.json`. **These are the compatibility contract.** Anything that changes
either shape is a breaking change and will be called one here.

`.dsxignore` filters both directions, in gitignore's syntax minus character classes.

[Unreleased]: https://github.com/somework/dsx/compare/v0.1.0...main
[0.1.0]: https://github.com/somework/dsx/releases/tag/v0.1.0
