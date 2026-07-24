# Contributing to dsx

dsx moves files between a Claude Design project and a local directory without the bytes
passing through a model's context. Everything below serves that; second to it, not losing
data.

Two documents are worth reading before the code:

- [CLAUDE.md](CLAUDE.md) — the map of the packages, and the numbered invariants. Each one cost
  something real to learn, and several were bought with actual data loss. Read it before
  changing anything under `internal/syncer`.
- [PROTOCOL.md](PROTOCOL.md) — the endpoint, as measured. It is undocumented and several facts
  in it contradict the obvious guess, so guessing produces wrong code that reads plausibly.

## Getting set up

Go 1.26 or newer, and nothing else:

```bash
git clone https://github.com/somework/dsx
cd dsx
go build .
```

There is no dependency to install, and adding one is a change to a hard constraint — see
[Things that will be sent back](#things-that-will-be-sent-back).

## Running the tests

The offline suite needs no login and reaches no network. It is the one to run, and the one CI
runs:

```bash
go test -race ./...
go vet ./... && go vet -tags=live ./... && gofmt -l .
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

`staticcheck` and `govulncheck` are separate CI jobs, so build, vet and the tests all stay
green while either one fails. Run them before you push rather than after CI tells you.

The version pins differ on purpose. `staticcheck` is pinned because it adds checks between
releases, and an unrelated pull request going red on a day nobody touched it is noise;
bumping it is a commit of its own. `govulncheck` is not pinned, because a newly disclosed
vulnerability failing CI is the signal the job exists for. Keep the staticcheck version here
identical to `.github/workflows/ci.yml` — a lint gate that disagrees with CI is worse than no
gate.

`go.mod` naming no dependency does not make dsx vulnerability-free: govulncheck reads the
standard library and the toolchain, which is what parses untrusted server bytes and holds the
credential.

CI also enforces coverage floors: 80% overall, and 95% on `plan.go` and `envelope.go`, because
that is where a bug costs data rather than convenience. The floor matches by filename
substring — moving a function out of a held file walks past the ratchet, so do not.

### The live suite

```bash
DSX_LIVE_PROJECT=<project id> go test -tags=live -run TestLive ./...
```

Or put the id in a `.env` file at the repo root — it is gitignored, and `.env.example`
explains the choice. An environment variable of the same name wins over the file.

The live suite talks to the real endpoint with your own credential and **writes** to the
project you name: each test creates and removes `.dsx-selftest*` paths, and asserts the file
count is back where it started. There is no `delete_project` tool, so the suite is forbidden
from creating one (`TestLiveRefusesToCreateProjects` enforces it) and there is no default
project — with the variable unset, every test that needs one skips rather than aiming at
someone else's.

Which project you pick decides what gets covered. A project you created carries a standing
write grant, which leaves the `finalize_plan` self-authorisation path and the
`needs_project_grant` 403 untested; covering those needs a project shared with you but not
created by you.

CI never runs the live suite and must not acquire a credential to.

## How changes are expected to arrive

### Write the test red first, and watch it fail

Not "add a test alongside the fix". Write the test, run it, see it fail for the reason you
think it fails, then fix the code. Better still, prove the test earns its place: break the
line it covers, watch it go red, restore it. A test that passes both before and after a change
is documenting an intention, not guarding a behaviour — and several such tests have been found
here after the fact, each of them green for a reason nobody intended.

### A mock proves handling, never protocol

`internal/mcptest` and the `fake_test.go` files test **dsx's own handling** of a reply. They
cannot establish what the server does, because they only repeat what we already believe. Three
protocol facts were believed wrong before they were measured, and a green mock would have
hidden all three.

So: anything that claims the server behaves a certain way needs a live test in
`internal/syncer/live_test.go` and a line in PROTOCOL.md. Note that a live test's *judgment*
belongs outside the `//go:build live` tag — CI never compiles that file, so a mutation inside
it fails nothing. The pattern is an untagged `…Verdict` function with an ordinary table test,
and only the plumbing under the tag.

### Docs are claims, and they are executed

A sentence naming a command or a flag is a testable claim, and the ones a reader is
instructed by are tested. `TestPublishedDocsNameOnlyRealCommandsAndFlags` walks every
`dsx …` and every standalone flag inside a code span — in README, in this file, in
SECURITY.md, in the pull-request and issue templates — and checks each against the real
registry. `TestEveryDsxInvocationInSourceNamesARealCommand` does the same for every
backticked invocation in production Go source, including the ones printed inside refusal
messages.

CLAUDE.md and PROTOCOL.md are deliberately outside that guard. They record what was measured
and what was removed, so they quote dead flags and other programs' flags on purpose, and a
guard firing on an accurate historical record is one people switch off.

None of these read English. They catch a form that no longer parses; they cannot catch a
sentence that describes behaviour dsx does not have. So run what you wrote before you write
it down.

## Commit messages

Conventional commits, with the type carrying the change:

```
feat(sync): push --force-with-lease
fix(push): status after push claimed "server moved ahead" about its own write
docs(protocol): get_conversation has no window — 29 fields checked
```

Types in use: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`. A `!` before
the colon marks a breaking change to the CLI surface.

**The existing history is in Russian.** It is a personal project that was opened up, not one
that started public, and rewriting the history would destroy the only record of why each
decision was made. Commits from contributors should be in **English**, as should issues
and pull requests. Both languages will be readable in `git log` forever; that is the honest
cost of the history being kept intact.

Code, comments and documentation are English throughout, with no exception.

## Style

`gofmt` decides formatting; there is nothing to argue about. Beyond that:

- **Many small files over few large ones.** 200–400 lines is typical, 800 is the ceiling.
- **Filenames carry no underscore** for group and command files. Go reads the last
  `_`-segment as a build constraint, so `files_js.go` would build only under `GOOS=js`,
  silently.
- **A comment that restates the code is noise.** The long ones in this repo are long because
  they record a measurement, or the defect the line below prevents, and that is not
  recoverable from the code. If you cannot name what a comment protects, delete it.
- **Data-loss-critical decisions stay unexported.** `planPull`, `planPush`, `safeJoin`,
  `checkRemotePath`, `writeBatch`, `save`. A caller outside `syncer` must not be able to reach
  them, and several structural tests exist to keep that true.
- **Cite no count that churns.** Test totals and tool counts rot into a quiet lie on the next
  commit. A number that pins a measurement — a file size, a byte cap, a coverage floor — is
  evidence and stays.

## Things that will be sent back

- **A dependency.** `go.mod` names none, and that is what keeps the binary a single artifact
  you can reason about — it reads an OAuth credential, so what it links against is the whole
  question. `staticcheck` and `goreleaser` run in CI by version and never enter `go.mod`.
- **A relaxed invariant with no measurement behind it.** CLAUDE.md numbers them and names the
  test that guards each. Relaxing one is a fine thing to propose; doing it because the
  constraint looked redundant is how two prune loops learned to delete files they should not
  have.
- **A doc change that was not run.** See above.
- **A live test that creates a project.** There is no tool to delete one.
- **Widening the surface to make a test pass.** If a guard fires on your change, the guard is
  the finding until proven otherwise.

## Opening a pull request

`main` is protected and takes no direct push: every change arrives as a pull request, and the
CI checks must pass before it can merge — both `test` platforms, `staticcheck`, `govulncheck`,
`crossbuild`, and one `smoke` per matrix row, which is more rows than there are shipped
binaries because darwin/amd64 is run on two macOS versions. The branch also has to be current with
`main` at merge time, so CI has run against what will actually land rather than against a
snapshot that has since moved.

No approval is required, because the review that matters here is the one in
[Things that will be sent back](#things-that-will-be-sent-back). History is linear: merge
commits are off, so a pull request lands squashed or rebased.

## Reporting security issues

Not through an issue — see [SECURITY.md](SECURITY.md).

## Licence

Contributions are accepted under the MIT licence the project ships under. See
[LICENSE](LICENSE).
