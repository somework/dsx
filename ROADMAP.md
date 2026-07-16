# dsx roadmap

Current state: works, 84 tests green under `-race`, all 20 MCP tools reachable, verified
byte-exact against a live 109-file project. Good enough to depend on personally. Not yet a
tool you hand to someone else.

## Decide first: is this published?

This is not a technical question and it gates most of the list below. Answer it before
building release machinery.

dsx does two things that are fine for personal use and become different questions in
public:

1. **It speaks an undocumented private API.** Nothing here is promised. A server deploy can
   change the envelope framing, the etag semantics, or the auth check, and every user's
   install breaks at once. Today that costs one person an afternoon.
2. **It reads Claude Code's OAuth token from the Keychain.** For its owner, on their own
   machine, for their own projects — unremarkable. As a published tool, "reads your Claude
   credentials" is a claim users must trust, and one Anthropic has not blessed.

Three honest options:

- **Personal tool** (status quo) — keep it in this repo, no releases, no promises. Cheapest,
  loses nothing that matters today.
- **Published, honest** — separate repo, README leads with "unofficial, undocumented API,
  may break without notice", no vendored credentials story beyond "we read what Claude Code
  already stored". Needs everything in *Blockers* below.
- **Ask first** — Anthropic may simply not mind, or may prefer it not exist. Cheap to find
  out, and the answer changes the plan.

My read: it is genuinely useful and the token handling is defensible (read-only, never
written, never printed, refuses to refresh precisely to avoid breaking Claude Code). The
real exposure is the undocumented API, and that is a documentation problem, not an ethics
one. But it is the user's account and reputation, so it is the user's call.

## Blockers for standalone

| | Why it blocks |
|---|---|
| `module dsx` | not an importable path; must become e.g. `github.com/<user>/dsx` before anyone can `go install` |
| macOS-only auth | `auth.go` shells out to `security(1)`. The claude binary contains the literal `.credentials.json`, so a file-based fallback exists — but **its path and payload shape are inferred, not measured**; verify on an actual Linux install before writing the `auth_darwin.go` / `auth_unix.go` split. Windows unknown |
| no LICENSE | nobody can legally use it |
| no CI | `go test -race`, `go vet`, `gofmt -l` on push. Trivial; absence is just neglect |
| no releases | `goreleaser` for darwin/linux × arm64/amd64, or leave `go install` as the only path |

## Correctness gaps

Ordered by what would actually bite.

1. **`finalize_plan` token expiry mid-push.** Path-scoped tokens last ~15 min. A push large
   enough to outlive one will fail partway. dsx should re-mint per batch, or use
   `scope:"project"` above a size threshold. Untested, so severity is a guess.
2. **A single line over 256 KiB is unreadable.** dsx reports it and refuses. Correct, but
   `_ds_bundle.js`-style minified files are exactly the shape that hits this. No workaround
   exists in the API; worth confirming whether one is needed at all.
3. **Result shapes unmodelled** for `render_preview`, `put_conversation`, and the
   member/sharing tools. They are wired and smoke-tested; their replies are passed through
   verbatim. Fine until someone scripts against them.
4. **No integration test.** Everything live is manual. A `-tags=live` suite hitting a real
   project (with the scratch-path discipline from CLAUDE.md) would catch protocol drift,
   which is the failure mode most likely to actually happen.

## UX, for humans

- **Config file.** `dsx pull bbbbbbbb-bbbb-… design` every time is hostile. `.dsx.toml` or
  reading the project id from `.dsx-state.json` when the directory is already bound — the
  ledger already knows it. This is the single biggest quality-of-life win and it is nearly
  free.
- **`.dsxignore`.** Currently `scanLocal` hardcodes `.git`/`node_modules`. A project with
  build output has no way to keep it local.
- **Progress on long pulls.** A cold pull is ~9 s of silence.
- **Conflict resolution.** Today: "conflicts 1, --force to overwrite". No diff, no per-file
  choice, no three-way. `--force` is all-or-nothing and that is a sharp edge.
- **Shell completions**, `--version`, `dsx doctor` (checks token, endpoint reachability,
  clock skew).

## UX, for agents

This is the part that matters most and is furthest along — worth protecting.

- **Stable exit codes.** Currently 0/1. Distinguish conflict (needs a human) from transport
  failure (retry) from auth expiry (run `claude`).
- **`--json` everywhere.** `pull`/`push`/`status`/`tree` have it; the passthrough commands
  emit whatever the server sent.
- **Machine-readable errors.** `{"error":"conflict","paths":[…]}` beats prose an agent has
  to pattern-match.
- **Keep output terse.** Output width is a token budget. Any feature that makes dsx chattier
  by default is a regression against its entire reason to exist.

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
- Reimplementing `/design-sync` (the Storybook→Design pipeline baked into the claude binary).
  Different problem entirely.
