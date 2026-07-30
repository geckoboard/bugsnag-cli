package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// EventsListInput is what the events table needs.
type EventsListInput struct {
	Events []Event

	// ErrorID is set when the list is scoped to one error, which is what makes the
	// class and other facts constant and so hoistable into the preamble.
	ErrorID string

	Filters string
}

// EventsList renders events as a table, with each occurrence's message beneath
// its row.
//
// The list is scoped to a single error, so the error class and — usually — the
// release stage, severity, version and handled-ness are the same on every
// occurrence. Those are stated once above the table rather than repeated in a
// column on every row, and each is included only when it is genuinely constant
// across the page. Context is deliberately not hoisted: verified on a live
// browser app, it varies per occurrence, so it stays a column.
//
// The columns left are the event id, when it arrived and its context. The
// message goes underneath each row: within one error it varies from occurrence
// to occurrence, and that variation is the reason to be looking at a list of
// them at all. Verified on two projects — one Go service where a third of the
// occurrences of one error came from a different call site, and one browser app
// where the interpolated ids differ every time.
func EventsList(d *render.Doc, in EventsListInput, m render.Mode) {
	title := "Events"
	if in.ErrorID != "" {
		title += " — error " + in.ErrorID
	}
	d.H1("%s", title)

	if len(in.Events) == 0 {
		d.Text("No events matched.")
		if in.Filters != "" {
			d.Footer("Filters: %s", in.Filters)
		}
		return
	}

	if in.Filters != "" {
		d.Text("%s", in.Filters)
	}
	if preamble := eventsPreamble(in.Events); preamble != "" {
		d.Text("%s", preamble)
	}

	tbl := d.Table("Event", "Received", "Context")
	tbl.NeverTruncate(0)
	tbl.DetailHeader("Message")

	for _, e := range in.Events {
		tbl.Row(
			render.Code(deref(e.Id)),
			m.Timestamp(deref(e.ReceivedAt)),
			render.Escape(deref(e.Context)),
		)
		if msg := e.Msg(); msg != "" {
			tbl.Detail("%s", render.Escape(msg))
		}
	}
	tbl.Done()

	d.Footer("One occurrence in full: `bugsnag errors event <id>`")
}

// eventsPreamble names the facts that are the same on every occurrence in the
// page, so they are stated once instead of on every row. Each is included only
// when it is genuinely single-valued across the events shown.
func eventsPreamble(events []Event) string {
	var parts []string
	if class, ok := singleValued(events, func(e Event) string { return e.Class() }); ok && class != "" {
		parts = append(parts, render.Code(class))
	}
	if stage, ok := singleValued(events, eventReleaseStage); ok && stage != "" {
		parts = append(parts, stage)
	}
	if sev, ok := singleValued(events, eventSeverity); ok && sev != "" {
		parts = append(parts, "severity "+sev)
	}
	if version, ok := singleValued(events, eventAppVersion); ok && version != "" {
		parts = append(parts, "version "+version)
	}
	if handled, ok := singleValued(events, func(e Event) string { return handledToken(&e) }); ok && handled != "" {
		parts = append(parts, handled)
	}
	return strings.Join(parts, " · ")
}

// singleValued returns f's value when it is the same for every event, and
// reports whether it was.
func singleValued(events []Event, f func(Event) string) (string, bool) {
	if len(events) == 0 {
		return "", false
	}
	first := f(events[0])
	for _, e := range events[1:] {
		if f(e) != first {
			return "", false
		}
	}
	return first, true
}

func eventSeverity(e Event) string {
	if e.Severity == nil {
		return ""
	}
	return string(*e.Severity)
}

func eventReleaseStage(e Event) string {
	if e.App == nil || e.App.ReleaseStage == nil {
		return ""
	}
	return render.Escape(*e.App.ReleaseStage)
}

func eventAppVersion(e Event) string {
	if e.App == nil || e.App.Version == nil {
		return ""
	}
	return render.Code(*e.App.Version)
}

// browserToken names the browser and version for a web event, as "Safari 13.1.2",
// or empty when the event carries no browser. The version is appended only when
// present, so a browser with no version still reads cleanly.
func browserToken(e Event) string {
	if e.Device == nil || e.Device.BrowserName == nil || *e.Device.BrowserName == "" {
		return ""
	}
	browser := *e.Device.BrowserName
	if e.Device.BrowserVersion != nil && *e.Device.BrowserVersion != "" {
		browser += " " + *e.Device.BrowserVersion
	}
	return browser
}

