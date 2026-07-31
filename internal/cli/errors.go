package cli

import (
	"context"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/geckoboard/bugsnag-cli/internal/view"
)

// newErrorsCmd builds the `errors` tree.
//
// Events hang off an error rather than living under a noun of their own, because
// that is how you reach them: you find an error, then look at its occurrences.
// There is deliberately no project-wide event listing.
func newErrorsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "errors",
		Aliases: []string{"error"},
		Short:   "List and inspect errors and their events",
		RunE:    groupRunE,
	}
	cmd.AddCommand(
		newErrorsListCmd(a),
		newErrorsViewCmd(a),
		newErrorsEventsCmd(a),
		newErrorsEventCmd(a),
	)
	return cmd
}

func newErrorsListCmd(a *app) *cobra.Command {
	var (
		sortBy   string
		allPages bool
		limit    int
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List errors in the project (the Inbox)",
		Long: "Lists errors as a row of facts per error, with the message on a row of its " +
			"own beneath it. The message is there because it is often the only thing " +
			"telling two rows apart: on a Go service every error shares one context.\n\n" +
			"`--limit` caps how many errors are shown and defaults to a screenful; pages " +
			"are fetched behind it until the limit is met. `--all-pages` lifts the limit " +
			"and fetches every error.\n\n" +
			"Caveats:\n" +
			"  - `Events` counts the interval named in the header line, not all time. That " +
			"interval is what `--since` narrows, and the project's retained history " +
			"otherwise. `errors view` shows the all-time total beside it.\n" +
			"  - The `Users` column appears only when some error has users, because these " +
			"projects report 0 when nothing identifies one.\n" +
			"  - Handled-ness is not shown: it is a property of an event, and errors carry " +
			"no such field. `errors view` shows it from the event it embeds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runErrorsList(cmd.Context(), a, sortBy, allPages, limit)
		},
	}

	f := cmd.Flags()
	f.StringVar(&sortBy, "sort", "last_seen", "sort by: last_seen, first_seen, events, users or unsorted")
	f.IntVar(&limit, "limit", a.defaultLimit(), "maximum errors to show, sized to the terminal by default")
	f.BoolVar(&allPages, "all-pages", false, "show every error, ignoring --limit")

	addFilterFlags(f)
	return cmd
}

func runErrorsList(ctx context.Context, a *app, sortBy string, allPages bool, limit int) error {
	p, err := a.project(ctx)
	if err != nil {
		return err
	}

	// Discovery short-circuits before anything else: the point is to find out what
	// could be asked for, so no list query runs.
	if done, err := a.maybeListFilters(ctx, p); done || err != nil {
		return err
	}

	filters, err := filtersFromFlags(a)
	if err != nil {
		return err
	}

	if allPages {
		limit = 0
	}
	perPage := perPageFor(limit)

	sortValue := bugsnagapi.ListProjectErrorsParamsSort(sortBy)

	// trend is only returned when a histogram is requested, and the sparkline is
	// most of what makes a row readable at a glance. "dynamic" scopes the buckets
	// to the range being asked about rather than a fixed two weeks.
	histogram := bugsnagapi.Dynamic
	params := &bugsnagapi.ListProjectErrorsParams{
		PerPage:   &perPage,
		Sort:      &sortValue,
		Histogram: &histogram,
	}

	req := bugsnagio.Request{
		Op: "list errors",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListProjectErrorsRequest(server, p.ID, params)
		},
		ExtraQuery: filters.Params(),
		// Pages are always followed; Limit is what stops the walk, so a screenful
		// is one request and --all-pages (Limit 0) runs to the end.
		AllPages: true,
		Limit:    limit,
	}

	if done, err := a.maybeDryRun(req, filters); done || err != nil {
		return err
	}

	return emitList(ctx, a, req, filters, func(
		d *render.Doc, errs []view.Error, _ bugsnagio.Meta, m render.Mode,
	) {
		view.ErrorsList(d, view.ErrorsListInput{
			Errors:      errs,
			ProjectName: p.Name,
			Filters:     filters.Describe(),
		}, m)
	})
}

