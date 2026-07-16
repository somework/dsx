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
go build -o ~/.local/bin/dsx .
```

Requires Go 1.26+ and a signed-in Claude Code on **macOS** (the token is read from the
Keychain; Linux support is not implemented yet — see [ROADMAP.md](ROADMAP.md)).

## Use

```bash
dsx projects                                   # find your project id
dsx pull  <project> design                     # server → disk
dsx push  <project> design                     # disk → server
dsx status <project> design                    # what a sync would do; transfers nothing
dsx help
```

Every one of the 20 MCP tools is reachable — `projects`, `tree`, `cat`, `put`, `rm`, `cp`,
`plan`, `preview`, `conv`, `members`, `sharing`, `prompt`. `dsx raw <tool> '<json>'` covers
anything the named commands do not wrap.

`--json` for machine-readable output, `-j N` for concurrency, `-n` for a dry run.

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
  it is what proved an earlier agent-driven pull had silently damaged 2 of 100 files.
- `--prune` deletes only what the ledger proves was ours and unmodified.

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

Reads Claude Code's own OAuth token from the macOS Keychain. **Read, never written, never
printed.** Refreshing it here would rotate the refresh token out from under Claude Code and
silently break its login, so an expired token is reported instead — run any `claude` command
to refresh. `DSX_TOKEN` overrides the Keychain; `DSX_ENDPOINT` overrides the URL.

```bash
dsx auth   # scopes and expiry, never the token
```

## Development

```bash
go test -race ./...
```

- [CLAUDE.md](CLAUDE.md) — orientation, invariants, testing discipline. Read before changing
  sync logic.
- [PROTOCOL.md](PROTOCOL.md) — the undocumented MCP contract, as measured.
- [ROADMAP.md](ROADMAP.md) — gaps, and the open question of whether this gets published.
- `reference/mcp-tools.json` — the server's own `tools/list` output, verbatim.