// EventDetailInput is what the event detail view needs.
type EventDetailInput struct {
	Event Event

	DashboardURL string
	Stacktrace   StacktraceOptions

	// Sections the user asked to include. Everything not asked for is named in
	// the trailing hidden line rather than silently dropped.
	ShowMetadata    bool
	ShowRequest     bool
	ShowBreadcrumbs bool
	ShowThreads     bool

	// Redact masks values that look like credentials. metaData and request
	// headers do carry live secrets.
	Redact bool
}

// EventDetail renders one event.
func EventDetail(d *render.Doc, in EventDetailInput, m render.Mode) {
	e := in.Event

	header := render.Code(e.Class())
	if ctx := deref(e.Context); ctx != "" {
		header += " · " + render.Escape(ctx)
	}
	d.H1("%s", header)

	if msg := e.Msg(); msg != "" {
		d.Text("%s", render.Escape(msg))
	}

	writeEventIdentity(d, in, m)

	d.H2("Stack trace")
	WriteExceptionChain(d, e.Exceptions(), in.Stacktrace, m)

	if in.ShowBreadcrumbs {
		writeBreadcrumbs(d, e, m)
	}
	if in.ShowMetadata {
		writeMetadata(d, e, in.Redact)
	}
	if in.ShowRequest {
		writeRequest(d, e, in.Redact)
	}
	if in.ShowThreads {
		writeThreads(d, e)
	}

	writeHiddenLine(d, in)
}

func writeEventIdentity(d *render.Doc, in EventDetailInput, m render.Mode) {
	e := in.Event

	status := make([]string, 0, 2)
	if handled := handledToken(&e); handled != "" {
		status = append(status, handled)
	}
	if e.Severity != nil {
		status = append(status, "severity "+string(*e.Severity))
	}
	if len(status) > 0 {
		d.Field("Status", "%s", strings.Join(status, " · "))
	}

	if when := m.Timestamp(deref(e.ReceivedAt)); when != "" {
		d.Field("Received", "%s", when)
	}
	if e.App != nil {
		var app []string
		if e.App.ReleaseStage != nil {
			app = append(app, render.Escape(*e.App.ReleaseStage))
		}
		if e.App.Version != nil {
			app = append(app, "version "+render.Code(*e.App.Version))
		}
		if len(app) > 0 {
			d.Field("App", "%s", strings.Join(app, " · "))
		}
	}
	if e.Device != nil {
		if e.Device.Hostname != nil && *e.Device.Hostname != "" {
			d.Field("Host", "%s", render.Code(*e.Device.Hostname))
		}
		// For a web event the device carries the browser, OS and locale; only the
		// browser name and version are surfaced here, since that is the piece that
		// identifies where a client-side error came from. The rest stays in the
		// event's own data, reachable with --json.
		if browser := browserToken(e); browser != "" {
			d.Field("Browser", "%s", render.Escape(browser))
		}
	}
	if e.User != nil {
		if id := deref(e.User.Id); id != "" {
			d.Field("User", "%s", render.Code(id))
		}
	}
	d.Field("Event", "%s", render.Code(deref(e.Id)))
	if id := deref(e.ErrorId); id != "" {
		d.Field("Error", "%s", render.Code(id))
	}
	if in.DashboardURL != "" {
		d.Field("Dashboard", "%s", in.DashboardURL)
	}
}

func writeBreadcrumbs(d *render.Doc, e Event, m render.Mode) {
	if e.Breadcrumbs == nil || len(*e.Breadcrumbs) == 0 {
		return
	}

	d.H2("Breadcrumbs")
	tbl := d.Table("When", "Type", "Name")
	for _, b := range *e.Breadcrumbs {
		// Breadcrumb name, type and timestamp are required in the spec, so they
		// are plain values rather than pointers.
		tbl.Row(m.Timestamp(b.Timestamp.Format(time.RFC3339)), string(b.Type), render.Escape(b.Name))
	}
	tbl.Done()
}

