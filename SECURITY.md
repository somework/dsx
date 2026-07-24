# Security

dsx reads an OAuth credential, fetches bytes from a remote server, and writes files to your
disk. Each of those is worth taking seriously, so this document says what dsx claims about
them and how to report it when a claim turns out to be false.

## Reporting

**Do not open an issue.** Use GitHub's private vulnerability reporting:

<https://github.com/somework/dsx/security/advisories/new>

If that is unavailable to you, email **i.pinchuk.work@gmail.com** with **dsx security** in the
subject line.

Please include what you measured rather than what you inferred: the command, the input, and
what dsx did. A proof-of-concept against a project you own is worth more than a description.

This is a project maintained by one person. You will get an acknowledgement, and a fix or an
explanation of why the behaviour is intended — but no service-level promise, because one made
here would be worth nothing. Please give it a reasonable window before disclosing publicly.

## Supported versions

The latest commit on `main`, and the most recent release. Nothing older is patched.

## What dsx claims

These are the properties a report can falsify. Each is enforced by a named test; the reasoning
behind them is in [CLAUDE.md](CLAUDE.md)'s invariants.

**The OAuth token is read, never written, never printed.** dsx reads the credential Claude
Code already stored — the macOS Keychain first, then `~/.claude/.credentials.json` — and never
refreshes it, because rotating the refresh token would silently break Claude Code's own login.
It does not appear in output, in errors, or on disk. `dsx auth` and `dsx doctor` report scopes
and expiry and are tested against printing the token itself.

**The preview URL is a credential and the sync lane never lets one out.** `render_preview`
returns a short-lived `*.claudeusercontent.com` URL carrying a token scoped to the whole
project and needing no `Authorization` header — the string *is* about an hour of read access
to every file in that project. In the sync lane it is minted, used and dropped inside one
function; it reaches no report, no error and no file.

**Only two hosts are fetched.** The preview lane admits an `https` host whose `Hostname()`
ends in `.claudeusercontent.com`, or the configured endpoint's own scheme/host/port, and
nothing else. Userinfo is refused on both (`https://x.claudeusercontent.com@evil.test/` is
`evil.test`), redirects are not followed, the bearer token is deliberately not sent to the
preview host, and the body is read through a limit reader bounded by the size the listing
reported.

**Remote paths and remote bytes are untrusted input.** A server-supplied path cannot escape
the synced directory, including through a symlink, and cannot name `.git`, `node_modules` or
dsx's own bookkeeping. Comparisons fold case, because APFS does. Server-supplied text that dsx
*renders* is sanitised too — a control character in a project name would otherwise rewrite
what the terminal already showed.

**Files are not silently corrupted or silently destroyed.** Every pulled file's decoded length
is checked against the size the listing reported, and a mismatch refuses the write. Every file
lands temp-then-rename, so an interrupted run never leaves a half-written file. `--prune`
deletes only what the ledger proves was dsx's and unmodified.

## Verifying a release

Release archives carry build provenance signed by GitHub with a short-lived identity minted
for the run — there is no long-lived key anywhere in this project. Once a release exists:

```bash
gh attestation verify dsx_<version>_<os>_<arch>.tar.gz --repo somework/dsx
```

That binds the archive to this repository, this workflow and the commit it was built from.
`checksums.txt` is attested too, since it is the file people verify against and an unsigned
one answers nothing about who produced it. A checksum says an archive is internally
consistent; it does not say who built it, and for a binary that reads an OAuth credential
those are different questions.

Building it yourself is the other answer, and it needs no trust at all: `go install` compiles
from source you can read.

## In scope

- The token, or any part of it, reaching stdout, stderr, a file, an error, or the network
  anywhere other than the endpoint's `Authorization` header.
- A `serve_url` escaping the sync lane, or a fetch reaching a host outside the two allowed.
- A server-controlled path or filename causing a write outside the synced directory.
- Server-controlled content causing dsx to execute anything, or to corrupt output a caller
  parses.
- A delete or overwrite of local or remote data that the documented rules say should have been
  refused — in particular anything `--prune` removes without `--force`, or anything
  `--force-with-lease` overwrites after the server moved.
- Credential files being created or left with permissions wider than the owner.

## Not vulnerabilities

- **`dsx files preview` printing `serve_url`, and `dsx plan new` printing `plan_token`.** Both
  are scoped capabilities the tool *returns*, and printing them is what those commands are
  for — `dsx files put --plan` documents itself as taking the token off the previous command's
  stdout. Treat that output as short-lived credential output: do not log or persist it, and
  hand people `open_url` instead. Read as a blanket "never print anything token-shaped", this
  yields a false finding against `files preview`; one has been filed and withdrawn already.
- **`--force` overwriting without a precondition.** That is what the flag is for, and it is
  documented as being exactly as dangerous as git's. `--force-with-lease` is the safe middle.
- **`DSX_TOKEN` or `DSX_ENDPOINT` pointing dsx somewhere else.** Both are the operator's own
  input. The ledger refuses to sync a directory bound to a different endpoint, which is the
  guard that matters; being able to set the variable is not the finding.
- **Behaviour of the Claude Design endpoint itself.** It is Anthropic's, not this project's,
  and it is undocumented — see [PROTOCOL.md](PROTOCOL.md). Report server-side issues to
  Anthropic. A drift that makes dsx *unsafe* is in scope here; the server's own behaviour is
  not.
