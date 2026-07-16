package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Exit codes. dsx's primary caller is a program, and a program branches on
// these numbers: they are contract. Add new ones, never renumber the old.
//
//	0  the command did what it was asked
//	1  it failed for a reason dsx has no better label for
//	2  the invocation was wrong; running it again will not help
//	3  a conflict: both sides hold work, so a human must choose
//	4  the network or the server faulted; a retry may well succeed
//	5  the token is missing, expired, or rejected; run any `claude` command
const (
	exitOK        = 0
	exitFailure   = 1
	exitUsage     = 2
	exitConflict  = 3
	exitTransport = 4
	exitAuth      = 5
)

// errKind is the stable token an agent matches on in --json output. It is
// deliberately coarser than the message: the message may be reworded, the
// token may not.
type errKind string

const (
	kindFailure   errKind = "error"
	kindUsage     errKind = "usage"
	kindConflict  errKind = "conflict"
	kindTransport errKind = "transport"
	kindAuth      errKind = "auth"
	kindProtocol  errKind = "protocol"
	kindLocal     errKind = "local"
)

// exitCode maps a kind onto the process status. Kinds that share exitFailure
// still differ in --json, so a caller loses nothing by the collapse: the codes
// exist to separate the three responses that differ (fetch a human, retry,
// re-authenticate) from everything that just failed.
func (k errKind) exitCode() int {
	switch k {
	case kindUsage:
		return exitUsage
	case kindConflict:
		return exitConflict
	case kindTransport:
		return exitTransport
	case kindAuth:
		return exitAuth
	default:
		return exitFailure
	}
}

// dsxError is a failure carrying the classification a caller acts on.
type dsxError struct {
	Kind  errKind
	Msg   string
	Paths []string
	Err   error
}

func (e *dsxError) Error() string {
	var sb strings.Builder
	sb.WriteString(e.Msg)
	if e.Err != nil {
		if e.Msg != "" {
			sb.WriteString(": ")
		}
		sb.WriteString(e.Err.Error())
	}
	if len(e.Paths) > 0 {
		sb.WriteString(": " + strings.Join(e.Paths, ", "))
	}
	return sb.String()
}

func (e *dsxError) Unwrap() error { return e.Err }

// classify recovers the classification from anywhere in the wrap chain. Every
// site on the way up wraps with %w, so the label survives the ascent; anything
// never labelled degrades to a plain failure rather than to success.
func classify(err error) *dsxError {
	if err == nil {
		return nil
	}
	var de *dsxError
	if errors.As(err, &de) {
		return de
	}
	return &dsxError{Kind: kindFailure, Msg: err.Error(), Err: err}
}

func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	return classify(err).Kind.exitCode()
}

// conflictError reports paths dsx refused to touch because both sides hold
// work. Paths are sorted so a caller diffing two runs sees a stable list.
func conflictError(paths []string, hint string) *dsxError {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return &dsxError{Kind: kindConflict, Msg: hint, Paths: sorted}
}

func usageError(form string) *dsxError {
	return &dsxError{Kind: kindUsage, Msg: "usage: dsx " + form}
}

// errorPayload is the --json error envelope. Field names are contract.
type errorPayload struct {
	Error   errKind  `json:"error"`
	Message string   `json:"message,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// renderError produces the single stderr line for a failed command: JSON an
// agent can parse, or prose a person can read.
func renderError(err error, asJSON bool) string {
	de := classify(err)
	if de == nil {
		return ""
	}
	if !asJSON {
		return "dsx: " + de.Error()
	}
	msg := de.Msg
	if de.Err != nil {
		if msg != "" {
			msg += ": "
		}
		msg += de.Err.Error()
	}
	b, marshalErr := json.Marshal(errorPayload{Error: de.Kind, Message: msg, Paths: de.Paths})
	if marshalErr != nil {
		// Falling back to prose beats emitting nothing: the caller still learns
		// that the command failed, and the exit code is unaffected.
		return "dsx: " + de.Error()
	}
	return string(b)
}

// jsonRequested reports whether the caller asked for machine-readable output.
//
// The error renderer runs outside every FlagSet -- errors escape before, during
// and after flag parsing -- so it cannot ask the flag package. Scanning argv is
// what lets a failure honour --json wherever the flag landed.
func jsonRequested(args []string) bool {
	for _, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		if name != "--json" && name != "-json" {
			continue
		}
		if !hasValue {
			return true
		}
		// The flag package honours -json=false; a scan that ignored the value
		// would hand JSON to a caller who explicitly turned it off.
		if v, err := strconv.ParseBool(value); err == nil {
			return v
		}
		return true
	}
	return false
}