func newErrorsViewCmd(a *app) *cobra.Command {
	var (
		frames      string
		showCode    bool
		showTrend   bool
		trendTable  bool
		noSummaries bool
		stats       bool
		all         bool
	)

	cmd := &cobra.Command{
		Use:     "view <error-id>",
		Aliases: []string{"get", "show"},
		Short:   "Show an error and its latest stack trace",
		Long: "Shows one error, built around the stack trace of its latest event, which is " +
			"what you want when debugging.\n\n" +
			"The pivot summaries — a breakdown of the error's occurrences by release " +
			"stage, browser, host and the like — are fetched by default, which costs one " +
			"extra request; `--no-summaries` skips them. The trend costs another request " +
			"and is fetched only with `--trend`.\n\n" +
			"Caveats:\n" +
			"  - `Events` reports two different numbers, and both say what they count. " +
			"The first covers the interval in brackets after it: this error's own " +
			"first-seen to last-seen, itself bounded by how far back the project still " +
			"holds data. `all-time` is the unthrottled total, and it can be twenty " +
			"times larger.\n" +
			"  - On Go services `in_project` is absent from every frame, so frame " +
			"filtering disables itself and the whole trace is shown.\n" +
			"  - `--code` shows nothing unless the notifier uploaded source, which " +
			"server-side Go and Ruby services generally do not.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := view.ParseFrameScope(frames)
			if err != nil {
				return usageError(err)
			}
			if all {
				scope = view.ScopeFull
				showTrend, stats = true, true
			}
			return runErrorsView(cmd.Context(), a, args[0], errorsViewOptions{
				Stacktrace: view.StacktraceOptions{
					Scope:     scope,
					Code:      showCode,
					MaxFrames: maxFramesFor(scope),
				},
				// --trend implies fetching it, and asking for the table form
				// implies wanting the trend at all.
				Trend:      showTrend || trendTable,
				TrendTable: trendTable,
				Summaries:  !noSummaries,
				Stats:      stats,
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&frames, "frames", "project", "which frames to show: project or full")
	f.BoolVar(&showCode, "code", false, "include source snippets, where the notifier uploaded them")
	f.BoolVar(&showTrend, "trend", false, "fetch and show the event trend (one extra request)")
	f.BoolVar(&trendTable, "trend-table", false, "show the trend as a table of counts rather than a sparkline")
	f.BoolVar(&noSummaries, "no-summaries", false, "skip the pivot summaries and their extra request")
	f.BoolVar(&stats, "stats", false, "show the full statistics block instead of one line")
	f.BoolVar(&all, "all", false, "include every optional section and the full trace")
	return cmd
}

// errorsViewOptions is what the error view was asked to include.
type errorsViewOptions struct {
	Stacktrace view.StacktraceOptions
	Trend      bool
	TrendTable bool
	Summaries  bool
	Stats      bool
}

func runErrorsView(ctx context.Context, a *app, errorID string, opts errorsViewOptions) error {
	p, err := a.project(ctx)
	if err != nil {
		return err
	}

	// --json returns the error object alone, its values unchanged. The extra
	// requests below exist to build the text page and would change what --json
	// means.
	if a.settings.Format == render.FormatJSON {
		return emitJSON(ctx, a, errorRequest(p.ID, errorID))
	}

	client, err := a.api()
	if err != nil {
		return err
	}

	errSink := bugsnagio.NewTypedSink[view.Error]()
	if err := client.One(ctx, errorRequest(p.ID, errorID), errSink); err != nil {
		return err
	}
	if len(errSink.Items) == 0 {
		return notFound("error", errorID)
	}

	// The stack trace is the body of this view, so the latest event is always
	// fetched, and the summaries are too unless --no-summaries turned them off.
	// The trend is the one section still fetched only on request.
	latest := a.fetchLatestEvent(ctx, errorID)

	var trend []view.TrendBucket
	if opts.Trend {
		trend = a.fetchTrend(ctx, p.ID, errorID, trendBuckets)
	}
	var pivots []view.Pivot
	if opts.Summaries {
		pivots = a.fetchPivots(ctx, p.ID, errorID)
	}

	d := a.doc()
	view.ErrorDetail(d, view.ErrorDetailInput{
		Error:        errSink.Items[0],
		DashboardURL: p.DashboardURL(errorID, ""),
		LatestEvent:  latest,
		Trend:        trend,
		TrendTable:   opts.TrendTable,
		Pivots:       pivots,
		Stats:        opts.Stats,
		Stacktrace:   opts.Stacktrace,
	}, d.Mode())

	reportDegraded(a.deps.Stderr, errSink)
	return render.Write(a.deps.Stdout, d)
}

// newErrorsEventsCmd lists the occurrences of one error.
func newErrorsEventsCmd(a *app) *cobra.Command {
	var (
		allPages bool
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "events <error-id>",
		Short: "List the events for one error",
		Long: "Lists an error's occurrences as a table of event id, when it arrived and its " +
			"context, with each occurrence's message beneath its row. Facts that are the " +
			"same on every occurrence — the error class, and usually the release stage, " +
			"severity, version and handled-ness — are stated once above the table.\n\n" +
			"`--limit` caps how many events are shown and defaults to a screenful; " +
			"`--all-pages` lifts the limit and fetches every event.\n\n" +
			"Caveats:\n" +
			"  - The error class and message come from `exceptions[0]`; this endpoint " +
			"sends no top-level `error_class` or `message` at all.\n" +
			"  - The exception key casing depends on the payload shape rather than the " +
			"endpoint: `errorClass` here, `error_class` in a full report. Both are read.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runErrorsEvents(cmd.Context(), a, args[0], allPages, limit)
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", a.defaultLimit(), "maximum events to show, sized to the terminal by default")
	f.BoolVar(&allPages, "all-pages", false, "show every event, ignoring --limit")

	addFilterFlags(f)
	return cmd
}

func runErrorsEvents(
	ctx context.Context, a *app, errorID string, allPages bool, limit int,
) error {
	p, err := a.project(ctx)
	if err != nil {
		return err
	}

	if done, err := a.maybeListFilters(ctx, p); done || err != nil {
		return err
	}

	filters, err := filtersFromFlags(a)
	if err != nil {
		return err
	}

	if allPages {
		limit = 0
	}
	perPage := perPageFor(limit)

	params := &bugsnagapi.ListEventsOnErrorParams{PerPage: &perPage}
	req := bugsnagio.Request{
		Op: "list events",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListEventsOnErrorRequest(server, p.ID, errorID, params)
		},
		ExtraQuery: filters.Params(),
		AllPages:   true,
		Limit:      limit,
	}

	if done, err := a.maybeDryRun(req, filters); done || err != nil {
		return err
	}

	return emitList(ctx, a, req, filters, func(
		d *render.Doc, events []view.Event, _ bugsnagio.Meta, m render.Mode,
	) {
		view.EventsList(d, view.EventsListInput{
			Events:  events,
			ErrorID: errorID,
			Filters: filters.Describe(),
		}, m)
	})
}

// newErrorsEventCmd shows one event in full.
func newErrorsEventCmd(a *app) *cobra.Command {
	var (
		frames      string
		showCode    bool
		metadata    bool
		request     bool
		breadcrumbs bool
		threads     bool
		all         bool
		noRedact    bool
	)

	cmd := &cobra.Command{
		Use:   "event <event-id>",
		Short: "Show one event in full",
		Long: "Shows one event: identity, the exception chain with its causes, and the " +
			"project's own frames.\n\n" +
			"Anything left out is named on the last line together with the flag that " +
			"shows it, so nothing is silently dropped.\n\n" +
			"Caveats:\n" +
			"  - `metaData` and request headers do carry live credentials, so values " +
			"whose key looks sensitive are masked. `--no-redact` disables that, and " +
			"`--json` is never redacted: its values are exactly what the API returned.\n" +
			"  - On Go services `in_project` is absent from every frame, so `--frames " +
			"project` shows the whole trace rather than nothing.\n" +
			"  - `--code` shows nothing unless the notifier uploaded source, which " +
			"server-side Go and Ruby services generally do not.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := view.ParseFrameScope(frames)
			if err != nil {
				return usageError(err)
			}
			if all {
				scope = view.ScopeFull
				metadata, request, breadcrumbs, threads = true, true, true, true
			}

			return runErrorsEvent(cmd.Context(), a, args[0], view.EventDetailInput{
				Stacktrace:      view.StacktraceOptions{Scope: scope, Code: showCode},
				ShowMetadata:    metadata,
				ShowRequest:     request,
				ShowBreadcrumbs: breadcrumbs,
				ShowThreads:     threads,
				Redact:          !noRedact,
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&frames, "frames", "project", "which frames to show: project or full")
	f.BoolVar(&showCode, "code", false, "include source snippets, where the notifier uploaded them")
	f.BoolVar(&metadata, "metadata", false, "include metaData")
	f.BoolVar(&request, "request", false, "include the request")
	f.BoolVar(&breadcrumbs, "breadcrumbs", false, "include breadcrumbs")
	f.BoolVar(&threads, "threads", false, "include the thread list")
	f.BoolVar(&all, "all", false, "include every section and the full trace")
	f.BoolVar(&noRedact, "no-redact", false, "do not mask values that look like credentials")
	return cmd
}

func runErrorsEvent(ctx context.Context, a *app, eventID string, in view.EventDetailInput) error {
	p, err := a.project(ctx)
	if err != nil {
		return err
	}

	req := bugsnagio.Request{
		Op: "view event",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewViewEventByIdRequest(server, p.ID, eventID)
		},
	}

	return emitOne(ctx, a, req, func(d *render.Doc, e view.Event, m render.Mode) {
		in.Event = e
		in.DashboardURL = p.DashboardURL(deref(e.ErrorId), deref(e.Id))
		view.EventDetail(d, in, m)
	})
}

func errorRequest(projectID, errorID string) bugsnagio.Request {
	return bugsnagio.Request{
		Op: "view error",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewViewErrorOnProjectRequest(server, projectID, errorID)
		},
	}
}

// fetchLatestEvent reads the event the detail page leads with.
//
// The latest-event endpoint is keyed by error alone; it takes no project id.
func (a *app) fetchLatestEvent(ctx context.Context, errorID string) *view.Event {
	items := fetchAside[view.Event](ctx, a, aside{
		op:     "view latest event",
		what:   "the latest event",
		single: true,
		build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewViewLatestEventOnErrorRequest(server, errorID)
		},
	})
	if len(items) == 0 {
		return nil
	}
	return &items[0]
}

// fetchPivots reads the Summaries.
func (a *app) fetchPivots(ctx context.Context, projectID, errorID string) []view.Pivot {
	summarySize := defaultSummarySize
	params := &bugsnagapi.ListPivotsOnAnErrorParams{SummarySize: &summarySize}

	return fetchAside[view.Pivot](ctx, a, aside{
		op:   "list pivots",
		what: "summaries",
		build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListPivotsOnAnErrorRequest(server, projectID, errorID, params)
		},
	})
}

