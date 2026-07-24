<!--
Read CONTRIBUTING.md first if this is your first change here. The short version
is below; delete any section that does not apply.
-->

## What this changes

<!-- One or two sentences. What is different after this lands? -->

## Why

<!-- What was wrong, or what became possible. If it fixes a defect, describe the
     failure: the inputs, and what dsx did that it should not have. -->

## How it was verified

- [ ] `go test -race ./...` passes
- [ ] `go vet ./... && go vet -tags=live ./... && gofmt -l .` is clean
- [ ] `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...` is clean
- [ ] `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` reports nothing
- [ ] The new test was watched **fail** before the fix, and pass after it
- [ ] Live suite run (`DSX_LIVE_PROJECT=… go test -tags=live -run TestLive ./...`), or not applicable

<!-- If this touches anything the server answers, say which claim in PROTOCOL.md
     it rests on and whether you re-measured it. A guess that reads plausibly is
     how three protocol facts were wrong before. -->

## Invariants

<!-- CLAUDE.md numbers them, and each cost something real to learn. If this
     change relaxes one, name it and say what you learned that the invariant did
     not already know. If it touches none, say so — that is a normal answer. -->

Touches invariants:

## Dependencies

- [ ] `go.mod` is unchanged — dsx is stdlib-only, and that is a hard constraint
