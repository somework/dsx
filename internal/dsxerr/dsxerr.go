// Package dsxerr is the error taxonomy and the exit codes -- the contract with
// an agent calling dsx.
//
// It is the module's one leaf: every other package reports failures through it,
// and it imports nothing of dsx's own. That is why it is a package at all. It
// holds no policy about what went wrong, only about how a caller is told.
package dsxerr

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
	ExitOK        = 0
	ExitFailure   = 1
	ExitUsage     = 2
	ExitConflict  = 3
	ExitTransport = 4
	ExitAuth      = 5
)

// Kind is the stable token an agent matches on in --json output. It is
// deliberately coarser than the message: the message may be reworded, the
// token may not.
type Kind string

const (
	KindFailure   Kind = "error"
	KindUsage     Kind = "usage"
	KindConflict  Kind = "conflict"
	KindTransport Kind = "transport"
	KindAuth      Kind = "auth"
	KindProtocol  Kind = "protocol"
	KindLocal     Kind = "local"
)

// ExitCode maps a kind onto the process status. Kinds that share ExitFailure
// still differ in --json, so a caller loses nothing by the collapse: the codes
// exist to separate the three responses that differ (fetch a human, retry,
// re-authenticate) from everything that just failed.
func (k Kind) ExitCode() int {
	switch k {
	case KindUsage:
		return ExitUsage
	case KindConflict:
		return ExitConflict
	case KindTransport:
		return ExitTransport
	case KindAuth:
		return ExitAuth
	default:
		return ExitFailure
	}
}

// Error is a failure carrying the classification a caller acts on.
type Error struct {
	Kind  Kind
	Msg   string
	Paths []string
	Err   error
}

func (e *Error) Error() string {
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

func (e *Error) Unwrap() error { return e.Err }

// Classify recovers the classification from anywhere in the wrap chain. Every
// site on the way up wraps with %w, so the label survives the ascent; anything
// never labelled degrades to a plain failure rather than to success.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	// Msg is left empty on purpose. Both renderers join Msg and Err, so setting
	// both to the same error made every unclassified failure say everything
	// twice -- in prose and in --json alike.
	return &Error{Kind: KindFailure, Err: err}
}

func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	return Classify(err).Kind.ExitCode()
}

// Conflict reports paths dsx refused to touch because both sides hold work.
// Paths are sorted so a caller diffing two runs sees a stable list.
func Conflict(paths []string, hint string) *Error {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return &Error{Kind: KindConflict, Msg: hint, Paths: sorted}
}

func Usage(form string) *Error {
	return &Error{Kind: KindUsage, Msg: "usage: dsx " + form}
}

// payload is the --json error envelope. Field names are contract.
type payload struct {
	Error   Kind     `json:"error"`
	Message string   `json:"message,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// Render produces the single stderr line for a failed command: JSON an agent
// can parse, or prose a person can read.
func Render(err error, asJSON bool) string {
	de := Classify(err)
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
	b, marshalErr := json.Marshal(payload{Error: de.Kind, Message: msg, Paths: de.Paths})
	if marshalErr != nil {
		// Falling back to prose beats emitting nothing: the caller still learns
		// that the command failed, and the exit code is unaffected.
		return "dsx: " + de.Error()
	}
	return string(b)
}

// JSONRequested reports whether the caller asked for machine-readable output.
//
// The error renderer runs outside every FlagSet -- errors escape before, during
// and after flag parsing -- so it cannot ask the flag package. Scanning argv is
// what lets a failure honour --json wherever the flag landed.
func JSONRequested(args []string) bool {
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
