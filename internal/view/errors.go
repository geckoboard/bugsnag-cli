package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// ErrorsListInput is what the Inbox view needs beyond the errors themselves.
type ErrorsListInput struct {
	Errors []Error

	// ProjectName titles the view.
	ProjectName string

	// Filters describes the applied filters, already formatted.
	Filters string
}

// ErrorsList renders the error list as a row of facts per error, followed by the
// message spanning the whole width.
//
// The message has to be there. On a project where every error shares a Context —
// which is the normal case for a Go service, where every row reads
// `unknown method:0` — the message is the only field that tells two rows apart.
// It cannot be a column either: a column wide enough to be useful starves every
// other one, and a column narrow enough to fit shows about twenty characters of a
// message whose diagnosis is at the end.
//
// So it is a row of its own, spanning the table. That is the layout this package
// exists to be able to draw: it is not expressible in markdown, and the table
// renderer markdown would be handed has no span support.
func ErrorsList(d *render.Doc, in ErrorsListInput, m render.Mode) {
	title := "Errors"
	if in.ProjectName != "" {
		title += " — " + render.Escape(in.ProjectName)
	}
	d.H1("%s", title)

	if len(in.Errors) == 0 {
		d.Text("No errors matched.")
		if in.Filters != "" {
			d.Footer("Filters: %s", in.Filters)
		}
		return
	}

	writeErrorsHeader(d, in, m)

	// The Users column is shown only when some error actually has users. These
	// projects report zero users when nothing identifies one, so on many of them
	// the column would be blank on every row and carry nothing.
	showUsers := false
	for _, e := range in.Errors {
		if e.Users != nil && *e.Users > 0 {
			showUsers = true
			break
		}
	}

	headers := []string{"ID", "Type", "Context", "Events"}
	aligns := []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight}
	if showUsers {
		headers = append(headers, "Users")
		aligns = append(aligns, render.AlignRight)
	}
	headers = append(headers, "Seen", "Trend")
	aligns = append(aligns, render.AlignRight, render.AlignLeft)

	tbl := d.Table(headers...)
	tbl.Align(aligns...)
	// A truncated id looks copyable and is not, and a truncated sparkline is
	// worse than either: it cuts the newest buckets, so a spike today would
	// simply not be there. Both are shown whole or not at all.
	tbl.NeverTruncate(0, len(headers)-1)
	tbl.DetailHeader("Message")

	sparkWidth := sparklineWidth(m)

	for _, e := range in.Errors {
		class := e.Class()
		if class == "" {
			class = "(no class)"
		}

		cells := []string{
			render.Code(deref(e.Id)),
			render.Code(class),
			render.Escape(deref(e.Context)),
			countOrBlank(e.Events),
		}
		if showUsers {
			cells = append(cells, positiveCountOrBlank(e.Users))
		}
		cells = append(cells,
			m.TimestampShort(deref(e.LastSeen)),
			sparklineN(trendCounts(e.Trend), sparkWidth),
		)

		tbl.Row(cells...)
		if msg := e.Msg(); msg != "" {
			tbl.Detail("%s", render.Escape(msg))
		}
	}
	tbl.Done()

	d.Footer("Full message and stack trace: `bugsnag errors view <id>`")
}

// writeErrorsHeader names the interval the Events column and the sparklines
// cover, and any active filters, since neither a number nor a column of blocks
// says what period it belongs to. The shown-of-total count is the pagination
// footer's job, so it is not repeated here.
func writeErrorsHeader(d *render.Doc, in ErrorsListInput, m render.Mode) {
	var parts []string
	if window := errorsWindow(in, m); window != "" {
		parts = append(parts, "Events and Trend cover "+window)
	}
	if in.Filters != "" {
		parts = append(parts, in.Filters)
	}
	if len(parts) > 0 {
		d.Text("%s", strings.Join(parts, " · "))
	}
}

