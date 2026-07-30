// Package filters encodes the Data Access API's filter query parameters.
//
// This is hand-rolled because the generated encoder is provably wrong.
// oapi-codegen types the filters parameter as an object and emits form+explode
// serialisation for it, which flattens to `error.status=<json>` — nothing like
// the bracket syntax the API actually wants:
//
//	filters[error.status][][type]=eq&filters[error.status][][value]=open
//
// The overlay removes those parameters so the wrong encoder cannot be reached,
// and this package produces the right wire format instead. It is the one wire
// format the spec does not define, which is why --dry-run prints it.
//
// The empty brackets are load-bearing, and verified against the live API: the
// indexed form `filters[error.status][0][type]` is rejected with a 400 saying
// the filter "must be of the format {"type" : ..., "value" : ...}", because a
// numeric key makes it a hash where the API wants an array. Two conditions on
// one field are therefore two appended pairs rather than two indices, and their
// order matters: each type must immediately precede its own value, or they pair
// up with the wrong partners on the way in.
package filters

import (
	"fmt"
	"net/url"
	"strings"
)

// Operator is a filter comparison.
type Operator string

const (
	// OpEq matches a value exactly.
	OpEq Operator = "eq"

	// OpNe excludes a value.
	OpNe Operator = "ne"

	// OpAfter and OpBefore bound a time field.
	OpAfter  Operator = "after"
	OpBefore Operator = "before"
)

// Condition is one filter clause.
type Condition struct {
	// Field is an event field id, such as "error.status" or "app.release_stage".
	Field string

	Operator Operator
	Value    string
}

// Set is a group of conditions.
//
// Conditions on the same field are OR-ed by the API; conditions on different
// fields are AND-ed. That is the API's behaviour, not a choice made here.
type Set struct {
	conditions []Condition
}

// Add appends a condition.
func (s *Set) Add(field string, op Operator, value string) {
	if field == "" || value == "" {
		return
	}
	s.conditions = append(s.conditions, Condition{Field: field, Operator: op, Value: value})
}

// AddValue appends a condition, reading a leading "!" as "not equal".
//
// The prefix is the whole negation syntax: `--status '!fixed'` rather than a
// second flag for every field.
func (s *Set) AddValue(field, value string) {
	if value == "" {
		return
	}
	if rest, negated := strings.CutPrefix(value, "!"); negated {
		s.Add(field, OpNe, rest)
		return
	}
	s.Add(field, OpEq, value)
}

// Len is how many conditions are set.
func (s *Set) Len() int { return len(s.conditions) }

// Conditions returns the conditions in insertion order.
func (s *Set) Conditions() []Condition {
	out := make([]Condition, len(s.conditions))
	copy(out, s.conditions)
	return out
}

// Param is one encoded query parameter.
//
// Filters are an ordered list rather than a url.Values because url.Values groups
// by key and sorts when it encodes, which would emit every type before every
// value and pair them up wrongly. A condition's type and value have to stay
// adjacent on the wire.
type Param struct {
	Key   string
	Value string
}

// Params encodes the set as query parameters, in the order they must be sent.
func (s *Set) Params() []Param {
	if len(s.conditions) == 0 {
		return nil
	}

	params := make([]Param, 0, 2*len(s.conditions))
	for _, c := range s.conditions {
		prefix := fmt.Sprintf("filters[%s][]", c.Field)
		params = append(params,
			Param{prefix + "[type]", string(c.Operator)},
			Param{prefix + "[value]", c.Value},
		)
	}
	return params
}

// Encode renders the parameters as a query string.
func Encode(params []Param) string {
	if len(params) == 0 {
		return ""
	}

	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

// Describe renders the set for a view's header line, in a form that can be
// pasted back as flags.
func (s *Set) Describe() string {
	if len(s.conditions) == 0 {
		return ""
	}

	parts := make([]string, 0, len(s.conditions))
	for _, c := range s.conditions {
		op := ":"
		switch c.Operator {
		case OpNe:
			op = ":!"
		case OpAfter:
			op = ">"
		case OpBefore:
			op = "<"
		}
		parts = append(parts, fmt.Sprintf("`%s%s%s`", shortField(c.Field), op, c.Value))
	}
	return strings.Join(parts, " ")
}

// String renders the encoded query for --dry-run, unescaped so the
// brackets are readable. The order is the order it goes on the wire, since that
// is part of what makes the encoding correct.
func (s *Set) String() string {
	params := s.Params()
	if len(params) == 0 {
		return ""
	}

	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, p.Key+"="+p.Value)
	}
	return strings.Join(parts, "&")
}

// Field ids used by the curated flags.
//
// Each one is verified against the live API by filtering on a real value and on a
// made-up one and checking the result set actually differs. A status code proves
// nothing here: the API accepts *any* filter key with a 200 and silently ignores
// the ones it does not know, so `filters[nonsense.field][][value]=whatever`
// returns every error in the project. Verified live on example-api and browser-app.
//
// That is why there are no error-class or app-version flags. The obvious ids for
// them — error.class and app.version — are accepted and ignored, so a filter on
// them looked like "every error is this class". The ids that do work are
// event.class and version.seen_in, both of which the project's own event_fields
// catalogue lists, so --list-filters names them and --filter applies them.
//
// The spec is not authoritative either: its Filters schema omits event.unhandled
// and search entirely, yet the API accepts and applies both.
const (
	FieldStatus       = "error.status"
	FieldSeverity     = "event.severity"
	FieldReleaseStage = "app.release_stage"
	FieldSince        = "event.since"
	FieldBefore       = "event.before"
	FieldUnhandled    = "event.unhandled"

	// FieldSearch is full-text search, and carries no namespace prefix. The
	// project's own event_fields catalogue describes it as matching "across all
	// available data", and that is measurably wider than any one field: on a
	// Rails project, searching for "circular" matched six errors where
	// event.message matched four, the extra two carrying the term only in the
	// stack frames of their latest event. Verified live in all 49 projects of
	// one organization.
	FieldSearch = "search"
)

// shortField trims the namespace for display, since the flag names are already
// unambiguous.
func shortField(field string) string {
	if _, rest, found := strings.Cut(field, "."); found {
		return rest
	}
	return field
}
