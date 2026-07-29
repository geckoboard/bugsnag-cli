package view

import (
	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// FilterRow is one filterable event field.
type FilterRow struct {
	// Flag is the curated flag that sets this field, or empty when the field has
	// no flag of its own.
	Flag string

	// FieldID is the value the API expects as a filter key. Bugsnag's own
	// documentation calls this an event field's display id.
	FieldID string

	// Name is the human-readable name the dashboard shows, or empty when the
	// project did not report this field.
	Name string

	// Custom marks a field the organization created, which is the case no static
	// list could cover.
	Custom bool
}

// FiltersInput is what the filter listing needs.
type FiltersInput struct {
	ProjectName string

	// Curated are the flags this CLI provides, whose field ids are verified.
	Curated []FilterRow

	// Other are the remaining fields the project reports.
	Other []FilterRow
}

// FiltersList renders what a project can be filtered on.
//
// The two groups are deliberately separate, and the reason is a verified
// discrepancy rather than tidiness. The event_fields endpoint is Bugsnag's
// catalogue of dashboard filter fields, which is not the same thing as the set of
// keys the filter API acts on: it omits ids that work, and it lists ids the API
// takes with a 200 and then ignores. So the curated flags are presented as
// reliable — each one verified to change the result set — and everything else as
// a lead to follow.
func FiltersList(d *render.Doc, in FiltersInput, m render.Mode) {
	title := "Filters"
	if in.ProjectName != "" {
		title += " — " + render.Escape(in.ProjectName)
	}
	d.H1("%s", title)

	if len(in.Curated) > 0 {
		d.Text("Flags this CLI provides. Each field id is verified against the API.")

		tbl := d.Table("Flag", "Field id", "Name")
		// A field id is what you would copy to build a filter by hand, so it is
		// never shown partially.
		tbl.NeverTruncate(1)
		for _, r := range in.Curated {
			// The placeholder is composed notation, so it is not escaped; a real
			// name is data and is. Works as a filter, but the project's
			// event-field catalogue does not mention it: worth showing, not hiding.
			name := "_(not in this project's field list)_"
			if r.Name != "" {
				name = render.Escape(r.Name)
			}
			tbl.Row(render.Code("--"+r.Flag), render.Code(r.FieldID), name)
		}
		tbl.Done()
	}

	if len(in.Other) == 0 {
		return
	}

	d.H2("Other event fields this project reports")

	d.Text("These have no flag. Check one before trusting it: the API accepts any " +
		"filter key with a 200 and silently ignores the ones it does not act on, so " +
		"a filter that does nothing looks like a filter that matched everything. " +
		"Compare a real value against a made-up one — if both return the same " +
		"count, the field is being ignored.")

	tbl := d.Table("Field id", "Name", "Custom")
	tbl.NeverTruncate(0)
	for _, r := range in.Other {
		custom := ""
		if r.Custom {
			custom = "yes"
		}
		tbl.Row(render.Code(r.FieldID), render.Escape(r.Name), custom)
	}
	tbl.Done()

	d.Footer("See the encoding a filter produces with `--dry-run`.")
}
