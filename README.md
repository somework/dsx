# dsx — Claude Design sync

Single Go binary, stdlib only. Talks to the Claude Design MCP endpoint directly so that
file contents move **between the server and disk without passing through any model
context**. Pulling 103 files costs the agent one summary line instead of ~665k tokens.

```bash
go build -o ~/.local/bin/dsx ./tools/dsx
dsx pull  bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb design
dsx push  bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb design
dsx status bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb design
dsx help
```

Every one of the 20 MCP tools is reachable; `dsx raw <tool> '<json>'` covers anything the
named commands do not wrap.

## Why it is cheap

`list_files` returns an etag per file. One listing per directory yields the whole tree's
etags up front, so **an unchanged file costs no request at all** — not even a conditional
one. A warm `dsx pull` of a 109-file project transfers zero bytes.

`read_file`'s `if_none_match` is the fallback for the case the ledger cannot settle: it
returns `{"unchanged":true,...}` instead of a body.

## Auth

Reads Claude Code's own OAuth token from the macOS Keychain
(`security find-generic-password -s "Claude Code-credentials"`), field
`claudeAiOauth.accessToken`.

The token is **read, never written, never printed**. Refreshing it here would rotate the
refresh token out from under Claude Code and silently break its login, so an expired token
is reported and the fix is to run any `claude` command. `DSX_TOKEN` overrides the Keychain.

Note the endpoint's `WWW-Authenticate` advertises `scope="user:design:read
user:design:write"`, but the Claude Code token carries neither and is accepted anyway —
`user:mcp_servers` is what the server actually checks. Advertised scope and enforced scope
are not the same thing here.

## Protocol facts

Established by probing the live endpoint; none of this is documented publicly.

| | |
|---|---|
| Endpoint | `POST https://api.anthropic.com/v1/design/mcp` |
| Framing | plain JSON-RPC 2.0, `MCP-Protocol-Version: 2025-06-18`. Stateless — no session id |
| `resources` capability | **absent** (`resources not supported`) |
| `read_file` body | XML-wrapped, HTML-entity-escaped — **exactly `&amp; &lt; &gt;`** |
| Read cap | 256 KiB; over that the reply carries `lines="A-B" total_lines="N"` and must be windowed via `offset` |
| `write_files` reply | `{"etags":{path:etag},"written":N,"url":...}` — a **map**, not a list |
| `needs_project_grant` | HTTP **403 with a JSON body**, not a tool error |
| `plan_token` | optional for `write_files`; **required** for `delete_files`, which refuses a project-scoped token |

### Two independent gates, not one

Easy to conflate; they answer different questions and disagree in both directions.

**Write — by extension → MIME, against an allowlist.** Measured:

```
✓ png jpg jpeg gif webp ico woff2 pdf zip mp4   ✓ css js json md html txt yaml toml
✗ bin exe → unsupported content type "application/octet-stream"
~ svg     → accepted but sanitised; an invalid SVG is rejected outright
```

**Read — by content, ignoring the extension entirely.** A file whose bytes are not
valid UTF-8 is stored base64, and `read_file` serves only the text lane. Nothing in the
API reads the other lane: no `resources` capability, no `encoding` parameter.

Measured, and the results kill the obvious theories:

| bytes | name | read back? |
|---|---|---|
| real PNG | `.png` | **refused** |
| `\x00\x01\x02` inside | `.txt` | **served**, byte-exact — NUL is valid UTF-8 |
| plain ASCII | `.png` | **served** — the extension buys nothing |
| `\xff\xfe` | `.txt` | **refused** |

So the six unreadable files in this project (`og.png`, screenshots, `.thumbnail`s) are
not a category of "binary files" — they are files whose content is not valid UTF-8.
They uploaded fine and are on the server.

They are reachable without `read_file`: `copy_files` moves them project→project
server-side without reading, and a local copy can replace them via `write_files`. Only
the server→disk direction is closed; the browser is the way out.

Note the one claim this repo cannot make: a binary **push** is verified by size only
(67 bytes in, 67 on the server). Confirming it byte-for-byte would require reading it
back, which is the very thing that does not work.

### Entity decoding

The escaping is why the closing tag is an unambiguous terminator: no raw `<` or `>` can
occur in a body. Decoding must be a single left-to-right pass — an `&` produced by `&amp;`
must not be reconsidered as the start of another entity, or a file literally containing
`&lt;` (escaped to `&amp;lt;`) decodes to `<` instead of `&lt;`.

### The size assertion

`list_files` reports each file's byte size. Every pulled file is checked against it and a
mismatch **refuses the write** rather than landing a corrupt file. This is not theoretical:
it is what proved the earlier agent-driven pull had silently damaged 2 of 100 files (a
stray trailing newline, and a stray space).

## Sync model

`.dsx-state.json` in the target directory is the ledger: per path, the etag last agreed
with the server and the sha256 of the bytes held at that etag. It also pins the project id,
so a directory cannot be pushed to the wrong project.

Three-way, like any sane sync. `if_match` on every write turns a blind overwrite into a
checked one — the server refuses if the file moved. Conflicts are reported, never resolved
silently; `--force` is the only way past.

Files the server will not serve back are recorded with `binary: true` and no bytes, so later
syncs stop re-asking. That makes them tracked-but-absent, which `--prune` must never read as
"deleted locally" — see `TestPlanPush/prune_must_never_delete_a_binary...`.

Conflicts key off whether the bytes on disk still match the ones last agreed with the server,
never off the etag: an etag test only sees the server, so it cannot see the both-sides-changed
case at all. That mistake shipped once — see `TestPlanPullBothSidesChangedIsAConflict`.

## Tests

```bash
go test -race ./tools/dsx/
```

The sync decisions live in `plan.go` as pure functions, apart from the transport, because
that is where a mistake costs data. They are covered at ~95%; the entity decoder at 100%.
The network layer is verified against the live service rather than a mock — a mock would
only test my guesses about the protocol, which is exactly what kept being wrong.