// errorsWindow names the interval the Events column counts.
//
// Verified against the live API: with histogram=dynamic the first bucket starts
// at the window the request was scoped to — the --since value when one was given,
// and otherwise the oldest event the project still retains — and the counts run
// to now. Every error in one response shares that window, so the first error with
// a trend describes them all.
//
// Naming it matters because the interval is invisible otherwise, and the same
// error's all-time total can be twenty times larger.
func errorsWindow(in ErrorsListInput, m render.Mode) string {
	for _, e := range in.Errors {
		from := trendStart(e.Trend)
		if from == "" {
			continue
		}
		return fmt.Sprintf("%s – %s", m.ShortDate(from), m.ShortDateOf(m.Now))
	}
	return ""
}

// sparklineWidth is how many cells the Trend column gets, so the sparkline can be
// grouped down to fit rather than cut off.
//
// Grouping preserves the shape; truncating loses the end of the series. A narrow
// terminal gets a shorter sparkline for that reason, since with an id column that
// cannot shrink there is not room for ten cells and two readable text columns.
func sparklineWidth(m render.Mode) int {
	if m.TTY && m.Width < narrowWidth {
		return 5
	}
	return 10
}

// narrowWidth is where the table stops having room for everything at full size.
const narrowWidth = 90

func countOrBlank(n *int) string {
	if n == nil {
		return ""
	}
	return render.Count(*n)
}

// positiveCountOrBlank additionally blanks a zero. The users column appears when
// any row has a user count, so the rows that do not need to read as empty rather
// than as a measured nought.
func positiveCountOrBlank(n *int) string {
	if n == nil || *n <= 0 {
		return ""
	}
	return render.Count(*n)
}

// statusTokens is the status, handled-ness and severity triple, which the
// one-line summary and the --stats field block both open with.
func statusTokens(in ErrorDetailInput) []string {
	e := in.Error

	tokens := make([]string, 0, 3)
	if e.Status != nil {
		tokens = append(tokens, string(*e.Status))
	}
	// Handled-ness comes from the embedded event, the only place it exists.
	if handled := handledToken(in.LatestEvent); handled != "" {
		tokens = append(tokens, handled)
	}
	return append(tokens, severityToken(e))
}

// severityToken renders the error's severity.
//
// The dashboard shows severity and a HANDLED pill together, but handled-ness is
// a property of an event and ErrorApiView carries no such field: getting it for a
// list would mean one extra request per row against a 30-request-per-minute
// limit. So the Inbox shows severity alone, and the detail view adds
// handled-ness from the event it already embeds.
func severityToken(e Error) string {
	if e.Severity == nil {
		return "severity unknown"
	}
	return "severity " + string(*e.Severity)
}

// handledToken renders an event's handled-ness, which is only knowable from an
// event.
func handledToken(ev *Event) string {
	if ev == nil || ev.Unhandled == nil {
		return ""
	}
	if *ev.Unhandled {
		return "unhandled"
	}
	return "handled"
}

// ErrorDetailInput is what the error detail view needs.
type ErrorDetailInput struct {
	Error Error

	// DashboardURL is composed from the project's cached html_url, since errors
	// carry no dashboard link of their own.
	DashboardURL string

	// LatestEvent carries the stack trace, which is the reason to open this view,
	// so it is the body of the page rather than a trailing section.
	LatestEvent *Event

	// Trend is the bucketed trend. Empty unless asked for: it costs a request.
	Trend []TrendBucket

	// TrendTable promotes the sparkline to a table of counts.
	TrendTable bool

	// Pivots are the Summaries rows. Empty unless asked for: another request.
	Pivots []Pivot

	// Stats expands the one-line summary into the full field block.
	Stats bool

	Stacktrace StacktraceOptions
}

// ErrorDetail renders one error, built around its stack trace.
//
// The trace is what you came for when debugging, so it is the body and everything
// else is either one line or behind a flag. The pivot summaries are fetched by
// default (one extra request); the trend costs another and is fetched only with
// --trend.
func ErrorDetail(d *render.Doc, in ErrorDetailInput, m render.Mode) {
	e := in.Error

	class := e.Class()
	if class == "" {
		class = "(no error class)"
	}
	header := render.Code(class)
	if ctx := deref(e.Context); ctx != "" {
		header += " · " + render.Escape(ctx)
	}
	d.H1("%s", header)

	if msg := e.Msg(); msg != "" {
		d.Text("%s", render.Escape(msg))
	}

	if in.Stats {
		writeErrorStats(d, in, m)
	} else {
		d.Line("%s", strings.Join(errorSummaryTokens(in, m), " · "))
	}

	writeLatestEvent(d, in, m)

	writeTrend(d, in, m)
	if len(in.Pivots) > 0 {
		writeSummaries(d, in.Pivots, m)
	}

	writeErrorFooter(d, in)
}

