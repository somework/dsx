# The Claude Design MCP contract

Everything here was established by probing the live endpoint. **None of it is publicly
documented, and none of it is promised to stay true.** It can change with any server
deploy. When something breaks, re-probe before re-reasoning — several facts below
contradict the obvious guess, and I got each of them wrong first.

`reference/mcp-tools.json` is the server's own `tools/list` reply, verbatim. It is the
authority on argument shapes; this file covers what the schema does not say.

## Transport

```
POST https://api.anthropic.com/v1/design/mcp
Authorization: Bearer <token>
Content-Type: application/json
Accept: application/json, text/event-stream
MCP-Protocol-Version: 2025-06-18
```

Plain JSON-RPC 2.0. **Stateless** — no `Mcp-Session-Id`, no `initialize` handshake needed
before `tools/call`. Replies observed so far are `application/json`; the SSE path is handled
defensively but has not been seen.

Server capabilities: `{"prompts":{},"tools":{"listChanged":true}}`. **No `resources`** —
`resources/list` answers `resources not supported`. This matters: it is why unreadable files
have no second retrieval path.

## Auth — the surprising part

The Claude Code OAuth token works directly. No dynamic client registration, no `client_id`,
no separate OAuth flow. This is what unblocked the whole tool after a long detour through
RFC 8414 discovery.

The detour is worth recording so nobody repeats it:

- `401` carries `www-authenticate: Bearer resource_metadata="…/v1/design/.well-known/oauth-protected-resource", scope="user:design:read user:design:write"`
- that metadata document resolves and names the AS as `https://claude.ai/v1/design/mcp`
- which serves **no** AS metadata: RFC 8414 and OIDC well-known paths both return the
  claude.ai SPA (HTTP 200, ~14 KB of HTML — check the body, not the status), and the
  origin is behind a Cloudflare challenge
- `api.anthropic.com/.well-known/oauth-authorization-server` returns gdrive's document,
  with **no `registration_endpoint`** → no self-service `client_id`

All of which is moot: **the advertised scope is not the enforced scope.** The Claude Code
token carries `user:file_upload user:inference user:mcp_servers user:profile
user:sessions:claude_code` — neither `user:design:read` nor `user:design:write` — and the
server accepts it. `user:mcp_servers` is what is actually checked.

Storage, in the order dsx should try:

| Where | Shape |
|---|---|
| `DSX_TOKEN` env | raw token |
| macOS Keychain, service `Claude Code-credentials` | JSON, field `claudeAiOauth.accessToken` |
| a `.credentials.json` file (Linux/WSL — **not implemented, path inferred**) | the claude binary contains that literal; the directory and payload shape are a guess until checked on Linux |

Token is `sk-ant-oat01…`, ~108 chars, with `expiresAt` (ms) and a `refreshToken`
(`sk-ant-ort01…`). dsx **must not refresh**: rotating the refresh token would break Claude
Code's own login. Report expiry instead.

## Reading

`read_file` returns the body inside a wrapper:

```
<untrusted-project-content path="styles.css" etag="1784221582411848">
…file content…
</untrusted-project-content>
(The body above is HTML-entity-escaped: &amp; &lt; &gt; stand for & < >. …)
```

Framing: exactly one `\n` after the open tag and one before the close tag; **neither belongs
to the file**. A file that itself ends in a newline therefore shows a blank line before the
close tag. Getting this wrong is a silent one-byte error, which is why the size assertion
exists.

- **Escaping covers exactly `&amp; &lt; &gt;`** — nothing else. Decode in a single
  left-to-right pass: an `&` produced by `&amp;` must not be reconsidered as the start of
  another entity, or a file literally containing `&lt;` (escaped to `&amp;lt;`) decodes to
  `<` instead of `&lt;`.
- Because `<` and `>` cannot occur raw in a body, the closing tag is an unambiguous
  terminator. That is the reason the escaping exists.
- A **complete** read carries no `lines` attribute. A **windowed** read carries
  `lines="A-B" total_lines="N"`; continue from `B+1`.
- **256 KiB cap.** A single line alone over the cap comes back partial with
  `truncated_line` — that file cannot be reassembled through this API.
