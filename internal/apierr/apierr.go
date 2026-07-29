// Package apierr defines the CLI's error type and the Kind taxonomy that
// drives exit codes.
//
// Every error carries its Kind, assigned where the error originates: from an
// HTTP status code, a network failure, or the specific validation that failed.
// Nothing anywhere classifies an error by matching its message text, so
// rewording a message cannot change the exit code an agent branches on.
package apierr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Kind is what went wrong, coarsely enough to branch on. internal/exitcode maps
// each Kind to a process exit code.
type Kind int

const (
	// KindInternal is a bug in this tool: an unexpected state, not a failure
	// the API or the user caused. It is the zero value so an unclassified
	// error reports as a bug rather than masquerading as something benign.
	KindInternal Kind = iota
	KindUsage
	KindConfig
	KindAuth
	KindNotFound
	KindBadRequest
	KindRateLimited
	KindServer
	KindNetwork
	KindCanceled
	KindUntrustedHost
	KindDecode
)

// String returns the stable machine-readable name emitted on stderr. These
// names are part of the CLI's contract; changing one is a breaking change.
func (k Kind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindUsage:
		return "usage"
	case KindConfig:
		return "config"
	case KindAuth:
		return "auth"
	case KindNotFound:
		return "not_found"
	case KindBadRequest:
		return "bad_request"
	case KindRateLimited:
		return "rate_limited"
	case KindServer:
		return "server"
	case KindNetwork:
		return "network"
	case KindCanceled:
		return "canceled"
	case KindUntrustedHost:
		return "untrusted_host"
	case KindDecode:
		return "decode"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Error is the CLI's error type.
type Error struct {
	Kind Kind

	// Op is what was being attempted, for the human-readable message, e.g.
	// "list errors".
	Op string

	// Message is the human-readable explanation.
	Message string

	// Status is the HTTP status this was classified from, or 0.
	Status int

	// Hint is an optional next step to suggest, e.g. naming the flag that
	// would resolve the problem.
	Hint string

	// Err is the underlying cause, if any.
	Err error
}

func (e *Error) Error() string {
	var msg string
	switch {
	case e.Message != "" && e.Op != "":
		msg = e.Op + ": " + e.Message
	case e.Message != "":
		msg = e.Message
	case e.Op != "" && e.Err != nil:
		msg = e.Op + ": " + e.Err.Error()
	case e.Op != "":
		msg = e.Op
	case e.Err != nil:
		msg = e.Err.Error()
	default:
		msg = e.Kind.String()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// New builds an error of the given Kind.
func New(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an error of the given Kind around a cause.
func Wrap(kind Kind, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Err: err}
}

// KindOf reports the Kind of err. An error that is not an *Error is a bug in
// this tool rather than a classified failure, so it reports KindInternal.
func KindOf(err error) Kind {
	if err == nil {
		return KindInternal
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	// Classify the context errors even when they reach us unwrapped, so that
	// Ctrl-C is never reported as an internal bug.
	if k, ok := kindOfContextErr(err); ok {
		return k
	}
	return KindInternal
}

// FromStatus classifies an HTTP response. Callers pass the response body so the
// API's own explanation reaches the user, but the Kind comes from the status
// code alone.
func FromStatus(op string, status int, body string) *Error {
	e := &Error{Op: op, Status: status, Message: apiMessage(status, body)}

	switch {
	case status == 401 || status == 403:
		e.Kind = KindAuth
		e.Hint = "check your API token: bugsnag auth login"
	case status == 404:
		e.Kind = KindNotFound
	case status == 429:
		e.Kind = KindRateLimited
		e.Hint = "the Data Access API allows 30 requests per minute"
	case status >= 500:
		e.Kind = KindServer
	case status >= 400:
		// Any other 4xx is something about the request we got wrong.
		e.Kind = KindBadRequest
	default:
		// A non-error status reaching here is our bug, not the API's.
		e.Kind = KindInternal
	}
	return e
}

// FromNetwork classifies a transport-level failure: a request that never
// produced an HTTP response.
func FromNetwork(op string, err error) *Error {
	if k, ok := kindOfContextErr(err); ok {
		msg := "canceled"
		if k == KindNetwork {
			msg = "timed out"
		}
		return &Error{Kind: k, Op: op, Message: msg, Err: err}
	}

	// A *url.Error wrapping a context error needs no case of its own: errors.Is
	// unwraps through it, so kindOfContextErr above has already caught it, and
	// *url.Error itself satisfies net.Error for the timeout case below.
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return &Error{Kind: KindNetwork, Op: op, Message: "timed out", Err: err}
	}

	return &Error{Kind: KindNetwork, Op: op, Message: networkMessage(err), Err: err}
}

// kindOfContextErr maps the two context errors. Cancellation is a deliberate
// stop (Ctrl-C) and must not be reported as retryable; a deadline is a timeout,
// which is.
func kindOfContextErr(err error) (Kind, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return KindCanceled, true
	case errors.Is(err, context.DeadlineExceeded):
		return KindNetwork, true
	}
	return 0, false
}

func networkMessage(err error) string {
	var derr *net.DNSError
	if errors.As(err, &derr) {
		return fmt.Sprintf("could not resolve %s", derr.Name)
	}
	return err.Error()
}

// apiMessage prefers the API's own error text, falling back to the status line.
func apiMessage(status int, body string) string {
	if m := errorTextFromJSON(body); m != "" {
		return m
	}
	return fmt.Sprintf("HTTP %d", status)
}

// errorTextFromJSON pulls the explanation out of an error body. The documented
// shape is {"errors": ["Bad request"]}; the single-string forms are accepted too
// because they cost nothing and undocumented shapes do turn up.
//
// This reads the body only to build a human-readable message. It never affects
// the Kind, which comes from the status code.
func errorTextFromJSON(body string) string {
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}

	var shape struct {
		Errors  []string `json:"errors"`
		Error   string   `json:"error"`
		Message string   `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &shape); err != nil {
		return ""
	}

	if len(shape.Errors) > 0 {
		return strings.Join(shape.Errors, "; ")
	}
	if shape.Error != "" {
		return shape.Error
	}
	return shape.Message
}

// maxErrorBody caps how much of an error body we will parse, so a stray HTML
// error page from a proxy cannot become the message.
const maxErrorBody = 8 << 10