// errorSummaryTokens is the one-line default: what you want to know before
// reading the trace, and nothing more.
func errorSummaryTokens(in ErrorDetailInput, m render.Mode) []string {
	e := in.Error

	tokens := statusTokens(in)

	if e.Events != nil {
		// One clearly-labelled count on the default line. The all-time total and
		// the interval this one covers are the business of --stats: showing two
		// diverging numbers and a bare date range here misled more than it told.
		tokens = append(tokens, fmt.Sprintf("%s events", render.Count(*e.Events)))
	}
	if last := m.Timestamp(deref(e.LastSeen)); last != "" {
		tokens = append(tokens, "last seen "+last)
	}
	return tokens
}

// writeErrorStats is the full field block, behind --stats.
func writeErrorStats(d *render.Doc, in ErrorDetailInput, m render.Mode) {
	e := in.Error

	d.Field("Status", "%s", strings.Join(statusTokens(in), " · "))

	if when := m.TimestampRange(deref(e.FirstSeen), deref(e.LastSeen)); when != "" {
		d.Field("Seen", "%s", when)
	}

	// The two counts are always distinguished, and the first one says what
	// interval it counts. They diverge wildly in practice, and the dashboard
	// shows both for that reason.
	d.Field("Events", "%s", eventCounts(e, m))

	if e.ReleaseStages != nil && len(*e.ReleaseStages) > 0 {
		d.Field("Stages", "%s", render.Escape(strings.Join(*e.ReleaseStages, ", ")))
	}
	if e.GroupingReason != nil && *e.GroupingReason != "" {
		d.Field("Grouping", "%s", render.Escape(string(*e.GroupingReason)))
	}
	// Zero users stays omitted, for the same reason as in the Inbox.
	if e.Users != nil && *e.Users > 0 {
		d.Field("Users", "%s", render.Count(*e.Users))
	}
	if e.CommentCount != nil && *e.CommentCount > 0 {
		d.Field("Comments", "%d", *e.CommentCount)
	}
	d.Field("ID", "%s", render.Code(deref(e.Id)))
}

// writeErrorFooter names what was not fetched, so the flag for it is always to
// hand rather than something to go looking for.
func writeErrorFooter(d *render.Doc, in ErrorDetailInput) {
	if in.DashboardURL != "" {
		d.Line("%s", in.DashboardURL)
	}

	var more []string
	if !in.Stats {
		more = append(more, "`--stats`")
	}
	if len(in.Trend) == 0 {
		more = append(more, "`--trend`")
	}
	more = append(more, "`--frames full`", "`--code`")

	d.Footer("More: %s · occurrences: `bugsnag errors events %s`",
		strings.Join(more, " · "), deref(in.Error.Id))
}

// eventCounts renders the two counts as an interval count and an all-time total,
// each worded so it reads unambiguously.
//
// events counts the interval the error was seen over; unthrottled_occurrence_count
// is the all-time total. They diverge by orders of magnitude — 211,038 against
// 5.7M on one example-api error — so neither is ever printed without saying what it
// counts.
func eventCounts(e Error, m render.Mode) string {
	var parts []string
	if e.Events != nil {
		parts = append(parts, fmt.Sprintf("%s events%s", render.Count(*e.Events), seenGloss(e, m)))
	}
	if e.UnthrottledOccurrenceCount != nil {
		parts = append(parts, fmt.Sprintf("%s all-time", render.Compact(*e.UnthrottledOccurrenceCount)))
	}
	if len(parts) == 0 {
		return "not reported"
	}
	return strings.Join(parts, " · ")
}