func writeMetadata(d *render.Doc, e Event, redact bool) {
	if e.MetaData == nil || len(*e.MetaData) == 0 {
		return
	}

	d.H2("Metadata")
	tbl := d.Table("Key", "Value")
	for _, key := range sortedKeys(*e.MetaData) {
		for _, row := range flatten(key, (*e.MetaData)[key], redact) {
			tbl.Row(render.Code(row.key), row.value)
		}
	}
	tbl.Done()
}

func writeRequest(d *render.Doc, e Event, redact bool) {
	if e.Request == nil {
		return
	}
	r := e.Request

	d.H2("Request")
	if r.HttpMethod != nil || r.Url != nil {
		d.Field("Request", "%s %s", deref(r.HttpMethod), render.Code(maybeRedactURL(deref(r.Url), redact)))
	}
	if r.ClientIp != nil && *r.ClientIp != "" {
		d.Field("Client IP", "%s", render.Code(*r.ClientIp))
	}

	if r.Headers != nil && len(*r.Headers) > 0 {
		tbl := d.Table("Header", "Value")
		for _, name := range sortedKeys(*r.Headers) {
			value := fmt.Sprint((*r.Headers)[name])
			tbl.Row(render.Code(name), render.Code(redactValue(name, value, redact)))
		}
		tbl.Done()
	}
}

func writeThreads(d *render.Doc, e Event) {
	if e.Threads == nil || len(*e.Threads) == 0 {
		return
	}

	threads := *e.Threads
	d.H2("Threads")

	tbl := d.Table("Thread", "Name", "Frames")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignRight)
	shown := min(len(threads), maxThreadRows)
	for _, t := range threads[:shown] {
		frames := 0
		if t.Stacktrace != nil {
			frames = len(*t.Stacktrace)
		}
		id := ""
		if t.Id != nil {
			id = fmt.Sprint(*t.Id)
		}
		tbl.Row(render.Code(id), render.Escape(deref(t.Name)), render.Count(frames))
	}
	tbl.Done()

	if shown < len(threads) {
		d.Footer("Showing %d of %d threads.", shown, len(threads))
	}
}

// writeHiddenLine names what was left out, so nothing is silently dropped and
// the flag to see it is always to hand.
func writeHiddenLine(d *render.Doc, in EventDetailInput) {
	e := in.Event
	var hidden []string

	if !in.ShowBreadcrumbs && e.Breadcrumbs != nil && len(*e.Breadcrumbs) > 0 {
		hidden = append(hidden, fmt.Sprintf("breadcrumbs (%d) `--breadcrumbs`", len(*e.Breadcrumbs)))
	}
	if !in.ShowMetadata && e.MetaData != nil && len(*e.MetaData) > 0 {
		hidden = append(hidden,
			fmt.Sprintf("metaData (%d %s) `--metadata`", len(*e.MetaData), render.Plural(len(*e.MetaData), "key")))
	}
	if !in.ShowRequest && e.Request != nil {
		hidden = append(hidden, "request `--request`")
	}
	if !in.ShowThreads && e.Threads != nil && len(*e.Threads) > 0 {
		hidden = append(hidden, fmt.Sprintf("threads (%d) `--threads`", len(*e.Threads)))
	}
	if in.Stacktrace.Scope == ScopeProject {
		hidden = append(hidden, "library frames `--frames full`")
	}

	if len(hidden) == 0 {
		return
	}
	d.Footer("Hidden: %s · everything `--all`", strings.Join(hidden, " · "))
}

// maxThreadRows caps the thread table, which can run to a megabyte.
const maxThreadRows = 40

type kv struct {
	key   string
	value string
}

// flatten turns nested metadata into one row per leaf.
func flatten(prefix string, value any, redact bool) []kv {
	switch v := value.(type) {
	case map[string]any:
		var out []kv
		for _, k := range sortedKeys(v) {
			out = append(out, flatten(prefix+"."+k, v[k], redact)...)
		}
		return out
	case []any:
		var out []kv
		for i, item := range v {
			out = append(out, flatten(fmt.Sprintf("%s[%d]", prefix, i), item, redact)...)
		}
		return out
	case nil:
		return []kv{{prefix, "_(null)_"}}
	default:
		text := fmt.Sprint(v)
		return []kv{{prefix, render.Escape(redactValue(prefix, text, redact))}}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