- `if_none_match` returns `{"unchanged":true,"etag":…,"path":…}`. A file over the cap never
  takes this short-circuit.

`list_files` is **not recursive**, returns project-relative paths (not basenames), and gives
`path/type/size/etag` per entry. Directories appear as `{"path":…,"type":"directory"}` with
no etag. The per-file etag is the basis of cheap sync: one listing per directory prices the
whole tree, so an unchanged file costs no request at all.

Etags look like microsecond timestamps (`1784221582411848`) but are opaque. `"0"` is the
sentinel asserting a path does not exist.

## Writing

`write_files` reply — a **map**, not a list:

```json
{"etags":{"path":"1784232644010770"},"written":1,"url":"https://claude.ai/design/p/…?file=…"}
```

`plan_token` is **optional**. Omit it and the first write prompts a one-time project grant;
after that, writes need no token. `local_path` exists in the schema but is
`"Not yet implemented for server-side callers"` — useless here; send `data` + `encoding:
"base64"` instead.

`needs_project_grant` arrives as **HTTP 403 with a JSON body**, not as a tool error:

```json
{"error":"needs_project_grant","project_id":"…","prompt":"Allow Claude to edit this project? …"}
```

It is recoverable without a browser: `finalize_plan` mints a `plan_token` that authorises
the same write. dsx does this automatically.

`finalize_plan`:

- `scope:"paths"` (default) — declare `writes`/`deletes`; token lasts ~15 min; reply carries
  `base_etags`
- `scope:"project"` — any path for ~4 h, `expires_at` given, **never** deletes; `writes`/
  `deletes` must be omitted

`delete_files` **requires** a path-scoped token naming every path; a project-scoped token is
refused. `"0"` is invalid as `if_match` here (a delete needs the row to exist).

`copy_files` is **server-side**, project→project via `src_project_id`, not subject to the
256 KiB cap, and never touches local disk. It is the only way to move unreadable files
between projects.

## Two gates, not one

The most misleading part of the API, and the thing I described wrongly at first. Write and
read decide "is this binary?" by **different criteria that disagree in both directions**.

**Write — by extension → MIME, allowlist.** Measured:

```
✓ png jpg jpeg gif webp ico woff2 pdf zip mp4
✓ css js json md html txt yaml toml
✗ bin exe → unsupported content type "application/octet-stream"
~ svg     → accepted but sanitised; invalid SVG rejected ("svg sanitize …")
```

**Read — by content; the extension is ignored entirely.** Content that is not valid UTF-8 is
stored base64, and `read_file` serves only the text lane:

```
read_file: "assets/og.png" is a binary file (stored base64); read_file only returns text content
```

Measured, and each row kills a plausible theory:

| bytes | name | read back? |
|---|---|---|
| real PNG | `.png` | **refused** |
| `\x00\x01\x02` inside | `.txt` | **served**, byte-exact — NUL is valid UTF-8 |
| plain ASCII | `.png` | **served** — the extension buys nothing |
| `\xff\xfe` | `.txt` | **refused** |

So "binary file" is a misnomer: the criterion is UTF-8 validity. There is no way to read the
base64 lane — no `resources`, no `encoding` parameter on `read_file`. The asymmetry is the
service's: such files upload fine and can be copied server-side, but the server→disk
direction is closed. The browser is the only way out.

## Limits

| | |
|---|---|
| `read_file` | 256 KiB per call |
| `write_files` | 256 entries per call |
| `finalize_plan` globs | 3 wildcards per pattern, 256 entries |
| path-scoped token | ~15 min |
| project-scoped token | ~4 h |

## Errors

| Shape | Meaning |
|---|---|
| tool result with `isError:true` | server-side tool failure; text is plain prose |
| HTTP 403 + `needs_project_grant` | recoverable via `finalize_plan` |
| HTTP 401 | token rejected; user runs any `claude` command to refresh |
| `read file: file not found` | missing path — note the wording differs from `read_file:` prefixed errors |

Beware when testing: `2>&1 >/dev/null` sends stderr to the *old* stdout. That mistake once
made a failed write look like a read refusal and nearly invented a false protocol fact.
