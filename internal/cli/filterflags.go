package cli

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/geckoboard/bugsnag-cli/internal/view"
)

// addFilterFlags registers the curated filter flags.
//
// The set is curated rather than a generic --filter escape hatch because the
// field ids are not guessable and the wire format is not documented.
// --list-filters asks the project what it actually supports, and is registered
// here so it is present wherever filters are, rather than somewhere a third
// filtering command could forget to wire up.
//
// The field ids here are verified against the live API rather than taken from the
// spec. The spec's Filters schema has no event.unhandled property, yet the API
// accepts that filter, so --unhandled exists regardless. The event_fields
// endpoint is not authoritative either: see --list-filters.
func addFilterFlags(f *pflag.FlagSet) {
	for _, c := range curatedFilters {
		switch c.kind {
		case filterTime:
			f.String(c.flag, "", c.usage)
		case filterBool:
			f.Bool(c.flag, false, c.usage)
		case filterList:
			f.StringSlice(c.flag, nil, c.usage)
		}
	}

	f.Bool("list-filters", false, "list the fields this project can be filtered on, then stop")
	f.Bool("print-request", false, "print the request that would be sent")
	f.Bool("dry-run", false, "print the request and stop without sending it")
}

// filterKind is how a flag's value becomes a filter condition.
type filterKind int

const (
	// filterList is a repeatable list of values compared with eq, or ne when the
	// value carries a leading "!".
	filterList filterKind = iota

	// filterTime is a single time, compared with the flag's own operator.
	filterTime

	// filterBool is a flag whose presence sets the field to true or false.
	filterBool
)

// curatedFilter maps one flag onto one event field.
type curatedFilter struct {
	flag  string
	field string
	usage string
	kind  filterKind

	// op is the comparison used by a time flag.
	op filters.Operator
}

// curatedFilters is the single source of truth for the curated flags. Flag
// registration, flag parsing and the --list-filters mapping all read this table,
// so they cannot disagree about which flag sets which field.
var curatedFilters = []curatedFilter{
	{
		flag:  "status",
		field: filters.FieldStatus,
		usage: "error status: open, fixed, snoozed or ignored (prefix ! to exclude)",
	},
	{
		flag:  "severity",
		field: filters.FieldSeverity,
		usage: "severity: error, warning or info (prefix ! to exclude)",
	},
	{
		flag:  "release-stage",
		field: filters.FieldReleaseStage,
		usage: "release stage, e.g. production (prefix ! to exclude)",
	},
	{
		flag:  "unhandled",
		field: filters.FieldUnhandled,
		kind:  filterBool,
		usage: "only unhandled events (--unhandled=false for handled only)",
	},
	{
		flag:  "since",
		field: filters.FieldSince,
		kind:  filterTime,
		op:    filters.OpAfter,
		usage: "only events at or after this time (e.g. 24h, 7d, or an ISO timestamp)",
	},
	{
		flag:  "until",
		field: filters.FieldBefore,
		kind:  filterTime,
		op:    filters.OpBefore,
		usage: "only events before this time",
	},
}

// filtersFromFlags builds the filter set from the flags the user passed.
func filtersFromFlags(a *app) (*filters.Set, error) {
	// A pasted dashboard URL supplies the filters instead, already translated
	// into the same conditions these flags would produce.
	if a.urlFilters != nil {
		return a.urlFilters, nil
	}

	set := &filters.Set{}
	f := a.flags
	if f == nil {
		return set, nil
	}

	for _, c := range curatedFilters {
		switch c.kind {
		case filterTime:
			raw, err := f.GetString(c.flag)
			if err != nil || raw == "" {
				continue
			}
			// A relative value is resolved to an absolute time here rather than
			// passed through, so what goes on the wire is unambiguous.
			value, err := resolveTime(raw, a.deps.Now())
			if err != nil {
				return nil, err
			}
			set.Add(c.field, c.op, value)

		case filterBool:
			// Presence is what matters: absent means no filter at all, so an
			// unpassed flag must not filter on false.
			if !f.Changed(c.flag) {
				continue
			}
			v, err := f.GetBool(c.flag)
			if err != nil {
				continue
			}
			set.Add(c.field, filters.OpEq, strconv.FormatBool(v))

		case filterList:
			values, err := f.GetStringSlice(c.flag)
			if err != nil {
				continue
			}
			for _, v := range values {
				set.AddValue(c.field, v)
			}
		}
	}

	return set, nil
}

// resolveTime accepts a duration like "24h" or "7d" as well as an absolute
// timestamp, and always sends an absolute time.
//
// A relative value is resolved here rather than passed through, so what goes on
// the wire is unambiguous and reproducible: two runs an hour apart with --since
// 24h are asking different questions, and the printed request should say which.
func resolveTime(raw string, now time.Time) (string, error) {
	raw = strings.TrimSpace(raw)

	// A bare day count is the common case and time.ParseDuration rejects "d".
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(days, "%d", &n); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour).UTC().Format(time.RFC3339), nil
		}
	}

	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d).UTC().Format(time.RFC3339), nil
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}

	return "", apierr.New(apierr.KindUsage,
		"cannot read %q as a time: use a duration like 24h or 7d, or an ISO timestamp", raw)
}

