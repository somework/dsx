# Working on dsx

dsx moves files between a Claude Design project and a local directory **without the bytes
passing through a model's context** — a pull that costs agents ~665k tokens costs dsx one
summary line. Every decision serves that; second to it, not losing data.

Read [PROTOCOL.md](PROTOCOL.md) before touching anything that talks to the server: the
endpoint is undocumented and parts of it are counter-intuitive enough that guessing produces
wrong code.

## Orientation

`main.go` holds only `func main` and `var version`. Everything else is a layer below, leaves
first; each imports only what is above it.

| Package | Holds |
|---|---|
| `internal/dsxerr` | error taxonomy and exit codes — the agent-facing contract. The one true leaf; imports nothing of ours |
| `internal/fmtutil` | `Truncate` and `Bytes` |
| `internal/auth` | credential resolution. Reads the token, never writes or prints it (invariant 8). Keychain lane in `auth_darwin.go` |
| `internal/mcp` | JSON-RPC transport, retry, SSE unwrapping, error types; `read_file` window reassembly (`envelope.go`, `ReadFull`) |
| `internal/mcptest` | the fake endpoint. Never imports `mcp`, so `mcp`'s own tests can use it |
| `internal/syncer` | the sync engine. Data-loss-critical decisions stay unexported (`planPull`, `planPush`, `safeJoin`, `checkRemotePath`, `writeBatch`, `save`) |
| `internal/cmd` | command kernel: the `Command`/`Group`/`Needs` types and the shared parse/emit helpers. Knows nothing about which commands exist |
| `internal/cmd/<group>` | one package per product group (`conv`, `escape`, `files`, `members`, `plans`, `projects`, `sync`), each exporting exactly one `Group`. `cmdX` stay unexported |
| `internal/clitest` | the fake-endpoint adapter as a real package. `_test.go`-only, never in the binary |
| `internal/cli` | the program's view of itself: `Main`, `run`, the `groups` registry, generated `usage`, the `diag` group. Exports one name: `Main` |

Structure rules that are load-bearing, not taste:

- **`plan.go` is pure and has no import block.** Its decisions are testable without a network; keep it that way. `RemoteEntry`, `localFile`, `State`, `FileState` live beside it for the same reason.
- **`groups` (in `cli`) is the single source; `commandIndex`, `commandNames` and `usage` derive from it in `init()`.** `diag` stays in `cli` because `cmdHelp`/`cmdCompletion` read those derived vars and `groups` must sit above every group — a `diag` package would close an import cycle.
- **The sync engine is `internal/syncer`, and the sync command package is `synccmd` (dir `internal/cmd/sync`), never `sync`** — a package named `sync` beside `import "sync"` silently shadows the stdlib.
- **Group/command filenames carry no underscore.** Go reads the last `_`-segment as a build constraint, so `files_js.go` would build only under `GOOS=js`, silently.
- **`internal/syncer/fake_test.go` is a deliberate duplicate of `clitest`** — syncer's internal tests can't import `clitest`, which imports `syncer` (a cycle).

## Invariants

Each cost something real to learn. Do not relax one without understanding why it exists; the
named test is the guard.

