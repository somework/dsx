package dsxerr

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

const (
	ExitOK        = 0
	ExitFailure   = 1
	ExitUsage     = 2
	ExitConflict  = 3
	ExitTransport = 4
	ExitAuth      = 5
)

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

func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return de
	}

	return &Error{Kind: KindFailure, Err: err}
}

func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	return Classify(err).Kind.ExitCode()
}

func Conflict(paths []string, hint string) *Error {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return &Error{Kind: KindConflict, Msg: hint, Paths: sorted}
}

func Usage(form string) *Error {
	return &Error{Kind: KindUsage, Msg: "usage: dsx " + form}
}

type payload struct {
	Error   Kind     `json:"error"`
	Message string   `json:"message,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

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
		return "dsx: " + de.Error()
	}
	return string(b)
}

func JSONRequested(args []string) bool {
	for _, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		if name != "--json" && name != "-json" {
			continue
		}
		if !hasValue {
			return true
		}

		if v, err := strconv.ParseBool(value); err == nil {
			return v
		}
		return true
	}
	return false
}
