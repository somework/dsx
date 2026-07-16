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
| `main.go` | CLI dispatch, `usage`, sync command wiring |
| `cmds.go` | one thin function per MCP tool; `raw` is the escape hatch |
| `auth.go` | Keychain read. **Never writes, never prints the token** |
| `mcp.go` | JSON-RPC transport, retry, SSE unwrapping, error types |
| `envelope.go` | `read_file` wrapper parsing + entity decoding |
| `tree.go` | concurrent recursive listing |
| `plan.go` | **the sync decisions, pure functions** — where a mistake costs data |
| `pull.go` / `push.go` | I/O around those decisions |
| `state.go` | `.dsx-state.json` ledger, path safety |
| `reference/mcp-tools.json` | the server's own `tools/list` output, verbatim |

`plan.go` is deliberately pure and separate. Decisions there are testable without a network;
keep it that way. If you find yourself needing a client inside it, the design is drifting.

## Invariants

Each of these is here because breaking it cost something real. Do not relax one without
reading why it exists.

1. **Never write a file whose decoded length disagrees with `list_files`' `size`.**
   This assertion is what caught an earlier agent-driven pull silently corrupting 2 of 100
   files. A mismatch means the decode is wrong; refusing beats landing a corrupt file.

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
   tracked but were never on disk; their absence is not a deletion.

5. **The ledger is saved whenever bytes moved**, including on error paths. Files on disk with
   no ledger entry become conflicts on the next run, which pushes the user toward `--force` —
   the opposite of safe.

6. **Only read-only tools may be retried on a transport fault.** A network error can land
   after the server applied a mutation. `429` is always safe (rejected before it ran); `5xx`
   and connection errors are not. See `readOnlyTools`.

7. **Remote paths are untrusted input.** `safeJoin` refuses escapes *including through
   symlinks* (lexical `Clean`/`Abs` cannot see them); `checkRemotePath` refuses names that
   stay inside the root but must never be written — `.git`, `node_modules`, the ledger itself.

8. **The token is read, never written, never printed.** Refreshing it here would rotate the
   refresh token out from under Claude Code and break its login. An expired token is
   reported; the fix is for the user to run any `claude` command.

## Testing

```bash
go test -race ./...     # 84 tests
go vet ./... && gofmt -l .
```

**Unit tests cover `plan.go`, `envelope.go`, `state.go`, `mcp.go`'s parsing** — the pure
logic. Aim high there; that is where data loss lives.

**The network layer is verified against the live service, not a mock.** This is deliberate:
a mock would only assert my guesses about the protocol, and those guesses were repeatedly
wrong (`write_files` returns a map not a list; `needs_project_grant` is an HTTP 403 not a
tool error; binary detection is by content not extension). A green mock would have hidden
all three.

When adding a test for a defect, **write it red first and watch it fail.** The both-sides
bug was invisible precisely because the existing test looked like it covered the case.

### Live testing discipline

Live tests touch the user's real projects. There is **no `delete_project` tool**, so a
throwaway project is permanent litter — do not create one. Instead:

- write to a clearly-named scratch path (`.dsx-selftest.txt`), verify, then `dsx rm` it
- pull into a scratch directory, never into `design/`, unless resyncing on purpose
- confirm the project is back to its original file count before you finish

Test project used so far: `bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb` (Kolgarn Design System,
109 files). `dsx projects` lists the rest.

## Known unknowns

Do not present these as facts; they are untested.

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
  binary a single trustworthy artifact that reads a credential.
- Terse output by default; `--json` for machines; never print file contents unless asked.
  Output width is a token budget.
- Errors say what to do next, not just what failed.
- Comments explain *why*, especially where the code looks over-careful. Most of the
  care in this codebase is load-bearing.
- Russian in commit messages, English in code and docs (matches the repo).

## Roadmap

See [ROADMAP.md](ROADMAP.md). The short version: it is macOS-only, its module path is not
importable, and it has no CI — those block "standalone tool". There is also an open question
about whether this should be published at all; ROADMAP states it plainly.
