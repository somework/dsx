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

Nothing released yet. `go install github.com/somework/dsx@latest` installs the current `main`.

The on-disk formats that a future version would have to keep compatible are the ledger
(`.dsx/state.json`) and the fetch baseline (`.dsx/baseline.json`). Anything that changes
either is a breaking change and will be called one here.

[Unreleased]: https://github.com/somework/dsx/commits/main
