# dsx — Claude Design sync

Single Go binary, stdlib only. Moves files between a [Claude Design](https://claude.ai/design)
project and a local directory **without the contents passing through any model's context**.

The same pull, done by agents reading each file, costs ~665k tokens. dsx costs one line:

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
dsx project ls                                 # find your project id
dsx clone <project> design                     # first pull into a new directory
cd design && dsx push                          # disk → server
dsx fetch                                      # record what the server holds — status answers from this
dsx status                                     # what changed here, from disk alone; no network call
dsx push --force-with-lease                    # overwrite, but only what the last fetch still accounts for
dsx help
```

Only `clone` and `pin` name a project; `unpin` may name a directory. Every other sync verb
takes no argument at all: it acts on the tree you are standing in, finding the ledger by
walking up the way `git status` does. `dsx -C <dir> <command>` moves first, exactly like
git's.

```bash
cd design/components && dsx pull               # syncs the whole tree, not the subdirectory
dsx -C design status                           # …or act on it from anywhere
dsx files tree                                 # and files cat, likewise
```

`files tree` and `files cat` read that binding too, so they take the project from the tree
you are standing in when you omit it — from any depth, and a named project still wins. The
writes do not: `files put`, `files rm` and `files cp` always name their project, because the
working directory must not choose the target of a destructive act.

`clone` needs an empty directory, and it is the only way to make one: `pull` no longer creates
its target. To sync into a directory that already holds files, bind it first with
`dsx pin <project> <dir>`, then pull. Files the server does not have are left alone. A path
present on both sides with no ledger entry for it collides: the first pull writes nothing at
all and reports every collision (exit 3).

```bash
dsx pin <project> design                       # bind an existing directory — no round trip
cd design
dsx fetch                                      # download + hash the files already there, once
dsx pull                                       # bytes that match land as verified, not blocked
dsx diff                                       # classify every path: same, local-only, remote-only, differs
```

`pin` refuses to rebind, so a mistyped id would otherwise be repairable only by deleting `.dsx`
by hand. `dsx unpin <dir>` is the way back, and it needs no credential — the state you are in
when you want out of a binding may well be an expired token. It releases only a binding that
has synced nothing: once the ledger tracks a file, dropping it would make every tracked path
untracked and leave the next `push --force` writing with no etag precondition at all, so unpin
refuses and says so.

`fetch` proves identity by downloading and hashing, never by etag alone, and only the paths it
verified are exempted from the collision — they stay untracked (`fetch` records a cache, not
the ledger), but they no longer block the first pull. Whatever it could not verify still
collides — resolve it by hand, or pass `--force` to take the server's copy.

`dsx diff` never prints a hunk — bytes still do not pass through a model's context. A fresh
`fetch` baseline proves a path `same` with no download; every other present-both path is
downloaded to classify. `--out <dir>` materialises the remote side of `differs` paths into an
empty directory so `diff -ru` does the work locally.

Every MCP tool is reachable — `project ls`, `files tree`, `files cat`, `files put`,
`files rm`, `files cp`, `plan new`, `files preview`, `conv get`, `member ls`,
`project sharing`, `comment ls`, `comment ack`, `skill`, `prompt`. `dsx raw <tool> '<json-args>'`
covers anything the named commands do not wrap.

`comment ls` reads the pin-anchored feedback people leave in Claude Design; its reply carries a
`server_time` watermark to pass back as `--since`, so a second call returns only what changed.
`comment ack` clears the queue flag on ids you have actually handled. `skill` fetches the
server's own design-quality guidance (`hifi-design`, `frontend-design`).

`--json` on every command, `-j N` for concurrency, `-n` for a dry run.



## Why it is cheap

`list_files` returns an etag per file, so one listing per directory prices the whole tree up
front: **an unchanged file costs no request at all** — not even a conditional one. A warm
pull of a 109-file project transfers zero bytes and takes ~2 s.

Contents go server→disk directly. Nothing a model reads scales with file size.

## Safety

The sync is three-way against `.dsx/state.json`, a ledger recording — per path — the etag
last agreed with the server and the sha256 of the bytes held at that etag. It also pins the
project id, so a directory cannot be pushed to the wrong project.

- Every write carries `if_match`: the server refuses it if the file moved. Blind overwrites
  are not possible without `--force`.
- Conflicts are reported, never resolved silently — including the case where **both** sides
  changed, which an etag comparison alone cannot see.
- `dsx fetch` proves a local file matches the server by downloading and hashing it, never by
  trusting an etag alone; a proven match is reported `verified`, not silently adopted into the
  ledger, so it never becomes eligible for `--prune` to delete.
- Every pulled file's decoded length is checked against the size `list_files` reported; a
  mismatch refuses the write rather than landing a corrupt file. Not theoretical: it caught
  an earlier agent-driven pull that had silently damaged 2 of 100 files, and stopped a
  second corruption bug from reaching disk.
- `--prune` deletes only what the ledger proves was ours and unmodified.

### Upgrading a directory synced by an older dsx

An earlier, never widely-distributed build of dsx kept the ledger at `.dsx-state.json`, in the
directory's root. This dsx does not read that path, and there is no automatic migration — if
you have one, move it by hand, once:

```bash
mkdir .dsx && mv .dsx-state.json .dsx/state.json
```

That is the whole migration: the ledger's shape on disk did not change, only its location.

## For agents

dsx's primary caller is a program, so the interface is built for one.

**Exit codes** name the responses that differ:

| code | meaning |
|---|---|
| `0` | it did what it was asked |
| `1` | it failed |
| `2` | the invocation was wrong; running it again will not help |
| `3` | **conflict** — both sides hold work; a human must choose |
| `4` | **transport** — the network or the server faulted; a retry may succeed |
| `5` | **auth** — run any `claude` command, then retry |

A dry run carries the same exit code as the run it previews: `pull -n` on a conflicted tree exits
`3`, exactly as `pull` would. The code states something about the tree — both sides hold work — not
about whether this invocation moved bytes, which is why `pull -q` prints nothing and still exits `3`.
`dsx status` and `dsx diff` are reports and exit `0` whatever they find — read their `--json` for the
list.

**`--json`** makes stdout exactly one JSON document. Tools that already answer in JSON pass
through untouched; the rest are wrapped as `{"text":…}`.

Which half owns the shape matters. Where dsx computes the answer itself, the payload is dsx's
and stable. Where it relays a tool result — anything printed through `JSONSafe`, and the raw
`tools/list` reply behind `dsx tools` — the shape is the server's: dsx neither validates nor
pins it, and it can change without a dsx release. Only the one-document guarantee spans both.

Without `--json`, a reply dsx has measured is drawn for a person; anything else is printed as
it arrived. `conv get` is the one place that draws *less*: `get_conversation` caps at 256 KiB
with no `offset`, so a busy project answers with a quarter of a megabyte of transcript cut
mid-JSON whose one actionable line — the `chat_id` to narrow to — the server appends at the
end. dsx prints that line and withholds the cut body.

`conv get` is also the one command whose `--json` is **dsx's shape, not the server's** — it
has to be, because the reply is a tag, a body and a notice rather than a JSON document, so the
alternative was not the server's bytes but the `{"text":…}` wrapper, in which `jq` can reach
the whole transcript only as one string:

```json
{"project_id":"…","untrusted":true,
 "transcript":{"chats":{…}},
 "truncated":{"bytes_dropped":1274645,"narrow_to":["ffffffff-…"],"tail_unparsed":2012}}
```

Exactly one of three keys carries the conversation, and which one is the answer to "how much of
this is real":

| key | meaning |
|---|---|
| `transcript` | the server sent it whole; it parsed as-is |
| `partial` | it was cut, and dsx trimmed back to the last **complete** element so everything present is byte-complete |
| `body` | it was cut somewhere no honest trim exists; raw text, parse it yourself |

`partial` is deliberately not spelled `transcript`: a reader keying off the wrong one would
believe it holds the whole conversation. The trim only ever shortens — the sole bytes dsx adds
are the closing brackets, and their types come from the JSON lexer's own stack. It cuts back to
the last whole element of the outermost open array rather than to the last thing that happened
to parse, because the latter leaves the final message holding a tool call whose `input` was
sliced off, and `…toolCall.input` then answers `null` with nothing to distinguish "there was
none" from "it was truncated".

The two loss counters are different losses and neither can stand for the other:
`bytes_dropped` is what the **server** never sent, `tail_unparsed` is the trailing fragment
**dsx** discarded to make the rest parse.

So on a capped project:

```console
$ dsx conv get bbbbbbbb-… --json | jq -r '.partial.chats[] | "\(.title)  \(.messages|length) messages"'
Direction confirmed  7 messages

$ dsx conv get bbbbbbbb-… --json | jq -r '.truncated.narrow_to[]'
ffffffff-ffff-4fff-8fff-ffffffffffff
```

`untrusted` is always true and is the only marker left once the wrapper is gone: a transcript
is user-authored data that may read like instructions.

Errors go to stderr:

```json
{"error":"conflict","message":"local differs from the server","paths":["a.css","b.css"]}
```

`error` is a stable token (`conflict`, `transport`, `auth`, `usage`, `protocol`, `local`,
`error`). The message may be reworded; the token may not. `error` is the catch-all for
anything with no distinct remedy — including a server-side tool refusal (`isError:true`),
reported verbatim behind the tool name so `dsx raw <tool>` reproduces it.

`dsx help --json` answers with two keys: `commands`, the registry — one object per command
carrying its group, its noun and section where it has them, name, invocation form,
description and aliases — and `flags`, the block documenting the flags a Form does not spell
(`--if-match`, `--plan`, `--json`, `-q`, `-n`, `-j N`) with the commands each reaches, plus
the exit codes and env vars. Neither is the prose `dsx help` prints. `dsx <noun> --json` is
the same registry narrowed to one noun: `{"noun":…,"desc":…,"verbs":[…]}`, the verbs in the
order the noun's help prints them.

## Excluding files

`.dsxignore` in the synced directory, gitignore's syntax minus character classes:

```
# a comment must be on its own line
dist/
*.map
/build
!dist/keep.css
```

`dist/` is directories only, `*.map` matches at any depth, `/build` is anchored to the root,
and a later `!` rule wins. A `#` starts a comment only at the start of a line — a trailing
`# like this` is part of the pattern, exactly as in gitignore.

It filters **both** directions. An ignored path is not dsx's business at all: it is not
pushed, not pulled, and not pruned from either side. (Filtering only the local scan would
make `push --prune` read "ignored here" as "deleted here".)

Always excluded and not re-includable by any `!` rule: VCS metadata (`.git`, `.svn`, `.hg`),
`node_modules`, `.DS_Store`, and dsx's own bookkeeping — `.dsx/` (the ledger and the fetch
baseline), `.dsxignore`, `.dsx-case-probe`, and `.dsx-state.json` (a pre-`.dsx/` ledger name;
kept ignored so a leftover from that older location, or its write-in-progress temp file, is
never mistaken for one of your own). `builtinIgnores` in `ignore.go` is the source of truth.

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
go test -race ./...
go test -tags=live -run TestLive ./...   # against the real endpoint
go vet ./... && gofmt -l .
```

- [CLAUDE.md](CLAUDE.md) — orientation, invariants, testing discipline. Read before changing
  sync logic.
- [PROTOCOL.md](PROTOCOL.md) — the undocumented MCP contract, as measured.
- `reference/mcp-tools.json` — the server's own `tools/list` output, content unchanged, indented
  and key-sorted so a schema change reads as a small diff.

## Licence

MIT. See [LICENSE](LICENSE).
