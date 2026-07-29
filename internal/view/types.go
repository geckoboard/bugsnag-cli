package view

import (
	"bytes"
	"encoding/json"

	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
)

// Error is one error, as the generated type plus the bytes it came from.
//
// The raw bytes are kept because the generated type cannot express the spec's
// inconsistencies: its exception type declares only the camelCase errorClass, so
// a full report's error_class decodes to nil. The lenient readers consult Raw for
// those fields, while everything else comes from the generated struct and so
// stays tied to the spec.
type Error struct {
	bugsnagapi.ErrorApiView
	Raw json.RawMessage
}

func (e *Error) UnmarshalJSON(b []byte) error {
	e.Raw = bytes.Clone(b)
	return json.Unmarshal(b, &e.ErrorApiView)
}

// Class is the error class, read from the exception chain.
func (e *Error) Class() string { return ErrorClass(e.Raw) }

// Msg is the error message, read from the exception chain.
func (e *Error) Msg() string { return Message(e.Raw) }

// Event is one event, as the generated type plus its raw bytes.
//
// The generated type is EventApiView for a full report. The thin projection
// `events list` returns has its own shape, but every field the views read is
// either in both or reached through the lenient readers.
type Event struct {
	bugsnagapi.EventApiView
	Raw json.RawMessage
}

func (e *Event) UnmarshalJSON(b []byte) error {
	e.Raw = bytes.Clone(b)
	return json.Unmarshal(b, &e.EventApiView)
}

// Class is the event's error class, read from exceptions[0]. `events list` sends
// no top-level class at all.
func (e *Event) Class() string { return ErrorClass(e.Raw) }

// Msg is the event's message, read from exceptions[0].
func (e *Event) Msg() string { return Message(e.Raw) }

// Exceptions is the event's exception chain, outermost first.
func (e *Event) Exceptions() []Exception { return ExceptionsFrom(e.Raw) }
