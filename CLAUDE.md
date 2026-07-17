# Working on dsx

dsx moves files between a Claude Design project and a local directory **without the bytes
passing through a model's context**. That is the whole point: the same pull done by agents
reading files cost ~665k tokens; dsx costs one summary line. Every design decision serves
that, and second to it, not losing data.

Read [PROTOCOL.md](PROTOCOL.md) before touching anything that talks to the server. The
endpoint is undocumented; everything we know was bought by probing it, and some of it is
counter-intuitive enough that guessing reliably produces wrong code.

## Orientation

| File | Holds |
|---|---|
| `main.go` | CLI dispatch, `usage`, sync command wiring, `emit` |
| `cmds.go` | one thin function per MCP tool; `raw` is the escape hatch |
| `exit.go` | the error taxonomy and exit codes — the contract with an agent |
| `auth.go` | credential resolution. **Never writes, never prints the token** |
| `auth_darwin.go` / `auth_other.go` | the Keychain lane, and its absence elsewhere |
| `mcp.go` | JSON-RPC transport, retry, SSE unwrapping, error types |
| `envelope.go` | `read_file` wrapper parsing, entity decoding, window reassembly |
| `tree.go` | concurrent recursive listing |
| `ignore.go` | `.dsxignore`, and the built-ins no rule may negate |
| `plan.go` | **the sync decisions, pure functions** — where a mistake costs data |
| `pull.go` / `push.go` | I/O around those decisions |
| `state.go` | `.dsx-state.json` ledger, path safety |
| `doctor.go` | `dsx doctor` — the first thing to run when something is wrong |
| `completion.go` | `commandNames`, which drives dispatch, `usage` and the shells |
| `version.go` | `--version`, ldflags-stamped or build-info-derived |
| `reference/mcp-tools.json` | the server's own `tools/list` output, verbatim |

`plan.go` is deliberately pure and separate. Decisions there are testable without a network;
keep it that way. If you find yourself needing a client inside it, the design is drifting.

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
   direction. See `TestIgnoredPathIsNeverPrunedFromTheServer`.

## Testing

```bash
go test -race ./...     # 581 tests
go vet ./... && gofmt -l .
```

**Unit tests cover `plan.go`, `envelope.go`, `state.go`, `ignore.go`, `exit.go` and `mcp.go`'s
parsing** — the pure logic. Aim high there; that is where data loss lives. CI enforces 80%
overall and 95% on `plan.go` and `envelope.go`.

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
