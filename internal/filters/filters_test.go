package filters_test

import (
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"gotest.tools/v3/assert"
)

// TestEncodingIsTheUnindexedBracketForm pins the one wire format the spec does
// not define.
//
// The empty brackets are not cosmetic. Verified against the live API: the indexed
// form `filters[error.status][0][type]` is rejected with a 400 saying the filter
// must be of the format {"type": ..., "value": ...}, because a numeric key makes
// it a hash where an array is wanted. Every filter flag in the CLI depended on
// getting this right.
func TestEncodingIsTheUnindexedBracketForm(t *testing.T) {

	var s filters.Set
	s.Add(filters.FieldStatus, filters.OpEq, "open")

	want := "filters[error.status][][type]=eq&filters[error.status][][value]=open"
	assert.Equal(t, s.String(), want)
}

// TestTwoConditionsOnOneFieldStayPaired: the API pairs each type with the value
// that follows it, so two conditions are two appended pairs and their order is
// part of the meaning. Encoding through a url.Values would sort every type ahead
// of every value and silently ask for something else.
func TestTwoConditionsOnOneFieldStayPaired(t *testing.T) {

	var s filters.Set
	s.AddValue(filters.FieldSeverity, "error")
	s.AddValue(filters.FieldSeverity, "!warning")

	want := "filters[event.severity][][type]=eq&filters[event.severity][][value]=error&" +
		"filters[event.severity][][type]=ne&filters[event.severity][][value]=warning"
	assert.Equal(t, s.String(), want)
}

// TestEncodeEscapesForTheWire, while String stays readable for --dry-run.
func TestEncodeEscapesForTheWire(t *testing.T) {

	var s filters.Set
	s.Add(filters.FieldSince, filters.OpEq, "2026-07-21T22:00:00Z")

	want := "filters%5Bevent.since%5D%5B%5D%5Btype%5D=eq&" +
		"filters%5Bevent.since%5D%5B%5D%5Bvalue%5D=2026-07-21T22%3A00%3A00Z"
	assert.Equal(t, filters.Encode(s.Params()), want)
}

// TestEmptySetEncodesToNothing, so a request without filters carries no trace of
// them.
func TestEmptySetEncodesToNothing(t *testing.T) {

	var s filters.Set
	assert.Assert(t, s.Params() == nil, "Params = %v, want nil", s.Params())
	assert.Equal(t, s.String(), "")
}

// TestNegationIsALeadingBang, which is the whole negation syntax.
func TestNegationIsALeadingBang(t *testing.T) {

	var s filters.Set
	s.AddValue(filters.FieldStatus, "!fixed")

	want := "filters[error.status][][type]=ne&filters[error.status][][value]=fixed"
	assert.Equal(t, s.String(), want)
}
