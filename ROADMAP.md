# dsx roadmap

Current state: 509 tests green under `-race`, plus 20 live tests against the real endpoint.
Coverage 89.7% overall, 96% on `plan.go`, 100% on `envelope.go`. All 20 MCP tools reachable.
`go install github.com/somework/dsx@latest` works. CI builds and tests on Linux and macOS.

Good enough to depend on personally, and now technically ready to hand to someone else. Whether
it *should* be handed to someone else is the question below, and it is not a technical one.

## Decide first: is this published?

This is not a technical question and it is the only thing still gating a release. Answer it
before pushing anything anywhere.

dsx does two things that are fine for personal use and become different questions in public:

1. **It speaks an undocumented private API.** Nothing here is promised. A server deploy can
   change the envelope framing, the etag semantics, or the auth check, and every user's
   install breaks at once. Today that costs one person an afternoon. This is not hypothetical:
   the truncation-notice corruption found on 2026-07-17 was a framing detail nobody had
   measured, and it had been silently damaging every file over 256 KiB that went through
   `dsx cat`.
2. **It reads Claude Code's OAuth token.** For its owner, on their own machine, for their own
   projects — unremarkable. As a published tool, "reads your Claude credentials" is a claim
   users must trust, and one Anthropic has not blessed.

Three honest options:

- **Personal tool** (status quo) — keep it in this repo, no releases, no promises. Cheapest,
  loses nothing that matters today.
- **Published, honest** — README leads with "unofficial, undocumented API, may break without
  notice", and the token story stays exactly what it is: we read what Claude Code already
  stored, never write it, never refresh it. Everything under *Blockers* is now done.
- **Ask first** — Anthropic may simply not mind, or may prefer it not exist. Cheap to find
  out, and the answer changes the plan.

My read: it is genuinely useful and the token handling is defensible (read-only, never
written, never printed, refuses to refresh precisely to avoid breaking Claude Code). The real
exposure is the undocumented API, and that is a documentation problem, not an ethics one. But
it is the owner's account and reputation, so it is the owner's call. **Nothing here should be
published by an agent.**

## Blockers for standalone — cleared

| | Status |
|---|---|
| `module dsx` | **done** — `github.com/somework/dsx`, `go install` resolves |
| macOS-only auth | **done in code, unproven on Linux.** The split is real (`auth_darwin.go` / `auth_other.go`) and the file lane is derived from Claude Code's shipped binary rather than guessed — but it has never read a `.credentials.json` that `claude` actually wrote. See *Correctness gaps*. Windows still unknown |
| no CI | **done** — `go test -race`, `go vet`, `gofmt -l`, `staticcheck`, coverage floors, on linux + macos, cross-building all four release targets |
| no releases | **done, deliberately unfired** — `.goreleaser.yaml` for darwin/linux × arm64/amd64, `draft: true`. Running it is the owner's call |

## Correctness gaps

Ordered by what would actually bite.

1. **The plaintext credential lane has never met a real file.** This is now the largest
   unknown in the project. Everything about it — `<config-dir>/.credentials.json`, mode 0600,
   the `{"claudeAiOauth":{…}}` payload, the keychain-first-then-file order — was read out of
   Claude Code v2.1.211's own storage implementation, which is far better than the guess it
   replaced, but no Linux machine was available to run it against. A single `dsx doctor` on a
   signed-in Linux box would settle it.
2. **`finalize_plan` token expiry mid-push.** Path-scoped tokens last ~15 min. A push large
   enough to outlive one will fail partway. dsx should re-mint per batch, or use
   `scope:"project"` above a size threshold. Untested, so severity is a guess.
3. **A single line over 256 KiB is unreadable.** dsx reports it and refuses. Correct, and now
   distinct from the windowing bug that used to be conflated with it — `_ds_bundle.js`-style
   minified files are exactly the shape that hits this. No workaround exists in the API.
4. **Result shapes unmodelled** for `render_preview`, `put_conversation`, and the
   member/sharing tools. They are wired and smoke-tested; their replies are passed through
   verbatim. Fine until someone scripts against them.
5. **The truncation-notice strip is prose-matching.** `parseEnvelope` recognises the server's
   trailer by a narrow anchored pattern. If the server rewords it, dsx refuses every file over
   256 KiB rather than corrupting one — the right failure, but a failure. The live suite is
   what would catch the rewording.

## UX, for humans

- **Progress on long pulls.** A cold pull is ~9 s of silence. Any fix must not make the
  default output chattier.
- **Conflict resolution.** Today: exit 3 and a path list. No diff, no per-file choice, no
  three-way. `--force` is all-or-nothing and that is a sharp edge.
- ~~Config file / project id from the ledger~~ — **done**, `dsx pull` in a bound directory.
- ~~`.dsxignore`~~ — **done**, filtering both directions.
- ~~Shell completions, `--version`, `dsx doctor`~~ — **done**.

## UX, for agents

This is the part that matters most and is furthest along — worth protecting.

- ~~Stable exit codes~~ — **done**: 0/1/2/3 conflict/4 transport/5 auth.
- ~~`--json` everywhere~~ — **done**, and under it stdout is one JSON document.
- ~~Machine-readable errors~~ — **done**: `{"error":"conflict","paths":[…]}` on stderr.
- **Keep output terse.** Output width is a token budget. Any feature that makes dsx chattier
  by default is a regression against its entire reason to exist. This is the standing one.

## Bigger ideas

- **`dsx watch`** — sync on change. Attractive; conflict handling makes it harder than it
  looks.
- **Git-aware mode.** `design/` is already a git mirror; `dsx` could refuse to pull over
  uncommitted changes and let git be the conflict UI. This may be better than building
  conflict resolution into dsx.
- **Generic MCP file-sync.** Nothing about the sync engine is Design-specific — the ledger,
  the etag diffing, and `plan.go` would work against any MCP server exposing files. Would be
  a real rewrite of the boundary, and speculative until a second server exists.

## Non-goals

- Dependencies. Stdlib-only is a hard constraint for a binary that reads a credential.
- Refreshing the OAuth token. See invariant 8 in CLAUDE.md.
- Publishing without the owner deciding to. See the top of this file.
