// Package view turns API responses into documents.
//
// Every function here is a pure function of its input and a render.Mode: no I/O,
// no clock, no terminal. That is what makes the views golden-testable without a
// pty or a network.
//
// This file holds the lenient readers. They exist because the spec and the API
// disagree in ways that would otherwise show up as blank output:
//
//   - `events list` returns no top-level error_class or message. They live in
//     exceptions[0]. Reading the top-level fields yields zero values, and the
//     event listing shows empty classes.
//   - The key casing depends on the payload shape rather than the endpoint:
//     errorClass in the thin list projection, error_class in every full report.
//     The generated type only declares one of the two, so reading it alone loses
//     half the responses.
//   - in_project is absent on every Go event. Absent is not false: treating it
//     as false would hide the whole stack trace on those services.
package view

import (
	"encoding/json"
	"strconv"
)

// Exception is one exception from either payload shape.
type Exception struct {
	ErrorClass string
	Message    string
	Stacktrace []Frame
}

// Frame is one stack frame.
type Frame struct {
	File         string
	Method       string
	LineNumber   int
	ColumnNumber int

	// InProject is deliberately three-valued. Absent is not false: it is absent
	// on every Go event, and treating that as "library code" would hide the
	// entire trace.
	InProject Tristate

	// Code is the source snippet, keyed by line number as a string.
	Code map[string]string
}

// Tristate is a boolean that may be absent.
type Tristate int8

const (
	// Unknown means the field was absent or null. Every Go event's frames are
	// Unknown.
	Unknown Tristate = iota
	No
	Yes
)

// Bool reports the value and whether it was present.
func (t Tristate) Bool() (bool, bool) {
	switch t {
	case Yes:
		return true, true
	case No:
		return false, true
	}
	return false, false
}

// ExceptionsFrom reads the exception chain out of a raw event or error payload.
//
// It reads from the raw bytes rather than a generated struct because the
// generated exception type declares only the camelCase key, so a full event
// report's error_class would decode to nil.
//
// The first item is the outermost exception; each later item caused the one
// before it, which is the order the dashboard's "Caused by" chain reads in.
func ExceptionsFrom(raw json.RawMessage) []Exception {
	if len(raw) == 0 {
		return nil
	}

	var envelope struct {
		Exceptions []map[string]any `json:"exceptions"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}

	out := make([]Exception, 0, len(envelope.Exceptions))
	for _, m := range envelope.Exceptions {
		out = append(out, exceptionFromMap(m))
	}
	return out
}

// classAndMessage reads both fields from one pass over the payload. They are
// always wanted together, and each falling back to the top level separately meant
// parsing the same bytes up to three times.
func classAndMessage(raw json.RawMessage) (class, msg string) {
	return classAndMessageFrom(ExceptionsFrom(raw), raw)
}

// classAndMessageFrom is classAndMessage for a caller that already has the chain.
func classAndMessageFrom(chain []Exception, raw json.RawMessage) (class, msg string) {
	if len(chain) > 0 {
		class, msg = chain[0].ErrorClass, chain[0].Message
	}
	if class != "" && msg != "" {
		return class, msg
	}

	// Only a payload whose chain did not supply both is read again at the top
	// level, which is the `errors list` projection.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return class, msg
	}
	if class == "" {
		class = mapString(m, "error_class", "errorClass")
	}
	if msg == "" {
		msg = mapString(m, "message")
	}
	return class, msg
}

// isSentinelFrame reports whether a frame carries no usable location.
//
// Some notifiers emit a placeholder frame with no file and no method, and a few
// use the string "unknown" for a method that could not be resolved. Rendering
// those as `:0` is noise, so they are dropped rather than shown.
func isSentinelFrame(f Frame) bool {
	if f.File != "" {
		return false
	}
	switch f.Method {
	case "", "unknown", "<unknown>", "?":
		return true
	}
	return false
}

// FilterableValues reads what one returned item shows for the fields a filter can
// name, keyed by what the value is rather than by any one field id.
//
// It exists so a filter the API ignored can be caught by the rows it answered
// with: the API returns 200 for a filter key it does not act on, so a wrong field
// id looks exactly like a filter everything matched. A row carrying a value the
// filter excludes settles it.
//
// Only what an error or event actually carries is here. A filter on metaData, a
// user or a stack frame cannot be judged from these rows, and guessing at one
// would be worse than staying quiet.
func FilterableValues(raw json.RawMessage) map[string]string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}

	out := make(map[string]string, 6)
	for _, key := range []string{"context", "status", "severity", "id"} {
		if v := mapString(m, key); v != "" {
			out[key] = v
		}
	}

	// Class and message go through the lenient reader: they live in exceptions[0]
	// on an event listing, which carries no top-level pair at all.
	class, msg := classAndMessage(raw)
	if class != "" {
		out["class"] = class
	}
	if msg != "" {
		out["message"] = msg
	}
	return out
}

// exceptionFromMap reads one exception, accepting either key casing.
func exceptionFromMap(m map[string]any) Exception {
	e := Exception{
		ErrorClass: mapString(m, "errorClass", "error_class"),
		Message:    mapString(m, "message"),
	}

	frames, _ := m["stacktrace"].([]any)
	e.Stacktrace = make([]Frame, 0, len(frames))
	for _, raw := range frames {
		fm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		e.Stacktrace = append(e.Stacktrace, frameFromMap(fm))
	}
	return e
}

func frameFromMap(m map[string]any) Frame {
	f := Frame{
		File:       mapString(m, "file"),
		Method:     mapString(m, "method"),
		InProject:  mapTristate(m, "in_project", "inProject"),
		LineNumber: mapInt(m, "line_number", "lineNumber"),

		// column_number is declared a string in the spec but comes back as an
		// integer, so both are accepted here as well as being patched in the
		// overlay.
		ColumnNumber: mapInt(m, "column_number", "columnNumber"),
	}

	if code, ok := m["code"].(map[string]any); ok && len(code) > 0 {
		f.Code = make(map[string]string, len(code))
		for line, text := range code {
			if s, ok := text.(string); ok {
				f.Code[line] = s
			}
		}
	}
	return f
}

// mapString reads the first key that holds a non-empty string.
func mapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// mapInt reads the first key that holds a number, accepting the string form too
// since the spec declares column_number as a string.
//
// float64 is the only numeric case because these maps all come from a plain
// encoding/json unmarshal into any, which has no other numeric form.
func mapInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	}
	return 0
}

// mapTristate distinguishes false from absent.
func mapTristate(m map[string]any, keys ...string) Tristate {
	for _, k := range keys {
		v, present := m[k]
		if !present || v == nil {
			continue
		}
		if b, ok := v.(bool); ok {
			if b {
				return Yes
			}
			return No
		}
	}
	return Unknown
}
