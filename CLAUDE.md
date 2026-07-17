# Working on dsx

dsx moves files between a Claude Design project and a local directory **without the bytes
passing through a model's context**. That is the whole point: the same pull done by agents
reading files cost ~665k tokens; dsx costs one summary line. Every design decision serves
that, and second to it, not losing data.

Read [PROTOCOL.md](PROTOCOL.md) before touching anything that talks to the server. The
endpoint is undocumented; everything we know was bought by probing it, and some of it is
counter-intuitive enough that guessing reliably produces wrong code.

## Orientation

**Start at `main.go`. It is the only `.go` file in the root, and that is on purpose** — it
holds nothing but `func main` and `var version`. Everything else is a layer below, leaves
first; each may import only what is above it:

| Package | Holds |
|---|---|
| `internal/dsxerr` | the error taxonomy and exit codes — the contract with an agent. The one true leaf: everything reports through it, it imports nothing of ours |
| `internal/fmtutil` | `Truncate` and `Bytes` — the two helpers more than one layer needs, and the only reason the package exists |
| `internal/auth` | credential resolution. **Never writes, never prints the token** — there is no setter for `Client.token` anywhere. Includes the Keychain lane (`auth_darwin.go`) and its absence elsewhere |
| `internal/mcp` | JSON-RPC transport, retry, SSE unwrapping, error types; `read_file` wrapper parsing and window reassembly (`envelope.go`, with `ReadFull` — they are one concern) |
| `internal/mcptest` | the fake endpoint. **Imports `mcp` never**, so `mcp`'s own internal tests can import it without a cycle |
| `internal/syncer` | the sync engine — see its own table below. Exports 17 top-level names plus the two reports' `Render`; `go doc -short` is the list. Everything a mistake could cost data through stays unexported — `planPull`, `planPush`, `localFile`, `safeJoin`, `checkRemotePath`, `writeBatch`, `deletePaths`, `save` |
| `internal/cli` | dispatch, `usage`, flags, one thin function per MCP tool, `doctor`, completions, `--version`. **Exports exactly one name: `Main`** |

Not `internal/sync`: `pull.go` and `tree.go` import the stdlib's `sync` for `Mutex` and
`WaitGroup`. `package sync` beside `import "sync"` compiles and neither vet nor gofmt says
a word, which is precisely the kind of ripple this split existed to remove.

Inside `internal/syncer`:

| File | Holds |
|---|---|
| `plan.go` | **the sync decisions, pure functions** — where a mistake costs data. Has no import block at all; that is the point |
| `pull.go` / `push.go` | I/O around those decisions |
| `state.go` | `.dsx-state.json` ledger, path safety, filesystem fold identity |
| `ignore.go` | `.dsxignore`, the built-ins no rule may negate, and `survey` — the only way to filter |
| `tree.go` | concurrent recursive listing; clamps its own concurrency |
| `grant.go` | the `finalize_plan` self-authorisation path, below every caller |
| `outcome.go` | `ConflictOutcome`: a report's conflicts → the exit status. Here, not in `cli`, so that "this classification reaches exit 3" stays assertable beside the classification. Not in `plan.go`: it needs `dsxerr` |

Elsewhere:

| | |
|---|---|
| `main.go` | `func main` and `var version` — **`-X main.version`'s target, and it can live nowhere else.** See invariant 10 |
| `internal/cli/cli.go` | `Main`, `run` and its dispatch, `usage`, `emit`, sync command wiring |
| `internal/cli/completion.go` | `commandNames`, which drives dispatch, `usage` and the shells |
| `fake_test.go` | **exists twice, in `cli` and in `syncer`, deliberately** — what `mcptest` cannot know: how to build a client, the domain shapes, `captureStdout`. It cannot be shared: `syncer`'s tests are internal ones (they drive `planPull`), so they cannot import anything that imports `syncer` — and any package holding `listingFor` would have to. Go's test rules leave no third option; the file says so at the top |
| `reference/mcp-tools.json` | the server's own `tools/list` output, verbatim. `internal/mcp`'s tests reach it at `../../reference` |