// fetchTrend reads the bucketed trend. The error object's own trend field is only
// populated when a histogram is requested, and the view-error endpoint takes no
// parameters, so this endpoint is the only way to get it here.
func (a *app) fetchTrend(ctx context.Context, projectID, errorID string, buckets int) []view.TrendBucket {
	params := &bugsnagapi.GetBucketedAndUnbucketedTrendsOnErrorParams{BucketsCount: &buckets}

	return fetchAside[view.TrendBucket](ctx, a, aside{
		op:   "get trend",
		what: "the trend",
		build: func(server string) (*http.Request, error) {
			built, err := bugsnagapi.NewGetBucketedAndUnbucketedTrendsOnErrorRequest(
				server, projectID, errorID, params)
			if err != nil {
				return nil, err
			}
			// The spec declares this path as /trends, which 404s. Verified live:
			// /trend returns 200, /trends returns 404. It is corrected here rather
			// than in the overlay because renaming a path there means inlining the
			// whole path item, which a spec refresh would then silently leave stale
			// while strict mode still reported the action as applied. api/openapi
			// corrects the same path for the same reason, so `api --list-paths`
			// names the one that answers.
			built.URL.Path = strings.TrimSuffix(built.URL.Path, "/trends") + "/trend"
			return built, nil
		},
	})
}