// seenGloss words the interval an error's event count covers, as " seen from May
// 29 to Jul 28", or empty when the error carries no dates. It is worded rather
// than a bare parenthetical range so the count reads clearly on its own.
//
// This endpoint takes no filters, so the interval is the error's own first_seen
// to last_seen. Verified against the live API: the count matches a filtered query
// bounded at first_seen, and first_seen is itself bounded by how far back the
// project still holds data — which is why the all-time total can be twenty times
// larger.
func seenGloss(e Error, m render.Mode) string {
	from, to := m.ShortDate(deref(e.FirstSeen)), m.ShortDate(deref(e.LastSeen))
	switch {
	case from == "" && to == "":
		return ""
	case from == "":
		return " seen up to " + to
	case to == "":
		return " seen since " + from
	}
	return " seen from " + from + " to " + to
}

func writeTrend(d *render.Doc, in ErrorDetailInput, m render.Mode) {
	if len(in.Trend) == 0 {
		return
	}

	counts := bucketCounts(in.Trend)
	d.H2("Trend")

	if !in.TrendTable {
		window := fmt.Sprintf("(%s – %s, %d buckets)",
			m.Timestamp(in.Trend[0].From), m.Timestamp(in.Trend[len(in.Trend)-1].To), len(in.Trend))
		d.Line("%s  %s", sparkline(counts), window)
		return
	}

	tbl := d.Table("From", "To", "Events")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignRight)
	for _, b := range in.Trend {
		tbl.Row(m.Timestamp(b.From), m.Timestamp(b.To), render.Count(b.Events))
	}
	tbl.Done()
}

func writeLatestEvent(d *render.Doc, in ErrorDetailInput, m render.Mode) {
	if in.LatestEvent == nil {
		return
	}

	when := m.Timestamp(deref(in.LatestEvent.ReceivedAt))
	if when != "" {
		d.H2("Stack trace · latest event %s", when)
	} else {
		d.H2("Stack trace · latest event")
	}

	WriteExceptionChain(d, in.LatestEvent.Exceptions(), in.Stacktrace, m)
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// writeSummaries renders the pivot table, shown by default and skipped with
// --no-summaries.
//
// Rows sort by descending distinct count, so the high-cardinality pivots — the ones
// worth drilling into — come first, and zero-cardinality pivots sort last rather
// than being hidden.
//
// Share is computed rather than read: the API's proportion field exists only on the
// per-pivot values endpoint, which would cost one request per pivot. The
// denominator is the pivot's own total, which the spec defines as its values plus
// no_value plus other.
func writeSummaries(d *render.Doc, pivots []Pivot, m render.Mode) {
	rows := make([]Pivot, 0, len(pivots))
	for _, p := range pivots {
		// Events, Errors and Users report a bare count in the cardinality field
		// with no value distribution. Listing that under "Distinct" would read as
		// that many distinct values.
		if p.IsCountOnly() {
			continue
		}
		rows = append(rows, p)
	}
	if len(rows) == 0 {
		return
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Cardinality() > rows[j].Cardinality()
	})

	d.H2("Summaries")

	tbl := d.Table("Pivot", "Top value", "Share", "Distinct")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight)
	for _, p := range rows {
		value, share := topPivotValue(p)
		tbl.Row(render.Escape(p.Name), value, share, render.Count(p.Cardinality()))
	}
	tbl.Done()
}

// topPivotValue returns the most common value and its share of the pivot's total.
func topPivotValue(p Pivot) (string, string) {
	summary := p.Summary()

	top, ok := summary.Top()
	if !ok {
		// No values at all, but the total is still meaningful: every event has no
		// value for this field, so the empty value accounts for all of it.
		if summary.Total() > 0 {
			return "_(empty)_", "100%"
		}
		return "_(empty)_", ""
	}

	value := top.Value
	if value == "" {
		value = "_(empty)_"
	} else {
		value = render.Code(value)
	}

	total := summary.Total()
	if total <= 0 {
		return value, ""
	}
	return value, fmt.Sprintf("%.1f%%", 100*float64(top.Events)/float64(total))
}