`plan.go` is deliberately pure and separate. Decisions there are testable without a network;
keep it that way. If you find yourself needing a client inside it, the design is drifting.
`RemoteEntry`, `localFile`, `State` and `FileState` live beside it for exactly this reason —
that is why `RemoteEntry` did **not** move into `mcp` with the rest of the listing shapes.

## Invariants

Each of these is here because breaking it cost something real. Do not relax one without
reading why it exists.

1. **Never write a file whose decoded length disagrees with `list_files`' `size`.**
   This assertion is what caught an earlier agent-driven pull silently corrupting 2 of 100
   files. It also stopped a second one: when `readFull` was found to be splicing the server's
   truncation notice into windowed reads, `pull` had been refusing those files all along
   while `dsx cat` wrote them out. A mismatch means the decode is wrong; refusing beats
   landing a corrupt file.

2. **Conflicts key off the bytes, never off the etag.** An etag comparison only sees the
   server, so it cannot see the case where *both* sides changed. That exact bug shipped:
   every conflict guard began `prev.Etag == r.Etag`, so the canonical conflict fell through
   to a silent overwrite. The test suite covered the strictly safer case and gave false
   confidence. See `TestPlanPullBothSidesChangedIsAConflict`.

3. **An interrupted operation is a failure, not a short success.** A goroutine leaving via
   `<-ctx.Done()` records nothing, so checking only the error slice reports a partial tree
   as complete — after which `--prune` reads "not enumerated" as "deleted on the server".
   Keep the caller's context separate from the derived one: the derived one is cancelled by
   our own error path too, so only the parent can distinguish "interrupted" from "gave up".

4. **`--prune` deletes only what we can prove was ours and unmodified.** Untracked → not
   ours. Locally edited → the only copy left, so it is a conflict. `binary: true` entries are
   tracked but were never on disk; their absence is not a deletion. **A path that exists but
   is not a regular file is not proof of anything** — dropping symlinks from the scan made
   them indistinguishable from a deletion, and `push --prune` deleted the server's copy with
   a matching `if_match`, so the server complied. `localFile.Irregular` exists for that.

   And when `--prune` *does* refuse, say which kind of refusal it is: `--force` resolves an
   ordinary conflict by overwriting (the bytes survive on the server) and a prune-conflict by
   **deleting** (they survive nowhere). Both once printed "--force to overwrite".

5. **The ledger is saved whenever bytes moved**, including on error paths. Files on disk with
   no ledger entry become conflicts on the next run, which pushes the user toward `--force` —
   the opposite of safe.

   **Its on-disk shape is a compatibility contract**, and `loadState`/`save` cannot police it:
   they agree with each other by construction, so a renamed json tag passes both. It was
   measured — mutating `json:"sha256"` to `json:"sha"` left all 581 tests green, while in the
   field every entry would decode to `SHA=""`, which `plan.go`'s `localDirty` reads as "every
   tracked file is modified": every path a conflict, exit 3, `--force` the apparent way out.
   `ledger_golden_test.go` holds the bytes. Its fixture is hand-written on purpose — one
   regenerated from the structs would only prove the code equals itself.

6. **Only read-only tools may be retried on a transport fault.** A network error can land
   after the server applied a mutation. `429` is always safe (rejected before it ran); `5xx`
   and connection errors are not. See `readOnlyTools`, which matches `readOnlyHint` in
   `reference/mcp-tools.json` exactly. Note the server sets `destructiveHint: true` on
   read-only tools like `read_file`, so `destructiveHint` is **not** a usable retry signal.

7. **Remote paths are untrusted input.** `safeJoin` refuses escapes *including through
   symlinks* (lexical `Clean`/`Abs` cannot see them); `checkRemotePath` refuses names that
   stay inside the root but must never be written — `.git`, `node_modules`, the ledger itself.
   `.dsxignore`'s built-ins are built from the same list, so the two cannot drift. **Both
   compare case-insensitively, because the filesystem does**: macOS ships case-insensitive
   APFS, so `.GIT/config` *is* `.git/config` and a case-sensitive guard is no guard.

   The bytes are untrusted too, not just the paths. A file's own content can look like the
   server's framing: see the truncation notice in invariant 1's neighbourhood, where an
   unanchored strip let a file that merely *described* the notice delete its own tail.

