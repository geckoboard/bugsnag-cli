package view

import (
	"bytes"
	"encoding/json"

	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
)

// Pivot is one Summaries row: the generated type plus the bytes it came from.
//
// The raw bytes are needed because the spec puts list, no_value and other at the
// top level of a pivot and declares `summary` as an untyped blob, while the API
// actually nests them inside `summary`:
//
//	{"name":"Release Stages","cardinality":2,
//	 "summary":{"list":[{"value":"production","events":150065}],"no_value":0,"other":0}}
//
// Reading only what the spec declares gives an empty Top value and Share on every
// row, which is what a live check found. Both shapes are accepted.
type Pivot struct {
	bugsnagapi.PivotApiView
	Raw json.RawMessage

	// summary is derived at decode time. The Summaries table asks for it several
	// times per row — for the share, the top value and the count-only test — and
	// deriving it means parsing the nested shape back out of Raw.
	summary PivotSummary
}

func (p *Pivot) UnmarshalJSON(b []byte) error {
	p.Raw = bytes.Clone(b)
	if err := json.Unmarshal(b, &p.PivotApiView); err != nil {
		return err
	}
	p.summary = readPivotSummary(b)
	return nil
}

// PivotSummary is a pivot's value distribution.
type PivotSummary struct {
	// Values are the most common values, highest count first as the API returns
	// them.
	Values []PivotValue

	// NoValue and Other complete the total, per the spec's own description that
	// the list plus these two account for every matching event.
	NoValue int
	Other   int

	// Present reports whether the pivot carried a summary at all. Some pivots
	// (Events, Errors, Users) are bare counts with no value distribution, and a
	// missing summary must not read as an empty one.
	Present bool
}

// PivotValue is one value and its event count.
type PivotValue struct {
	Value  string
	Events int
}

// Summary is the pivot's value distribution.
func (p *Pivot) Summary() PivotSummary { return p.summary }

// readPivotSummary reads the distribution from either shape. The embedded flat
// struct is the shape the spec declares, reached by field promotion, so the two
// forms share one set of field tags.
func readPivotSummary(b []byte) PivotSummary {
	var envelope struct {
		Summary *pivotSummaryJSON `json:"summary"`
		pivotSummaryJSON
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return PivotSummary{}
	}

	// Reality first: the nested form is what the API sends.
	if envelope.Summary != nil {
		return summaryFrom(*envelope.Summary)
	}

	flat := envelope.pivotSummaryJSON
	if len(flat.List) > 0 || flat.NoValue != nil || flat.Other != nil {
		return summaryFrom(flat)
	}

	return PivotSummary{}
}

func summaryFrom(s pivotSummaryJSON) PivotSummary {
	return PivotSummary{
		Values:  toValues(s.List),
		NoValue: deref(s.NoValue),
		Other:   deref(s.Other),
		Present: true,
	}
}

// Total is every event the pivot accounts for. The spec states that the list plus
// no_value plus other covers all matching events, which makes it the correct
// denominator for a share.
func (s PivotSummary) Total() int {
	total := s.NoValue + s.Other
	for _, v := range s.Values {
		total += v.Events
	}
	return total
}

// Top returns the most common value.
func (s PivotSummary) Top() (PivotValue, bool) {
	if len(s.Values) == 0 {
		return PivotValue{}, false
	}

	best := 0
	for i, v := range s.Values {
		if v.Events > s.Values[best].Events {
			best = i
		}
	}
	return s.Values[best], true
}

// Cardinality is how many distinct values the pivot has.
func (p *Pivot) Cardinality() int { return deref(p.PivotApiView.Cardinality) }

// IsCountOnly reports whether the pivot is a bare total rather than a value
// distribution.
//
// The Events, Errors and Users pivots report a count in the cardinality field and
// carry no values. Showing 210,634 in a "Distinct" column would read as 210,634
// distinct somethings, and the event count is already in the header, so these are
// left out of the Summaries table.
func (p *Pivot) IsCountOnly() bool {
	switch p.EventFieldDisplayId {
	case "event", "error":
		return true
	}
	return !p.Summary().Present
}

type pivotSummaryJSON struct {
	List    []pivotValueJSON `json:"list"`
	NoValue *int             `json:"no_value"`
	Other   *int             `json:"other"`
}

type pivotValueJSON struct {
	Value  *string `json:"value"`
	Events *int    `json:"events"`
}

func toValues(in []pivotValueJSON) []PivotValue {
	out := make([]PivotValue, 0, len(in))
	for _, v := range in {
		out = append(out, PivotValue{Value: deref(v.Value), Events: deref(v.Events)})
	}
	return out
}