1. **Never write a file whose decoded length disagrees with `list_files`' `size`.** A mismatch means a corrupt decode; refuse rather than land a bad file. Caught 2/100 silently corrupted files, and a truncation-notice splice besides.
2. **Conflicts key off the bytes, never the etag.** An etag comparison can't see the both-sides-changed case. `TestPlanPullBothSidesChangedIsAConflict`.
3. **An interrupted operation is a failure, not a short success.** A goroutine leaving via `<-ctx.Done()` records nothing; keep the caller's context separate from the derived one so only the parent distinguishes "interrupted" from "gave up", or `--prune` reads a partial tree as server-side deletions.
4. **`--prune` deletes only what we can prove was ours and unmodified.** Untracked → not ours; locally edited → a conflict; `binary: true` → never on disk, so its absence is not a deletion. A path that is not a regular file is not proof of anything (`localFile.Irregular`). And a prune-conflict under `--force` *deletes* — say so, don't print "--force to overwrite".
5. **The ledger is saved whenever bytes moved, error paths included.** A file on disk with no ledger entry becomes a conflict next run, pushing the user to `--force`. Its on-disk shape is a compatibility contract that `load`/`save` can't police (they agree by construction); `ledger_golden_test.go` holds hand-written bytes.
6. **Only read-only tools may be retried on a transport fault.** A `5xx`/connection error can land after the server applied a mutation; `429` is safe (rejected before running). `readOnlyTools` matches `readOnlyHint` in `reference/mcp-tools.json`. `destructiveHint` is **not** a usable retry signal (the server sets it `true` on read-only tools).
7. **Remote paths and their bytes are untrusted input.** `safeJoin` refuses escapes including through symlinks; `checkRemotePath` refuses `.git`, `node_modules`, the ledger. Both compare case-insensitively because APFS folds case. A file's content can mimic the server's framing, so strips must be anchored.
8. **The token is read, never written, never printed.** Refreshing it would rotate the refresh token out from under Claude Code. An expired token is reported; the fix is to run any `claude` command.
9. **`.dsxignore` filters both sides, never one.** A path hidden locally but left in the server listing is indistinguishable from a user deletion, so `push --prune` would delete it. `survey` is the only filter and takes no `*ignoreSet` by design; `loadIgnore`/`filterRemote`/`scanLocal` are unexported so a one-sided filter outside `syncer` won't compile. Wiring is guarded by `prune_ignore_test.go`; the structural half by `TestSyncCallersCannotFilterOneSide`.
10. **`var version` stays in `main.go`, named `version`, threaded to `cli.Main`.** It is `-X main.version`'s target; `-X` aimed at a symbol that no longer exists builds clean and ships `(unknown)`. No test can catch this — CI builds with `-X main.version=v0.0.0-cistamp` and greps. Do not delete that step.
11. **`groups` is the only place a command is declared.** Dispatch, shells and `dsx help` all derive from it, so a missing command is absent code, not a failing test. `TestEveryCommandFormStartsWithItsName` guards `Form` vs `Name`; `TestEveryDeclaredGroupIsRegistered` (parses `.` and `../cmd/*`, resolves the import alias) guards a `Group` var forgotten from `groups`; `TestUsageIsGeneratedByteForByte` holds the hand-written usage golden. Derivation is in `init()`, not var initialisers, to avoid an import cycle.

## Testing

```bash
go test -race ./...     # 627 tests (343 top-level)
go vet ./... && go vet -tags=live ./... && gofmt -l .
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

- Unit tests cover the pure logic — `plan.go`, `envelope.go`, `state.go`, `ignore.go`, `dsxerr.go`, `mcp.go` parsing. CI enforces 80% overall, 95% on `plan.go` and `envelope.go`. The floor matches by filename substring and is gameable by moving a function out of a held file; don't relocate past the ratchet.
- **`fake_test.go` tests dsx's own handling, never protocol facts** — a mock only repeats what we already believe. Three protocol facts were guessed wrong first (`write_files` returns a map; `needs_project_grant` is HTTP 403; binary detection is by content), and a green mock would have hidden all three.
- **Protocol claims are verified live** (`live_test.go`, `-tags=live`); every claim in PROTOCOL.md has a test there. If one fails, PROTOCOL.md is wrong.
- **Write a defect's test red first and watch it fail.** Better, prove it earns its place: break the line it covers, watch it go red, restore.

### Live testing discipline

Live tests touch real projects and there is **no `delete_project` tool** — never create a throwaway one (`TestLiveRefusesToCreateProjects` enforces it).

- write only to `.dsx-selftest*` paths; `liveScratch` registers and verifies removal
- pull into `t.TempDir()`, never `design/`
- every mutating test asserts the file count is back where it started

Test project `bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb` (override with `DSX_LIVE_PROJECT`). It has
no standing write grant, so every live write exercises the `finalize_plan` self-authorisation
path.

## Known unknowns

Untested — do not present as fact.

- The plaintext credential lane has never read a real `.credentials.json` (no Linux machine). Largest remaining gap.
- Byte-exactness of a binary push is unverified — size matches, bytes were never read back.
- The write allowlist was probed with ~20 extensions; not exhaustive.
- `render_preview`, `put_conversation`, member/sharing tools are wired but only smoke-tested.
- `finalize_plan` token expiry mid-push (~15 min) has not been hit.
- Whether the server rotates refresh tokens (the reason `auth.go` refuses to refresh) is assumed.

## Conventions

- Go stdlib only, no dependencies — a hard constraint that keeps the binary a single trustworthy artifact. `staticcheck`/`goreleaser` run in CI without entering `go.mod`.
- Terse output by default; `--json` for machines; never print file contents unless asked. Output width is a token budget.
- Errors say what to do next and carry an `errKind` an agent branches on. Reword the message, never the token.
- Russian in commit messages, English in code and docs.

## Roadmap

`go install` works, auth is no longer macOS-only *in code*, CI and release machinery exist.
The open question is whether this should be published at all — the owner's call.