// aside describes one of the error detail page's supporting sections.
type aside struct {
	op   string
	what string

	// single selects the one-object endpoint over the list one.
	single bool

	build bugsnagio.BuildFunc
}

// fetchAside reads a supporting section of the error detail page.
//
// A failure here warns and yields nothing rather than failing the command: the
// error and its stack trace are the point of the page, and a missing sidebar beats
// no output at all. It is a function rather than a method because only functions
// take type parameters.
func fetchAside[T any](ctx context.Context, a *app, in aside) []T {
	client, err := a.api()
	if err != nil {
		return nil
	}

	sink := bugsnagio.NewTypedSink[T]()
	req := bugsnagio.Request{Op: in.op, Build: in.build}

	if in.single {
		err = client.One(ctx, req, sink)
	} else {
		err = client.Stream(ctx, req, sink)
	}
	if err != nil {
		warnf(a.deps.Stderr, "could not load %s: %s", in.what, err)
		return nil
	}
	return sink.Items
}

const (
	// trendBuckets is how many buckets the sparkline uses: enough to show a
	// shape, few enough to stay on one line.
	trendBuckets = 14

	// defaultMaxFrames keeps the embedded trace to the part that identifies the
	// bug.
	defaultMaxFrames = 12

	// defaultSummarySize is how many values per pivot the API is asked to
	// summarise.
	defaultSummarySize = 10
)

