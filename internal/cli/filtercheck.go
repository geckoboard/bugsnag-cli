package cli

import (
	"encoding/json"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/view"
)

// warnIfFiltersIgnored reports a filter the rows themselves disprove.
//
// The API answers 200 for a filter key it does not act on and returns everything,
// so a mistyped field id is indistinguishable from a filter that matched. The
// rows settle it without a second request: one row carrying a value the filter
// excludes is proof the filter was not applied. That is why this is a
// contradiction rather than a count comparison — a filter that legitimately
// matches every error would fail a count test and passes this one.
//
// Only the escape hatch is checked. The curated flags are verified against the
// live API, so a contradiction there would mean the field's meaning changed
// underneath us, which is not what this warning is for.
func (a *app) warnIfFiltersIgnored(set *filters.Set, rows []json.RawMessage) {
	// Listings that take no filters at all, such as organizations, pass none.
	if set == nil || set.Len() == 0 || len(rows) == 0 {
		return
	}

	values := make([]map[string]string, 0, len(rows))
	for _, raw := range rows {
		if v := view.FilterableValues(raw); len(v) > 0 {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return
	}

	for _, f := range checkableFields(set) {
		conds, observable := f.conds, f.observable

		for _, row := range values {
			got, ok := row[observable.key]
			if !ok || got == "" {
				continue
			}
			if satisfiesAny(conds, got, observable.substring) {
				continue
			}

			warnf(a.deps.Stderr,
				"`--filter %s` did not filter: a result has %s %q. "+
					"The API returns 200 for a field id it does not act on and ignores it; "+
					"check the id with `--list-filters`",
				describeCondition(conds[0]), observable.key, truncateForWarning(got))
			break
		}
	}
}

// observable is where a field's value shows up in a returned row, and how the
// API compares it.
type observable struct {
	// key is the value's name in view.FilterableValues.
	key string

	// substring is whether the API matches this field by substring rather than
	// exactly. The spec says so for the event.* text fields.
	substring bool
}

// observableFields is the fields a returned row can disprove a filter on.
//
// The wrong-but-obvious ids are here deliberately, since they are the ones worth
// catching: error.class and app.version are accepted and ignored by the API, and
// error.class is the id someone reaches for before finding event.class.
//
// `search` is deliberately absent and must stay that way. It matches across all
// available data, including stack frames the row does not carry, so a row whose
// class and message lack the term proves nothing — on a real project it matched
// six errors where event.message matched four, the extra two carrying the term
// only in their frames.
var observableFields = map[string]observable{
	"error.class":   {key: "class", substring: true},
	"event.class":   {key: "class", substring: true},
	"error.message": {key: "message", substring: true},
	"event.message": {key: "message", substring: true},
	"app.context":   {key: "context", substring: true},
	"error.context": {key: "context", substring: true},
	"error.id":      {key: "id"},
	"error":         {key: "id"},
}

// checkableField is a field worth checking, with what it was filtered on and
// where its value shows up in a row.
type checkableField struct {
	conds      []filters.Condition
	observable observable
}

// checkableFields is the uncurated fields in the set that a row could disprove,
// in the order they were given.
//
// A field whose conditions mix eq and ne, or use a time operator, is left out:
// what the API does with those combinations is not established well enough to
// call a mismatch a bug, and a false warning here would teach people to ignore
// the real one.
func checkableFields(set *filters.Set) []checkableField {
	var order []checkableField
	seen := make(map[string]bool)

	for _, c := range set.Conditions() {
		if seen[c.Field] {
			continue
		}
		if _, curated := curatedByField(c.Field); curated {
			continue
		}
		observable, ok := observableFields[c.Field]
		if !ok {
			continue
		}

		seen[c.Field] = true
		if conds := conditionsOn(set, c.Field); unambiguous(conds) {
			order = append(order, checkableField{conds: conds, observable: observable})
		}
	}
	return order
}

func conditionsOn(set *filters.Set, field string) []filters.Condition {
	var out []filters.Condition
	for _, c := range set.Conditions() {
		if c.Field == field {
			out = append(out, c)
		}
	}
	return out
}

// unambiguous reports whether every condition on a field uses the same
// equality operator, which is the only case with one clear reading.
func unambiguous(conds []filters.Condition) bool {
	for _, c := range conds {
		if c.Operator != conds[0].Operator {
			return false
		}
		if c.Operator != filters.OpEq && c.Operator != filters.OpNe {
			return false
		}
	}
	return len(conds) > 0
}

// satisfiesAny reports whether a row's value is consistent with the conditions on
// its field. Conditions on one field are OR-ed by the API, so an eq value need
// match only one of them, while a ne value must match none.
func satisfiesAny(conds []filters.Condition, got string, substring bool) bool {
	for _, c := range conds {
		matched := strings.EqualFold(got, c.Value)
		if substring {
			matched = strings.Contains(strings.ToLower(got), strings.ToLower(c.Value))
		}

		if c.Operator == filters.OpNe {
			if matched {
				return false
			}
			continue
		}
		if matched {
			return true
		}
	}
	return conds[0].Operator == filters.OpNe
}

// describeCondition renders a condition the way it would be typed back in.
func describeCondition(c filters.Condition) string {
	op := "="
	switch c.Operator {
	case filters.OpNe:
		op = "!="
	case filters.OpAfter:
		op = ">"
	case filters.OpBefore:
		op = "<"
	}
	return c.Field + op + c.Value
}

// truncateForWarning keeps a message from taking over the warning line.
func truncateForWarning(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