8. **The token is read, never written, never printed.** Refreshing it here would rotate the
   refresh token out from under Claude Code and break its login. An expired token is
   reported; the fix is for the user to run any `claude` command.

9. **`.dsxignore` filters both sides, never one.** A path hidden from the local scan but left
   in the server's listing is indistinguishable from a file the user deleted, so `push
   --prune` would delete it from the server. An ignored path is not dsx's business in either
   direction.

   **`survey` is the only way to filter, and it takes no `*ignoreSet` on purpose**: a caller
   holding one could still hand a different set to each side. `Pull` and `Push` used to
   spell out `loadIgnore` → `filterRemote` → `scanLocal` by hand — a rule two callers had to
   remember, with two chances to forget.

   **Outside `internal/syncer` the compiler now enforces this**, because `survey`,
   `loadIgnore`, `filterRemote` and `scanLocal` are all unexported: a one-sided filter in
   `cli` is not a rule to remember, it is a name that does not resolve. Keep them that way —
   exporting any one of them silently demotes this half of the invariant back to a
   convention. The tests below still matter: inside `syncer` the compiler sees nothing.

   Note which test guards what, because three of them are named for this invariant and only
   one drives it. `TestIgnoredPathIsNeverPrunedFromTheServer` and `...FromDisk` call
   `filterRemote` and `planPush`/`planPull` directly and hand-assemble the correct pairing
   themselves, so they guard **the decision, not the wiring** — they stay green when `survey`
   filters only one side. Their comment says "end to end"; it is not.

   The wiring is guarded by `prune_ignore_test.go`, which drives `Push`/`Pull` against a
   real `.dsxignore` and asks the only shape-blind question there is: did an ignored path reach
   `delete_files`? Two traps live there — push's delete sends **`files`** (maps of
   `{path, if_match}`) while `dsx rm` sends `paths`, so a probe reading `paths` passes
   vacuously; and `--prune` is only exposed by a ledger that tracked the path **before** it was
   ignored, since an empty ledger makes prune a correct no-op via invariant 4 and hides
   everything.

   `TestSyncCallersCannotFilterOneSide` is the structural half: nothing but `survey` may name
   `loadIgnore`, `filterRemote` or `scanLocal`. It scans every non-test file and fails if it
   cannot find `survey` — an earlier version parsed a hardcoded list of callers by name, and
   moving the function to another file, renaming it, or hoisting the calls into a helper each
   made it match nothing and pass while a live one-sided filter shipped. **Syntax cannot see
   `_, local, err := survey(...)`**, which names nothing forbidden and still breaks the
   invariant; only the behavioural test catches that.

10. **`var version` stays in `main.go`, named `version`, and reaches `cli.Main`.** It is
    `-X main.version`'s target: `-X` names a symbol by its full path, and one aimed at a
    symbol that no longer exists **builds with no error and no warning**. Rename it, move it
    into `cli`, or stop threading it, and the release binary ships reporting `(unknown)`.

    Nothing in the test suite can catch this, and that is the point: `version_test.go` hands
    `stamped` to `buildVersion` itself rather than reading the var, so it stays green through
    all three mutations. Measured, not assumed — renaming `version` to `release` leaves
    `go build` clean and all six packages green while the binary goes silent.

    A link-time property needs a link-time check, so CI builds with
    `-X main.version=v0.0.0-cistamp` and greps for it. That step is the only guard; do not
    delete it as redundant. Before the split this could not break — `var version` and
    `cmdVersion` were in one file — so the invariant is younger than the code it protects.

## Testing

```bash
go test -race ./...     # 624 tests (324 top-level)
go vet ./... && go vet -tags=live ./... && gofmt -l .
```

**Unit tests cover `plan.go`, `envelope.go`, `state.go`, `ignore.go`, `dsxerr.go` and
`mcp.go`'s parsing** — the pure logic. Aim high there; that is where data loss lives. CI
enforces 80% overall and 95% on `plan.go` and `envelope.go`.

**The floor matches by filename substring, so it followed `plan.go` into `internal/syncer`
for free — and it is gameable in a way worth knowing.** Moving a function out of a held file
raises that file's number without covering anything: a one-line wrapper once dropped
`envelope.go` from 100% to 85.7%, and the reverse trick works too. If a function is hard to
cover where it is, export it and test it; do not relocate it past the ratchet.

**`fake_test.go` is a fake endpoint for dsx's own handling — never for protocol facts.**
Retry, batching, ledger bookkeeping, error classification: all fair game. But a mock can only
repeat what we already believe. Three protocol facts were guessed **wrong** first —
`write_files` returns a map not a list; `needs_project_grant` is an HTTP 403 not a tool error;
binary detection is by content not extension — and a green mock would have hidden all three.

**Protocol claims are verified against the live service** (`live_test.go`, `-tags=live`). That
suite is not decoration: it found the truncation-notice corruption, which no amount of mocking
could have. Every claim in PROTOCOL.md has a test there. If one fails, PROTOCOL.md is what is
wrong.

When adding a test for a defect, **write it red first and watch it fail.** The both-sides bug
was invisible precisely because the existing test looked like it covered the case. Better
still, prove the test earns its place: break the line it covers, watch it go red, restore.
Several tests in this suite that looked like coverage turned out to encode framing the server
does not use.

### Live testing discipline

Live tests touch the user's real projects. There is **no `delete_project` tool**, so a
throwaway project is permanent litter — do not create one. `TestLiveRefusesToCreateProjects`
enforces that by reading its own source.

- write only to `.dsx-selftest*` paths; `liveScratch` registers the removal and verifies it
- pull into `t.TempDir()`, never into `design/`
- every mutating test asserts the project's file count is back where it started

Test project: `bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb` (Kolgarn Design System, 109 files),
overridable with `DSX_LIVE_PROJECT`. `dsx projects` lists the rest. The project has no
standing write grant, so every live write exercises the `finalize_plan` self-authorisation
path — which is how that path stays honest.

## Known unknowns

Do not present these as facts; they are untested.

- **The plaintext credential lane has never met a real file.** The path, mode and payload are
  read out of Claude Code's shipped binary rather than guessed from a bare string, but no
  Linux machine was available, so dsx has never read a `.credentials.json` that `claude`
  wrote. This is the largest remaining gap and the reason CI builds for Linux but cannot
  prove auth there.
- **Byte-exactness of a binary push is unverified.** Size matches (67 in, 67 on the server),
  but confirming the bytes requires reading them back, which is the one thing that does not
  work. Do not claim `cmp`-level proof.
- The write allowlist was probed with ~20 extensions; it is not exhaustive.
- `render_preview`, `put_conversation`, and the member/sharing tools are wired but only
  smoke-tested. Their result shapes are not modelled.
- Behaviour of `finalize_plan` token expiry mid-push (path-scoped ≈15 min) has not been hit.
- Whether the server rotates refresh tokens — the reason auth.go refuses to refresh — is
  assumed, not measured.

## Conventions

- Go stdlib only. No dependencies. This is a hard constraint, not a preference: it keeps the
  binary a single trustworthy artifact that reads a credential. `staticcheck` and `goreleaser`
  run in CI without entering `go.mod`.
- Terse output by default; `--json` for machines; never print file contents unless asked.
  Output width is a token budget. Any feature that makes dsx chattier by default is a
  regression against its entire reason to exist.
- Errors say what to do next, not just what failed, and carry an `errKind` an agent can
  branch on. The message may be reworded; the token may not.
- Comments explain *why*, especially where the code looks over-careful. Most of the
  care in this codebase is load-bearing.
- Russian in commit messages, English in code and docs (matches the repo).

## Roadmap

See [ROADMAP.md](ROADMAP.md). The short version: `go install` works, auth is no longer
macOS-only *in code*, CI and release machinery exist. The open question is whether this
should be published at all; ROADMAP states it plainly, and it is the owner's call, not an
agent's.