// maxFramesFor caps the embedded trace at the frames that identify the bug,
// unless the whole trace was asked for.
//
// The truncation notice points at `--frames full`, so full scope has to lift the
// cap, not only widen which frames are eligible. It is a function rather than a
// constant because two commands build these options, `errors view` and the URL
// router, and they have to agree.
func maxFramesFor(scope view.FrameScope) int {
	if scope == view.ScopeFull {
		return 0
	}
	return defaultMaxFrames
}

// defaultLimit is a screenful, rather than the API's own default of 30, which
// scrolls off the top before it is read.
//
// It is derived from the terminal's height because an entry runs to several
// lines: its row, its message, and a blank line after it. Piped there is no page
// to fill, so it is the maximum.
func (a *app) defaultLimit() int {
	return a.mode().PageSize(render.TableEntryLines)
}

// perPageFor sizes the request chunk from the item limit, so a screenful is a
// single request. The chunk is capped at the API's page-size maximum, and an
// unbounded walk (--all-pages, limit 0) fetches the largest pages it can.
func perPageFor(limit int) int {
	if limit <= 0 || limit > apiMaxPerPage {
		return apiMaxPerPage
	}
	return limit
}

// apiMaxPerPage is the largest page the list endpoints are asked for. The API
// defaults to 30 and documents no larger maximum, so 30 is the safe chunk.
const apiMaxPerPage = 30

func usageError(err error) error {
	return apierr.Wrap(apierr.KindUsage, err, "invalid flag value")
}

func notFound(kind, id string) error {
	return apierr.New(apierr.KindNotFound, "no %s with id %s", kind, id)
}
