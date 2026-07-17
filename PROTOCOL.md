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

### Where the credential lives

Read out of the shipped `claude` binary (v2.1.211), not guessed. Its storage layer is

```js
qc() = Kac(Bwi, MBn)      // "keychain-with-plaintext-fallback"
```

— the macOS Keychain first, a plaintext JSON file second, **on every platform**, with no
check on `process.platform`. On Linux the keychain lane simply fails (`security(1)` is a
macOS binary) and the file answers. Both backends serialise the same object, so both hold
the same `{"claudeAiOauth":{…}}`.

dsx walks the same chain, in the same order:

| Where | Shape |
|---|---|
| `DSX_TOKEN` env | raw token; overrides everything, including `dsx auth` |
| Keychain (darwin only), service **computed** — matched on the service alone, [deliberately not on the account](auth_darwin.go) | JSON, field `claudeAiOauth.accessToken` |
| `<config-dir>/.credentials.json`, mode `0600` | the same JSON |

**The service name is not a constant.** Claude Code builds it as

```
`Claude Code${hs().OAUTH_FILE_SUFFIX}-credentials${dirSuffix}`
```

where `OAUTH_FILE_SUFFIX` is `""` for the production build (`-local-oauth` and
`-custom-oauth` exist for Anthropic's internal ones), and `dirSuffix` is empty for the
default config dir and `-<sha256(configDir)[:8]>` otherwise — so several logins can share
one keychain. dsx hardcoded the plain name until 2026-07-17, which read the wrong item, or
none, for anyone who sets `CLAUDE_CONFIG_DIR`.

The config dir resolves as, in order: `CLAUDE_SECURESTORAGE_CONFIG_DIR` if *defined* (empty
means the default), else `CLAUDE_CONFIG_DIR`, else `~/.claude`. Three independent places in
the binary agree on `CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude")`.

**Measured** on a real install: the keychain item is service `Claude Code-credentials`,
account `$USER`, with neither env var set — i.e. the default branch of the formula, confirmed.

**Not measured:** the file lane. No Linux machine was available, so dsx has never read a
`.credentials.json` that Claude Code actually wrote. The path, the mode and the payload are
read out of Claude Code's own code rather than guessed at from a bare string, but that is a
derivation, and it is the one thing in this document that has not met the real thing.

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

### Windowed reads carry the server's prose inside the body

The nastiest thing in this file, and it silently corrupted files until 2026-07-17.

A window that stops short of the end ends with a line **the server wrote**, inside the body,
after the content:

```
line 003854 — …\n
\n
…[+54400 bytes truncated at read_file's 256 KiB cap — the body ends at a complete line; continue with offset=3856]\n
</untrusted-project-content>
```

Measured on a 316,540-byte file (4,655 lines), which came back as `lines="1-3855"` then
`lines="3856-4655"`:

- The notice is on **every window that stops short of `total_lines`**, and **absent from the
  final one**.
- It sits after the content's own trailing newline, separated by one more newline, and the
  framing newline before the close tag is stripped as usual — so it lands squarely inside
  `Body`.
- "the body ends at a complete line" is true, and it answers a question this document used to
  leave open: **windows need no separator between them.** Concatenating the bodies is correct
  *once the notice is removed*.

Concatenating window bodies verbatim therefore splices that sentence into the middle of the
user's file. `dsx pull` was saved by the size assertion — the decoded length disagreed with
`list_files` and the write was refused, so large files simply would not pull — but `dsx cat`
wrote it out without a word.

dsx now strips it, and **refuses** any windowed body whose trailer it cannot account for. If
the server rewords the notice, dsx breaks loudly on files over 256 KiB. That is the trade:
loud is recoverable, quiet is not.

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

A **stale `if_match`** comes back as a *tool error* whose text is JSON — not prose, unlike
every other tool error. Measured 2026-07-17:

```json
{"conflicts":[{"path":"a.css","etag":"1784268009093847",
               "current_content":"<untrusted-project-content path=\"a.css\" etag=\"…\">\nhello\n\n</untrusted-project-content>"}],
 "message":"write_files: refused — the user (or another writer) changed one or more of these files since your if_match etag. Nothing was written. Re-base on current_content (wrapped the same way read_file wraps …) and retry with the new etag as if_match."}
```

Three things matter here:

- **"Nothing was written."** The refusal is atomic across the batch, so dsx can report it as a
  plain conflict rather than as a partial write.
- `conflicts[].path` and `.etag` are structured, so dsx can name the files and exit 3 instead
  of degrading the one race every `if_match` exists to catch into a generic failure.
- `current_content` arrives wrapped exactly as `read_file` wraps a body — same escaping, same
  framing — so a rebase needs the same decoder.

`needs_project_grant` arrives as **HTTP 403 with a JSON body**, not as a tool error:

```json
{"error":"needs_project_grant","project_id":"…","prompt":"Allow Claude to edit this project? …"}
```

It is recoverable without a browser: `finalize_plan` mints a `plan_token` that authorises
the same write. dsx does this automatically.

`finalize_plan`:

- `scope:"paths"` (default) — declare `writes`/`deletes`; token lasts ~15 min; reply carries
  `base_etags`
- `scope:"project"` — any path for ~4 h, **never** deletes; `writes`/`deletes` must be omitted.
  Reply:

  ```json
  {"expires_at":1784262307,"plan_token":"plan_eyJ…","project_id":"…","scope":"project"}
  ```

  `expires_at` is **unix seconds as a number**, not a string. The token is a JWT-shaped blob
  whose payload names the project and the scope.

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

| limit | value | evidence |
|---|---|---|
| `read_file` | 256 KiB per call | measured; asserted live |
| `write_files` | 256 entries per call | **uncorroborated.** The schema states no batch limit and types `files` as an unbounded array; dsx sends at most 128 (`maxBatchFiles`) and so can never discover the ceiling. The real constraint is dsx's own. |
| `finalize_plan` globs | 3 wildcards per pattern, 256 entries | **uncorroborated, and probably wrong.** `reference/mcp-tools.json` contains no glob or wildcard anywhere; `finalize_plan` types `writes`/`deletes` as literal paths and normalises them like storage does. dsx never sends a pattern. Treat this row as a guess wearing a table's authority until it is re-probed. |
| path-scoped token | ~15 min | probed once; not asserted live |
| project-scoped token | ~4 h | asserted live (order of magnitude only) |

## Errors

| Shape | Meaning |
|---|---|
| tool result with `isError:true` | server-side tool failure; text is plain prose |
| HTTP 403 + `needs_project_grant` | recoverable via `finalize_plan` |
| HTTP 401 | token rejected; user runs any `claude` command to refresh |
| `read file: file not found` | missing path — note the wording differs from `read_file:` prefixed errors |

Beware when testing: `2>&1 >/dev/null` sends stderr to the *old* stdout. That mistake once
made a failed write look like a read refusal and nearly invented a false protocol fact.

## Re-probing this document

Much of this document is asserted by the live suite, and a good deal of it is not. The
difference matters, because of what the next paragraph promises:

```bash
go test -tags=live -run TestLive ./...     # 17 tests, 20 with subtests, ~90 s
```

**Pinned live:** the envelope framing and entity decoding, the size agreement, windowed reads
and their truncation notice, `if_none_match`, `write_files`' map reply, `if_match` (stale, `"0"`,
and the structured conflict), `needs_project_grant` as a 403 and its `finalize_plan` recovery,
delete refusing a project-scoped token, binary-by-content in both directions, `resources` still
unsupported, `tools/list` still naming every tool dsx wraps, and an end-to-end push/pull round
trip through the real sync engine.

**Not pinned live, and resting on a one-off probe or on the schema:** the whole **Auth**
section (unit-tested only — and its file lane has never met a real file at all), `copy_files`,
the **Limits** table, the accepted half of the write allowlist (only `.bin`'s refusal is probed,
and softly), the `read file: file not found` wording, and the `prompts`/`listChanged`
capabilities.

The blanket claim that used to stand here — "every claim above is asserted" — was false, and
falsely reassuring in the one document where that is most expensive: it invited a reader to
treat a green suite as proof of the whole thing.

It writes only to `.dsx-selftest*` paths in the test project, removes them, and confirms the
project's file count is back where it started. There is no `delete_project` tool, so it never
creates one — `TestLiveRefusesToCreateProjects` enforces that by reading its own source.

If one of those tests fails, **this document is what is wrong**, not the test. Re-probe, then
correct both.

The tests are worth more than they look: three facts here were guessed wrong before they were
measured (`write_files` returns a map; `needs_project_grant` is a 403; binary-ness is by
content), and the truncation notice above was found only because a live test read a file
larger than the cap. A mock would have agreed with every wrong guess.
