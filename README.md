# dsx — Claude Design sync

Single Go binary, stdlib only. Moves files between a [Claude Design](https://claude.ai/design)
project and a local directory **without the contents passing through any model's context**.

The same pull done by agents reading files cost ~665k tokens. dsx costs one line:

```
pulled 103, unchanged 0, binary 6 (660.1 KB)
```

> **Unofficial.** The Claude Design MCP endpoint is undocumented and makes no promises. dsx
> was built by probing it and can break with any server deploy. See [PROTOCOL.md](PROTOCOL.md).

## Install

```bash
go install github.com/somework/dsx@latest
```

Requires Go 1.26+ and a signed-in Claude Code. dsx reads the credential Claude Code already
stored, the same way Claude Code reads it: the macOS Keychain first, then
`~/.claude/.credentials.json`. See [Auth](#auth).

## Use

```bash
dsx projects                                   # find your project id
dsx pull  <project> design                     # server → disk
dsx push  <project> design                     # disk → server
dsx status <project> design                    # what a sync would do; transfers nothing
dsx help
```

After the first sync the directory remembers its project, so the id becomes optional and the
directory defaults to `.`:

```bash
cd design && dsx pull
```

Every one of the 20 MCP tools is reachable — `projects`, `tree`, `cat`, `put`, `rm`, `cp`,
`plan`, `preview`, `conv`, `members`, `sharing`, `prompt`. `dsx raw <tool> '<json>'` covers
anything the named commands do not wrap.

`--json` on every command, `-j N` for concurrency, `-n` for a dry run.

## Why it is cheap

`list_files` returns an etag per file, so one listing per directory prices the whole tree up
front: **an unchanged file costs no request at all** — not even a conditional one. A warm
pull of a 109-file project transfers zero bytes and takes ~2 s.

Contents go server→disk directly. Nothing a model reads scales with file size.

## Safety

The sync is three-way against `.dsx-state.json`, a ledger recording — per path — the etag
last agreed with the server and the sha256 of the bytes held at that etag. It also pins the
project id, so a directory cannot be pushed to the wrong project.

- Every write carries `if_match`: the server refuses it if the file moved. Blind overwrites
  are not possible without `--force`.
- Conflicts are reported, never resolved silently — including the case where **both** sides
  changed, which an etag comparison alone cannot see.
- Every pulled file's decoded length is checked against the size `list_files` reported; a
  mismatch refuses the write rather than landing a corrupt file. This is not theoretical —
  it is what proved an earlier agent-driven pull had silently damaged 2 of 100 files, and it
  is what stopped a second corruption bug from ever reaching disk.
- `--prune` deletes only what the ledger proves was ours and unmodified.

## For agents

dsx's primary caller is a program, so the interface is built for one.

**Exit codes** name the three responses that differ:

| | |
|---|---|
| `0` | it did what it was asked |
| `1` | it failed |
| `2` | the invocation was wrong; running it again will not help |
| `3` | **conflict** — both sides hold work; a human must choose |
| `4` | **transport** — the network or the server faulted; a retry may succeed |
| `5` | **auth** — run any `claude` command, then retry |

A dry run exits `0` even with conflicts: it was asked to move nothing, so refusing to move
something is the answer it wanted. `dsx status` is a report and always exits `0` — read its
`--json` for the conflict list.

**`--json`** makes stdout exactly one JSON document. Tools that already answer in JSON pass
through untouched; the rest are wrapped as `{"text":…}`. Errors go to stderr:

```json
{"error":"conflict","message":"local differs from the server","paths":["a.css","b.css"]}
```

`error` is a stable token (`conflict`, `transport`, `auth`, `usage`, `protocol`, `local`,
`error`). The message may be reworded; the token may not.

## Excluding files

`.dsxignore` in the synced directory, gitignore's syntax minus character classes:

```
dist/           # directories only
*.map           # any depth
/build          # anchored to the root
!dist/keep.css  # a later rule wins
```

It filters **both** directions. An ignored path is not dsx's business at all: it is not
pushed, not pulled, and not pruned from either side. (Filtering only the local scan would
make `push --prune` read "ignored here" as "deleted here".)

`.git`, `node_modules`, `.DS_Store`, the ledger and `.dsxignore` itself are always excluded
and cannot be re-included.

## Files the server will not return

`read_file` serves text only. Content that is not valid UTF-8 is stored base64 and there is
no API to read it back — no `resources` capability, no `encoding` parameter. dsx reports
these and moves on:

```
~ 6 binary file(s) skipped — read_file serves text only: assets/og.png, …
```

They upload fine, and `copy_files` moves them between projects server-side. Only the
server→disk direction is closed; the browser is the way out. Note the criterion is UTF-8
validity, not the extension — a `.png` holding ASCII is served, a `.txt` holding `\xff\xfe`
is not. [PROTOCOL.md](PROTOCOL.md) has the measurements.

## Auth

Reads Claude Code's own OAuth token. **Read, never written, never printed.** Refreshing it
here would rotate the refresh token out from under Claude Code and silently break its login,
so an expired token is reported instead — run any `claude` command to refresh.

`DSX_TOKEN` overrides the stored credential; `DSX_ENDPOINT` overrides the URL.

```bash
dsx auth     # scopes and expiry, never the token
dsx doctor   # credential source, expiry, scopes, endpoint, clock skew
```

`dsx doctor` is the first thing to run when something is wrong. It names which store the
login came from, which is what you actually need to know when dsx cannot see a login you are
certain exists.

## Development

```bash
go test -race ./...              # 509 tests
go test -tags=live -run TestLive ./...   # 20 more, against the real endpoint
go vet ./... && gofmt -l .
```

- [CLAUDE.md](CLAUDE.md) — orientation, invariants, testing discipline. Read before changing
  sync logic.
- [PROTOCOL.md](PROTOCOL.md) — the undocumented MCP contract, as measured.
- [ROADMAP.md](ROADMAP.md) — gaps, and the open question of whether this gets published.
- `reference/mcp-tools.json` — the server's own `tools/list` output, verbatim.

## Licence

MIT. See [LICENSE](LICENSE).
