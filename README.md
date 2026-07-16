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
| Binary files | **cannot be read back at all** — `read_file` is text-only, and there is no `encoding` parameter. `write_files` accepts them via base64. The asymmetry is the service's |
| `write_files` reply | `{"etags":{path:etag},"written":N,"url":...}` — a **map**, not a list |
| `needs_project_grant` | HTTP **403 with a JSON body**, not a tool error |
| `plan_token` | optional for `write_files`; **required** for `delete_files`, which refuses a project-scoped token |

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

Binaries are recorded with `binary: true` and no bytes, so later syncs stop re-asking. That
makes them tracked-but-absent, which `--prune` must never read as "deleted locally" — see
`TestPlanPush/prune_must_never_delete_a_binary...`.

## Tests

```bash
go test -race ./tools/dsx/
```

The sync decisions live in `plan.go` as pure functions, apart from the transport, because
that is where a mistake costs data. They are covered at ~95%; the entity decoder at 100%.
The network layer is verified against the live service rather than a mock — a mock would
only test my guesses about the protocol, which is exactly what kept being wrong.