// maybePrintRequest handles --print-request and --dry-run.
//
// The bracket filter encoding is the one wire format the spec does not define, so
// being able to see it before sending it is how it gets verified against reality.
// It returns true when the command should stop.
func (a *app) maybePrintRequest(req bugsnagio.Request, set *filters.Set) (bool, error) {
	if a.flags == nil {
		return false, nil
	}

	print, _ := a.flags.GetBool("print-request")
	dryRun, _ := a.flags.GetBool("dry-run")
	if !print && !dryRun {
		return false, nil
	}

	url, err := a.requestURL(req)
	if err != nil {
		return true, err
	}

	d := a.doc()
	d.H1("Request")
	d.Field("Method", "GET")
	d.Field("URL", "%s", render.Code(url))
	if encoded := set.String(); encoded != "" {
		d.Field("Filters", "%s", render.Code(encoded))
	} else {
		d.Field("Filters", "none")
	}
	// The token is never printed, only that one is set.
	d.Field("Authorization", "`token` prefix with the configured token (%d characters)",
		len(a.settings.Token))

	if dryRun {
		d.Footer("Dry run: nothing was sent.")
	}
	if err := emitDoc(a, d); err != nil {
		return true, err
	}

	return dryRun, nil
}

// requestURL builds the URL a request would use, without sending it.
func (a *app) requestURL(req bugsnagio.Request) (string, error) {
	if req.Build == nil {
		return "", apierr.New(apierr.KindInternal, "request has no builder")
	}

	built, err := req.Build(a.settings.Host)
	if err != nil {
		return "", apierr.Wrap(apierr.KindInternal, err, "building request")
	}

	bugsnagio.AppendQuery(built.URL, req.ExtraQuery)
	return built.URL.String(), nil
}

// maybeListFilters handles --list-filters.
//
// It short-circuits like --dry-run: the point is to find out what you could ask
// for, so the list query itself is not run. Returns true when the command should
// stop.
func (a *app) maybeListFilters(ctx context.Context, p resolvedProject) (bool, error) {
	if a.flags == nil {
		return false, nil
	}
	if list, _ := a.flags.GetBool("list-filters"); !list {
		return false, nil
	}

	fields, err := a.fetchEventFields(ctx, p.ID)
	if err != nil {
		return true, err
	}

	d := a.doc()
	view.FiltersList(d, buildFilterRows(p.Name, fields), d.Mode())
	return true, emitDoc(a, d)
}

// fetchEventFields lists the fields a project can be filtered on.
func (a *app) fetchEventFields(ctx context.Context, projectID string) ([]bugsnagapi.EventField, error) {
	client, err := a.api()
	if err != nil {
		return nil, err
	}

	sink := bugsnagio.NewTypedSink[bugsnagapi.EventField]()
	req := bugsnagio.Request{
		Op: "list event fields",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListProjectEventFieldsRequest(server, projectID)
		},
		AllPages: true,
	}
	if err := client.Stream(ctx, req, sink); err != nil {
		return nil, err
	}
	return sink.Items, nil
}

// buildFilterRows joins the project's reported fields against the curated flags.
//
// The curated flags are listed unconditionally, because their field ids are
// verified to change the result set whether or not the event_fields endpoint
// mentions them. Everything else the project reports is listed after, as a
// starting point rather than a promise: the API accepts any key with a 200 and
// ignores the ones it does not act on, so a field being in that catalogue is no
// evidence that filtering on it does anything.
func buildFilterRows(projectName string, fields []bugsnagapi.EventField) view.FiltersInput {
	nameForField := make(map[string]string, len(fields))
	customField := make(map[string]bool, len(fields))
	for _, f := range fields {
		id := deref(f.DisplayId)
		if id == "" {
			continue
		}
		nameForField[id] = f.FilterOptions.Name
		customField[id] = deref(f.Custom)
	}

	curated := make([]view.FilterRow, 0, len(curatedFilters))
	for _, c := range curatedFilters {
		curated = append(curated, view.FilterRow{
			Flag:    c.flag,
			FieldID: c.field,
			Name:    nameForField[c.field],
			Custom:  customField[c.field],
		})
	}

	haveFlag := make(map[string]bool, len(curatedFilters))
	for _, c := range curatedFilters {
		haveFlag[c.field] = true
	}

	other := make([]view.FilterRow, 0, len(fields))
	for id, name := range nameForField {
		if haveFlag[id] {
			continue
		}
		other = append(other, view.FilterRow{FieldID: id, Name: name, Custom: customField[id]})
	}
	sort.SliceStable(other, func(i, j int) bool { return other[i].FieldID < other[j].FieldID })

	return view.FiltersInput{ProjectName: projectName, Curated: curated, Other: other}
}
