package cli_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/internal/clitest"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

// TestErrorsListIsTheInbox is the smallest end-to-end slice through every layer:
// config, project autodetect, transport, streaming, the lenient readers and the
// laid-out render.
func TestErrorsListIsTheInbox(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	// The document must start with its title. This is the cheapest check that the
	// laid-out path ran rather than something falling through to raw JSON.
	assert.Check(t, strings.HasPrefix(got.Stdout, "Errors — example-api\n"),
		"output does not start with its title:\n%s", got.Stdout)

	// The class and message come from exceptions[0] via the lenient readers.
	for _, want := range []string{"*fmt.wrapError", "*errors.withStack"} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}

	// The id is a whole field, never truncated, so it can be copied straight into
	// `errors view`.
	assert.Check(t, is.Contains(got.Stdout, "6a3a318a90a602cb08300beb\t"))
	// Piped output carries no notation and no styling.
	for _, marker := range []string{"`", "\x1b["} {
		assert.Check(t, !strings.Contains(got.Stdout, marker), "piped output carries %q:\n%s", marker, got.Stdout)
	}
}

// TestErrorsListNamesTheIntervalItCounts: the count for an interval and the
// all-time count diverge by orders of magnitude, so confusing them is the easiest
// way to badly misinform someone. The list names the interval once, in the
// header, and gives it explicit dates rather than a vague label like "in range".
func TestErrorsListNamesTheIntervalItCounts(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list")

	// The fixture trend starts on 2026-07-01 and the window runs to the harness's
	// fixed clock.
	assert.Check(t, is.Contains(got.Stdout, "Events and Trend cover 2026-07-01T00:00:00Z – 2026-07-28T12:00:00Z"))
	assert.Check(t, !strings.Contains(got.Stdout, "in range"),
		"a bare \"in range\" says a window exists without saying what it is:\n%s", got.Stdout)
}

// TestErrorsListOmitsZeroUsers follows the dashboard: these projects report 0
// users because nothing identifies a user, and printing 0 implies it was measured.
func TestErrorsListOmitsZeroUsers(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list")

	assert.Check(t, !strings.Contains(got.Stdout, "0 users"),
		"zero users should be omitted, not printed:\n%s", got.Stdout)
}

// TestErrorsListCarriesTheMessage. On these projects every error shares one
// Context, so the message is the only field that tells two rows apart. Piped it
// belongs on the same line as its row: one error, one line, nothing to stitch
// back together.
func TestErrorsListCarriesTheMessage(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list")

	var line string
	for _, l := range strings.Split(got.Stdout, "\n") {
		if strings.HasPrefix(l, "6a68d7fa018f5404872a0000\t") {
			line = l
		}
	}
	assert.Assert(t, line != "", "the error row is missing:\n%s", got.Stdout)
	assert.Check(t, strings.HasSuffix(line, "\tstore: unable to upsert records: context deadline exceeded"),
		"the message should end its own row:\n%s", line)
	assert.Check(t, is.Contains(got.Stdout, "errors view <id>"))
}

// tsvHeader returns the table's header line, which is the first line carrying
// tabs.
func tsvHeader(t *testing.T, stdout string) string {
	t.Helper()
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.Contains(line, "\t") {
			return line
		}
	}
	t.Fatalf("no table in:\n%s", stdout)
	return ""
}

// TestPaginationFooterIsAlwaysWritten. Without the footer, thirty rows look like
// the whole answer when thousands exist.
func TestPaginationFooterIsAlwaysWritten(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--limit", "2")

	assert.Check(t, is.Contains(got.Stdout, "Showing 2 of 3"))
}

// TestLimitFollowsOpaqueLinksAcrossPages: the internal page chunk is capped at the
// API maximum, so a limit larger than one page has to follow the Link header to
// meet it. The fake server mints offsets the CLI could not have constructed, so
// this genuinely tests that Link is followed verbatim.
func TestLimitFollowsOpaqueLinksAcrossPages(t *testing.T) {

	h := clitest.New(t)
	h.Server.Errors = manyErrors(35)

	got := h.Run("errors", "list", "--limit", "35")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	// 35 items in pages of 30 is two list requests, following the opaque Link.
	listRequests := 0
	for _, r := range h.Server.Requests() {
		if strings.HasSuffix(r.Path, "/errors") {
			listRequests++
		}
	}
	assert.Check(t, listRequests >= 2, "made %d list requests; expected the limit to span pages", listRequests)
	assert.Check(t, is.Contains(got.Stdout, "e34\t"))
}

// TestAllPagesFetchesEverything: --all-pages lifts the limit and follows Link to
// the end.
func TestAllPagesFetchesEverything(t *testing.T) {

	h := clitest.New(t)
	h.Server.Errors = manyErrors(35)

	got := h.Run("errors", "list", "--all-pages")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "Showing all 35"))
}

// manyErrors builds n minimal errors, ids e0..e(n-1), for exercising pagination.
func manyErrors(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range n {
		out[i] = json.RawMessage(fmt.Sprintf(
			`{"id":"e%d","project_id":"p-example-api","error_class":"E","message":"m %d","events":1}`, i, i))
	}
	return out
}

// TestJSONPreservesValues is the promise of the raw path: values unchanged.
func TestJSONPreservesValues(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--json")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "--json did not produce valid JSON:\n%s", got.Stdout)
	assert.Check(t, is.Len(items, 3))

	// A field the generated types do not model must survive.
	assert.Check(t, is.Contains(got.Stdout, "unknown_future_field"))
	// A count past float64's exact integer range must survive as a literal.
	assert.Check(t, is.Contains(got.Stdout, "9007199254740993"))
	// Nothing the laid-out path draws may leak onto the JSON path.
	assert.Check(t, strings.HasPrefix(strings.TrimSpace(got.Stdout), "["),
		"--json must be the response array and nothing else:\n%s", got.Stdout)
	assert.Check(t, !strings.ContainsAny(got.Stdout, "│─"),
		"gridlines leaked into --json output:\n%s", got.Stdout)
}

func TestJSONFlagAndFormatFlagAgree(t *testing.T) {

	h := clitest.New(t)

	shorthand := h.Run("errors", "list", "--json")
	explicit := h.Run("errors", "list", "--format", "json")

	assert.Check(t, is.Equal(shorthand.Stdout, explicit.Stdout), "--json and --format json differ")
}

// TestErrorsEventsHoistsInvariantFacts: scoped to one error, the class and the
// facts that are the same on every occurrence are stated once above the table,
// and the columns left are the event id, when it arrived and its context.
func TestErrorsEventsHoistsInvariantFacts(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "events", "6a3a318a90a602cb08300beb")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	// The preamble line states the facts constant across the page, above the
	// table. The class comes from exceptions[0]; this endpoint sends no top-level
	// class. The release stage is keyed release_stage, which the generated type
	// misses, so it is read leniently from the raw payload.
	preamble := firstNonTabLine(t, got.Stdout, "Events — error")
	for _, want := range []string{"*fmt.wrapError", "production", "warning", "handled"} {
		assert.Check(t, is.Contains(preamble, want))
	}

	// The table columns are just event, received and context — the hoisted facts
	// are not repeated as columns.
	header := tsvHeader(t, got.Stdout)
	assert.Check(t, is.Equal(header, "Event\tReceived\tContext\tMessage"),
		"want the reduced column set:\n%s", got.Stdout)
}

// firstNonTabLine returns the first content line that carries no tab, skipping
// the title. It is how the events preamble — a plain line above the table — is
// read.
func firstNonTabLine(t *testing.T, stdout, title string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" || strings.Contains(line, "\t") || strings.HasPrefix(line, title) {
			continue
		}
		return line
	}
	t.Fatalf("no preamble line in:\n%s", stdout)
	return ""
}

// TestErrorsListShowsUsersOnlyWhenPresent: the Users column appears when some
// error has users, and is dropped when they are all zero, because these projects
// report 0 when nothing identifies a user.
func TestErrorsListShowsUsersOnlyWhenPresent(t *testing.T) {

	t.Run("dropped when all zero", func(t *testing.T) {
		h := clitest.New(t)
		got := h.Run("errors", "list")

		header := tsvHeader(t, got.Stdout)
		assert.Check(t, !strings.Contains(header, "Users"),
			"an all-zero Users column should be dropped, header was %q:\n%s", header, got.Stdout)
		assert.Check(t, is.Equal(header, "ID\tType\tContext\tEvents\tSeen\tTrend\tMessage"))
	})

	t.Run("shown when a user count is present", func(t *testing.T) {
		h := clitest.New(t)
		h.Server.Errors = []json.RawMessage{json.RawMessage(
			`{"id":"e0","project_id":"p-example-api","error_class":"E","message":"m","events":3,"users":5}`)}
		got := h.Run("errors", "list")

		header := tsvHeader(t, got.Stdout)
		assert.Check(t, is.Contains(header, "Users"),
			"expected a Users column when a count is present, header was %q:\n%s", header, got.Stdout)
	})
}

// TestErrorsEventsCarriesEachOccurrencesMessage. With the invariant facts hoisted
// into the preamble, what is left is little more than ids and timestamps — and
// within one error the message varies from occurrence to occurrence, which is the
// reason to be looking at a list of them. Verified against two live projects.
func TestErrorsEventsCarriesEachOccurrencesMessage(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "events", "6a3a318a90a602cb08300beb")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	// Piped, the message is the last field of its own row.
	assert.Check(t, is.Contains(tsvHeader(t, got.Stdout), "\tMessage"))
	// The fixture's third occurrence failed differently from the first two, which
	// is the whole reason to show the message per row.
	for _, want := range []string{
		"resolving field #1: cannot read field id",
		"from step #4 (*steps.HTTPStep): not following 301 redirect",
	} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}
	// And it points at the command that shows one in full, which is `errors
	// event` — there is no `events` noun.
	assert.Check(t, is.Contains(got.Stdout, "bugsnag errors event <id>"))
}

// TestEventsViewShowsTheChainAndHidesTheRest.
func TestErrorsEventShowsTheChainAndHidesTheRest(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "event", "ev00000000000000000001")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	// The exception chain reads outermost first with a Caused by for each link.
	assert.Check(t, is.Contains(got.Stdout, "Caused by"))
	// The App line shows the release stage (app.releaseStage in the payload).
	assert.Check(t, is.Contains(got.Stdout, "production"))
	// Frame format is path:line, matching the dashboard.
	assert.Check(t, is.Contains(got.Stdout, "manager.go:105"))
	// Anything left out must be named along with the flag that shows it.
	assert.Check(t, is.Contains(got.Stdout, "Hidden:"))
	for _, want := range []string{"--metadata", "--request", "--threads"} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}
}

// TestErrorsEventShowsBrowserByDefault: a web event's device carries the browser,
// OS and locale, but only the browser name and version identify where a
// client-side error came from, so only those are surfaced by default.
func TestErrorsEventShowsBrowserByDefault(t *testing.T) {

	h := clitest.New(t)
	h.Server.LatestEvent = []byte(`{
		"id": "ev00000000000000000042",
		"error_id": "6a3a318a90a602cb08300beb",
		"received_at": "2026-07-28T11:59:00.000Z",
		"severity": "error",
		"unhandled": true,
		"context": "GET /dashboard",
		"app": {"releaseStage": "production", "version": "2019-05-09"},
		"device": {"osName": "Mac OS X 10.13", "browserName": "Safari",
			"browserVersion": "13.1.2", "locale": "en-us"},
		"exceptions": [{"error_class": "TypeError", "message": "undefined is not a function",
			"stacktrace": [{"file": "app.js", "method": "render", "line_number": 10}]}]
	}`)

	got := h.Run("errors", "event", "ev00000000000000000042")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	// The browser name and version are surfaced.
	assert.Check(t, is.Contains(got.Stdout, "Browser: Safari 13.1.2"))
	// The OS and locale are not — only name and version.
	assert.Check(t, !strings.Contains(got.Stdout, "Mac OS X"), "OS should not be shown by default:\n%s", got.Stdout)
	assert.Check(t, !strings.Contains(got.Stdout, "en-us"), "locale should not be shown by default:\n%s", got.Stdout)
}

// TestFramesFullLiftsTheFrameCap. The truncation notice points at `--frames full`,
// so that flag has to produce the whole trace: widening which frames are eligible
// while leaving the cap in place would make the advice useless.
func TestFramesFullLiftsTheFrameCap(t *testing.T) {

	h := clitest.New(t)
	// A trace longer than the default cap.
	var frames []string
	for i := 1; i <= 20; i++ {
		frames = append(frames, fmt.Sprintf(
			`{"file":"deep/frame%d.go","method":"Step%d","line_number":%d}`, i, i, i))
	}
	h.Server.LatestEvent = []byte(`{
		"id": "ev00000000000000000009",
		"error_id": "6a3a318a90a602cb08300beb",
		"exceptions": [{"error_class":"E","message":"m","stacktrace":[` +
		strings.Join(frames, ",") + `]}]
	}`)

	capped := h.Run("errors", "view", "6a3a318a90a602cb08300beb")
	assert.Assert(t, is.Contains(capped.Stdout, "Frame list truncated"),
		"expected the default to cap a 20-frame trace:\n%s", capped.Stdout)
	assert.Check(t, !strings.Contains(capped.Stdout, "frame20.go"),
		"the cap did not apply:\n%s", capped.Stdout)

	full := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--frames", "full")
	assert.Check(t, is.Contains(full.Stdout, "frame20.go"))
	assert.Check(t, !strings.Contains(full.Stdout, "Frame list truncated"),
		"--frames full should not report a truncation:\n%s", full.Stdout)
}

// TestGoEventShowsEveryFrame: in_project is absent on every Go frame, so frame
// filtering must disable itself rather than show nothing.
func TestGoEventShowsEveryFrame(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "event", "ev00000000000000000001")

	// All three frames of the fixture, none of which declares in_project.
	for _, want := range []string{"manager.go:105", "event.go:73", "async.go:13"} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}
}

// TestErrorsEventRedactsSecrets: real events in this organization carry a secret=
// query parameter, an auth_token metaData value and an x-honeycomb-team ingest
// key, all live. A Honeycomb trace id is not a secret, so it is left shown.
func TestErrorsEventRedactsSecrets(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "event", "ev00000000000000000001", "--metadata", "--request")

	assert.Check(t, !strings.Contains(got.Stdout, "live-secret-value"),
		"a secret was printed:\n%s", got.Stdout)
	assert.Check(t, !strings.Contains(got.Stdout, "live-ingest-key"),
		"the x-honeycomb-team ingest key was printed:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stdout, "[redacted]"))
	// The non-secret part of the URL stays readable.
	assert.Check(t, is.Contains(got.Stdout, "internal.example"))
	// A trace id identifies a trace and is not a secret, so it is shown.
	assert.Check(t, is.Contains(got.Stdout, "trace-abc123"))
}

// TestJSONIsNeverRedacted: the raw path preserves the API's values, and its help
// says so, so a secret in the payload is present on the JSON path.
func TestJSONIsNeverRedacted(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "event", "ev00000000000000000001", "--json")

	assert.Check(t, is.Contains(got.Stdout, "live-secret-value"))
}

// TestErrorsViewMirrorsTheDetailPage.
func TestErrorsViewMirrorsTheDetailPage(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "view", "6a3a318a90a602cb08300beb")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	// The default is one line with a single clearly-labelled count. The all-time
	// total and the interval this one covers are behind --stats, not here.
	assert.Check(t, is.Contains(got.Stdout, "70,681 events"))
	assert.Check(t, !strings.Contains(got.Stdout, "all-time"),
		"the all-time total should be behind --stats, not on the default line:\n%s", got.Stdout)
	// The trace is the body of this view.
	assert.Check(t, is.Contains(got.Stdout, "Stack trace"))
	// The summaries are fetched by default; the trend still costs a request and is not.
	assert.Check(t, is.Contains(got.Stdout, "\nSummaries\n"))
	assert.Check(t, !strings.Contains(got.Stdout, "\nTrend\n"),
		"the trend should be opt-in:\n%s", got.Stdout)

	// The dashboard link is composed from the project's cached html_url.
	assert.Check(t, is.Contains(got.Stdout, "app.bugsnag.com/example-org/example-api/errors/6a3a318a90a602cb08300beb"))
}

// TestErrorsViewOptionalSections: the trend and the summaries each cost a request,
// so they are fetched only when asked for.
func TestErrorsViewOptionalSections(t *testing.T) {

	t.Run("trend", func(t *testing.T) {
		h := clitest.New(t)
		got := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--trend")
		assert.Check(t, strings.ContainsAny(got.Stdout, "▁▂▃▄▅▆▇█"),
			"--trend should show a sparkline:\n%s", got.Stdout)
	})

	t.Run("summaries on by default", func(t *testing.T) {
		h := clitest.New(t)
		got := h.Run("errors", "view", "6a3a318a90a602cb08300beb")
		assert.Check(t, is.Contains(got.Stdout, "\nSummaries\n"))
		assert.Check(t, is.Contains(got.Stdout, "57.1%"))
	})

	t.Run("no-summaries skips them and their request", func(t *testing.T) {
		h := clitest.New(t)
		got := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--no-summaries")
		assert.Check(t, !strings.Contains(got.Stdout, "\nSummaries\n"),
			"--no-summaries should skip the pivot table:\n%s", got.Stdout)
		for _, r := range h.Server.Requests() {
			assert.Check(t, !strings.HasSuffix(r.Path, "/pivots"),
				"--no-summaries still fetched %s", r.Path)
		}
	})

	t.Run("stats", func(t *testing.T) {
		h := clitest.New(t)
		got := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--stats")
		// Only the full block puts the two counts side by side, and the first one
		// carries the interval it covers, worded rather than a bare date range.
		assert.Check(t, strings.Contains(got.Stdout, "70,681 events seen from") && strings.Contains(got.Stdout, "5.7M all-time"),
			"--stats should show both counts, the first with its worded interval:\n%s", got.Stdout)
	})

	t.Run("default fetches summaries but not the trend", func(t *testing.T) {
		h := clitest.New(t)
		h.Run("errors", "view", "6a3a318a90a602cb08300beb")
		var pivots, trend bool
		for _, r := range h.Server.Requests() {
			if strings.HasSuffix(r.Path, "/pivots") {
				pivots = true
			}
			if strings.HasSuffix(r.Path, "/trend") {
				trend = true
			}
		}
		assert.Check(t, pivots, "the default view should fetch the summaries")
		assert.Check(t, !trend, "the default view should not fetch the trend")
	})
}

func TestErrorsViewTrendTable(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--trend-table")

	assert.Check(t, is.Contains(got.Stdout, "From\tTo\tEvents"))
	assert.Check(t, is.Contains(got.Stdout, "44,819"))
}

// TestAllNilErrorStillRenders: every optional field absent is a shape a
// defensive renderer has to survive.
func TestAllNilErrorStillRenders(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "6a00000000000000000000ff"))
}

// TestProjectAutodetectPersistsAndNotesOnStderr: the note must not go to stdout,
// or it would corrupt a --json pipeline.
func TestProjectAutodetectPersistsAndNotesOnStderr(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--json")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stderr, "using project example-api"))

	// stdout must still be valid JSON with the note on stderr.
	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "the autodetect note corrupted stdout:\n%s", got.Stdout)

	// The resolution is persisted centrally, keyed by canonical repo identity.
	cfg := h.Config()
	repo, ok := cfg.Repos["github.com/example-org/example-api"]
	assert.Assert(t, ok, "resolution was not persisted: %+v", cfg.Repos)
	assert.Check(t, is.Equal(repo.ProjectID, "p-example-api"))
	assert.Check(t, is.Equal(repo.OrgID, "org1"), "the cache is only valid per organization")
	assert.Check(t, repo.HTMLURL != "", "html_url was not cached; dashboard links are composed from it")
}

// TestCachedProjectSkipsAutodetect.
func TestCachedProjectSkipsAutodetect(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list")
	first := h.Server.RequestCount()

	h.Run("errors", "list")
	second := h.Server.RequestCount() - first

	// The second run should not repeat the project search.
	for _, r := range h.Server.Requests()[first:] {
		assert.Check(t, !(strings.Contains(r.Path, "/projects") && strings.HasSuffix(r.Path, "/projects")),
			"the second run searched for projects again: %s?%s", r.Path, r.Raw)
	}
	assert.Check(t, second != 0, "the second run made no requests at all")
}

// TestPrefixPairDoesNotCrossMatch: worker must not resolve to worker-replica.
func TestPrefixPairDoesNotCrossMatch(t *testing.T) {

	h := clitest.New(t)
	h.Remotes = map[string]string{"origin": "git@github.com:example-org/worker.git"}

	got := h.Run("errors", "list")
	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	assert.Check(t, is.Contains(got.Stderr, "using project worker (p-worker)"))
	repo := h.Config().Repos["github.com/example-org/worker"]
	assert.Check(t, is.Equal(repo.ProjectID, "p-worker"))
}

// TestNoGitRemoteExplainsItself rather than guessing a project.
func TestNoGitRemoteExplainsItself(t *testing.T) {

	h := clitest.New(t)
	h.Remotes = nil

	got := h.Run("errors", "list")
	assert.Check(t, is.Equal(got.Code, exitcode.Config))
	assert.Check(t, is.Contains(got.Stderr, "--project"))
}

// TestFilterEncodingIsTheBracketSyntax is the one wire format the spec does not
// define, so it is asserted against the raw query string.
//
// The brackets are empty rather than indexed. Verified against the live API: an
// index makes the filter a hash where an array is wanted, and every filtered
// request comes back 400.
func TestFilterEncodingIsTheBracketSyntax(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--status", "open", "--release-stage", "production")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	var listRequest clitest.Request
	for _, r := range h.Server.Requests() {
		if strings.HasSuffix(r.Path, "/errors") {
			listRequest = r
		}
	}

	for key, want := range map[string]string{
		"filters[error.status][][type]":       "eq",
		"filters[error.status][][value]":      "open",
		"filters[app.release_stage][][type]":  "eq",
		"filters[app.release_stage][][value]": "production",
	} {
		got := listRequest.Query[key]
		assert.Check(t, len(got) > 0 && got[0] == want,
			"query param %q = %v, want %q\nraw: %s", key, got, want, listRequest.Raw)
	}
}

// TestFilterNegation: a leading ! is the whole negation syntax.
func TestFilterNegation(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list", "--status", "!fixed")

	var found bool
	for _, r := range h.Server.Requests() {
		if v := r.Query["filters[error.status][][type]"]; len(v) > 0 && v[0] == "ne" {
			found = true
		}
	}
	assert.Check(t, found, "a ! prefix should encode as ne:\n%s", h.Server.LastRequest().Raw)
}

// TestSeveralConditionsOnOneFieldAreRepeatedPairs.
//
// The API reads a field's conditions as an array and pairs each type with the
// value that follows it, so two conditions are two appended pairs in order. The
// raw query is asserted rather than a parsed map, because the order is the part
// that carries the meaning.
func TestSeveralConditionsOnOneFieldAreRepeatedPairs(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list", "--severity", "error", "--severity", "warning")

	var req clitest.Request
	for _, r := range h.Server.Requests() {
		if strings.HasSuffix(r.Path, "/errors") {
			req = r
		}
	}

	raw, err := url.QueryUnescape(req.Raw)
	assert.NilError(t, err, "query is not decodable")
	want := "filters[event.severity][][type]=eq&filters[event.severity][][value]=error&" +
		"filters[event.severity][][type]=eq&filters[event.severity][][value]=warning"
	assert.Check(t, is.Contains(raw, want))
}

// TestDryRunSendsNothing, so the filter encoding can be eyeballed before it is
// used against the live API.
func TestDryRunSendsNothing(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--status", "open", "--dry-run", "--project", "p-example-api")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "filters[error.status][][type]=eq"))
	assert.Check(t, is.Contains(got.Stdout, "Dry run"))
	// The token must never be printed.
	assert.Check(t, !strings.Contains(got.Stdout, h.Server.Token),
		"the token was printed:\n%s", got.Stdout)

	for _, r := range h.Server.Requests() {
		assert.Check(t, !strings.HasSuffix(r.Path, "/errors"),
			"--dry-run sent a request: %s?%s", r.Path, r.Raw)
	}
}

// TestSinceIsResolvedToAnAbsoluteTime, so the request is reproducible and says
// what it actually asked for.
func TestSinceIsResolvedToAnAbsoluteTime(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--since", "7d", "--dry-run", "--project", "p-example-api")

	// Seven days before the harness's fixed clock.
	assert.Check(t, is.Contains(got.Stdout, "2026-07-21T12:00:00Z"))
}

func TestUnknownFlagIsAUsageError(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--nonsense")

	assert.Check(t, is.Equal(got.Code, exitcode.Usage))
	assert.Check(t, is.Equal(got.Stdout, ""), "errors must not reach stdout")
}

// TestAPIErrorsAreClassifiedByStatus, not by matching message text.
func TestAPIErrorsAreClassifiedByStatus(t *testing.T) {

	for _, tc := range []struct {
		status int
		want   int
	}{
		{401, exitcode.Auth},
		{403, exitcode.Auth},
		{404, exitcode.NotFound},
		{422, exitcode.BadRequest},
		{429, exitcode.RateLimited},
		{500, exitcode.Server},
	} {
		h := clitest.New(t)
		h.Server.Status = tc.status
		// The same message text every time, so only the status can be driving
		// the classification.
		h.Server.Body = `{"errors":["project_id is required"]}`

		got := h.Run("errors", "list", "--project", "p-example-api")
		assert.Check(t, is.Equal(got.Code, tc.want),
			"status %d gave exit code %d, want %d\nstderr: %s", tc.status, got.Code, tc.want, got.Stderr)

		line := strings.SplitN(got.Stderr, "\n", 2)[0]
		assert.Check(t, strings.Contains(line, "exit_code=") && strings.Contains(line, "retryable="),
			"status %d: stderr is missing the machine-readable fields: %q", tc.status, line)
	}
}

// TestRetryableIsReportedForTheRetryBand: 7..9 means retry.
func TestRetryableIsReportedForTheRetryBand(t *testing.T) {

	for status, wantRetryable := range map[int]bool{
		429: true,
		500: true,
		404: false,
		401: false,
	} {
		h := clitest.New(t)
		h.Server.Status = status

		got := h.Run("errors", "list", "--project", "p-example-api")
		want := "retryable=false"
		if wantRetryable {
			want = "retryable=true"
		}
		assert.Check(t, is.Contains(got.Stderr, want), "status %d", status)
	}
}

func TestAuthStatusNeverPrintsTheToken(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("auth", "status")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, !strings.Contains(got.Stdout, h.Server.Token),
		"the token was printed in full:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stdout, "Example Org"))
}

// TestAuthLoginResolvesTheOrgSilentlyWhenThereIsOne.
func TestAuthLoginResolvesTheOrgSilentlyWhenThereIsOne(t *testing.T) {

	h := clitest.NewSignedOut(t)
	got := h.Run("auth", "login", "--token", h.Server.Token)

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "Signed in"))

	cfg := h.Config()
	assert.Check(t, is.Equal(cfg.Token, h.Server.Token), "the token was not stored")
	assert.Check(t, is.Equal(cfg.Org.ID, "org1"), "listing organizations both validates and resolves")
}

// TestCommandsWithoutATokenExplainThemselves rather than failing obscurely.
func TestCommandsWithoutATokenExplainThemselves(t *testing.T) {

	h := clitest.NewSignedOut(t)
	got := h.Run("errors", "list")

	assert.Check(t, got.Code == exitcode.Config || got.Code == exitcode.Auth,
		"code = %d, want a config or auth error", got.Code)
	assert.Check(t, is.Contains(got.Stderr, "bugsnag auth login"))
}

func TestProjectShowExplainsWhereTheProjectCameFrom(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("project", "show")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	for _, want := range []string{"example-api", "github.com/example-org/example-api", "Resolved from"} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}
}

func TestProjectLinkAndUnlink(t *testing.T) {

	h := clitest.New(t)

	link := h.Run("project", "link", "p-worker")
	assert.Equal(t, link.Code, exitcode.OK, "link failed, stderr:\n%s", link.Stderr)
	repo := h.Config().Repos["github.com/example-org/example-api"]
	assert.Check(t, is.Equal(repo.ProjectID, "p-worker"), "link stored the wrong project")

	unlink := h.Run("project", "unlink")
	assert.Equal(t, unlink.Code, exitcode.OK, "unlink failed, stderr:\n%s", unlink.Stderr)
	_, ok := h.Config().Repos["github.com/example-org/example-api"]
	assert.Check(t, !ok, "unlink did not remove the entry")
}

// TestCachedProjectIsIgnoredAfterAnOrgChange: the cache records which
// organization it belongs to, so switching cannot return another org's project.
func TestCachedProjectIsIgnoredAfterAnOrgChange(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list")

	// Point the config at a different organization, leaving the cached repo entry
	// in place.
	cfg := h.Config()
	cfg.Org.ID = "org-other"
	err := saveConfig(t, h.ConfigPath, cfg)
	assert.NilError(t, err)

	before := h.Server.RequestCount()
	h.Run("project", "show")

	// The cache must not be trusted: a fresh project search has to happen.
	// Re-resolving to the same project afterwards is fine; reusing the entry
	// without checking is not.
	searched := false
	for _, r := range h.Server.Requests()[before:] {
		if strings.HasSuffix(r.Path, "/projects") {
			searched = true
		}
	}
	assert.Check(t, searched, "a cached project from another organization was reused without re-resolving")
}

func TestTimeStyleRawIsByteFaithful(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--time", "raw")

	assert.Check(t, is.Contains(got.Stdout, "2026-07-28T11:59:00.000Z"))
}

func TestTimeStyleRelative(t *testing.T) {

	h := clitest.New(t)
	// The list's Seen column uses the compact form ("38s"), since "38 seconds ago"
	// is too wide for a column. The detail view uses the full phrase.
	listRun := h.Run("errors", "list", "--time", "relative")
	assert.Check(t, is.Contains(listRun.Stdout, "\t38s\t"),
		"--time relative should produce a compact relative column:\n%s", listRun.Stdout)
	viewRun := h.Run("errors", "view", "6a3a318a90a602cb08300beb", "--time", "relative")
	assert.Check(t, is.Contains(viewRun.Stdout, "ago"),
		"--time relative should produce relative phrases:\n%s", viewRun.Stdout)
}

// saveConfig writes a config directly, for setting up a state a command would
// not normally produce.
func saveConfig(t *testing.T, path string, cfg config.Config) error {
	t.Helper()
	return config.Save(path, cfg)
}

// TestListFiltersAsksTheProject: the curated flags cannot cover a custom filter
// an organization added, so discovery has to come from the project itself.
func TestListFiltersAsksTheProject(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--list-filters")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	// The curated flag is shown next to the field id it sets.
	assert.Check(t, is.Contains(got.Stdout, "--status\terror.status"))
	// A custom field has no flag but must still be listed, since it is the case
	// no static list could know about.
	assert.Check(t, is.Contains(got.Stdout, "user.email"))
	// event.unhandled is absent from the spec's Filters schema but accepted by
	// the API, so the flag exists.
	assert.Check(t, is.Contains(got.Stdout, "--unhandled\tevent.unhandled"))
}

// TestListFiltersTrustsVerifiedFieldsOverTheCatalogue.
//
// A curated flag is listed whether or not the project's event_fields catalogue
// mentions its field id, because the flag is verified to work and the catalogue is
// not authoritative. Marking a working flag as unavailable would be the worse
// error, so it is shown with a note instead.
func TestListFiltersTrustsVerifiedFieldsOverTheCatalogue(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--list-filters")

	// The fixture's catalogue does not mention event.since or event.before.
	for _, want := range []string{"--since\tevent.since", "--until\tevent.before"} {
		assert.Check(t, is.Contains(got.Stdout, want),
			"%s should be listed even though the catalogue omits it:\n%s", want, got.Stdout)
	}
	assert.Check(t, is.Contains(got.Stdout, "not in this project's field list"))
	// And the catalogue's own entries are still shown, in their own section. That
	// is the only route to a field with no flag of its own — error class filters
	// as event.class, which is where someone would find it.
	assert.Check(t, is.Contains(got.Stdout, "Other event fields"))
	assert.Check(t, is.Contains(got.Stdout, "event.class"))
}

// TestNoFlagLiesAboutFiltering: a flag that the API accepts and then ignores is
// worse than no flag, because filtering on it looks like every error matching.
// error.class and app.version are both accepted-and-ignored, verified live, so
// neither has a flag.
func TestNoFlagLiesAboutFiltering(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "list", "--help")

	for _, flag := range []string{"--error-class", "--app-version"} {
		assert.Check(t, !strings.Contains(got.Stdout, flag),
			"%s filters nothing and must not be offered:\n%s", flag, got.Stdout)
	}
}

// TestUnhandledFilterEncodes: absent means no filter at all, so an unpassed flag
// must not filter on false.
func TestUnhandledFilterEncodes(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list", "--unhandled")

	var req clitest.Request
	for _, r := range h.Server.Requests() {
		if strings.HasSuffix(r.Path, "/errors") {
			req = r
		}
	}
	values := req.Query["filters[event.unhandled][][value]"]
	assert.Check(t, len(values) > 0 && values[0] == "true", "--unhandled = %v, want true\nraw: %s", values, req.Raw)

	h2 := clitest.New(t)
	h2.Run("errors", "list")
	for _, r := range h2.Server.Requests() {
		assert.Check(t, !strings.Contains(r.Raw, "event.unhandled"),
			"an unpassed --unhandled filtered anyway: %s", r.Raw)
	}
}

// TestListFiltersRunsNoListQuery: it short-circuits like --dry-run, because the
// point is to find out what could be asked for.
func TestListFiltersRunsNoListQuery(t *testing.T) {

	h := clitest.New(t)
	h.Run("errors", "list", "--list-filters")

	for _, r := range h.Server.Requests() {
		assert.Check(t, !strings.HasSuffix(r.Path, "/errors"),
			"--list-filters ran the list query: %s?%s", r.Path, r.Raw)
	}
}

// TestListFiltersIsOnEveryFilteringCommand: it is registered by the same function
// that registers the filters, so it cannot drift out of sync with them.
func TestListFiltersIsOnEveryFilteringCommand(t *testing.T) {

	for _, args := range [][]string{
		{"errors", "list", "--list-filters"},
		{"errors", "events", "6a3a318a90a602cb08300beb", "--list-filters"},
	} {
		h := clitest.New(t)
		got := h.Run(args...)

		assert.Check(t, is.Equal(got.Code, exitcode.OK), "%v: stderr:\n%s", args, got.Stderr)
		assert.Check(t, is.Contains(got.Stdout, "Filters — example-api"), "args=%v", args)
	}
}

// TestUnknownSubcommandIsAUsageError: cobra's default is to print the parent's
// help and exit 0, which makes a typo look like success.
func TestUnknownSubcommandIsAUsageError(t *testing.T) {

	for _, args := range [][]string{
		{"project", "nonsense"},
		{"errors", "nonsense"},
		{"auth", "nonsense"},
		{"org", "nonsense"},
	} {
		h := clitest.New(t)
		got := h.Run(args...)

		assert.Check(t, is.Equal(got.Code, exitcode.Usage), "args=%v", args)
		assert.Check(t, is.Equal(got.Stdout, ""), "args=%v, errors must not reach stdout", args)
	}
}

// TestGroupCommandWithNoArgsShowsHelp and succeeds, which is what someone
// exploring the tree expects.
func TestGroupCommandWithNoArgsShowsHelp(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("project")

	assert.Check(t, is.Equal(got.Code, exitcode.OK))
	assert.Check(t, is.Contains(got.Stdout, "Available Commands"))
}

// TestCodeShowsSnippetsWhenPresent. There was no end-to-end coverage of --code,
// which is why nobody could tell whether the flag worked: it does, but only when
// the notifier uploaded source.
func TestCodeShowsSnippetsWhenPresent(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("errors", "event", "ev00000000000000000001", "--code")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "store: %w"))
	// The frame's own line is marked, so you can see which one threw.
	assert.Check(t, is.Contains(got.Stdout, "> 105"))
}

// TestCodeSaysWhenThereIsNoSource: silently printing nothing looks like a broken
// flag, when the real answer is that the event carries no source.
func TestCodeSaysWhenThereIsNoSource(t *testing.T) {

	h := clitest.New(t)
	// A full report whose frames carry no code at all.
	h.Server.LatestEvent = []byte(`{
		"id": "ev00000000000000000009",
		"error_id": "6a3a318a90a602cb08300beb",
		"exceptions": [{"error_class":"E","message":"m",
			"stacktrace":[{"file":"a.go","method":"Run","line_number":1}]}]
	}`)

	got := h.Run("errors", "event", "ev00000000000000000009", "--code")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "No source snippets"))
}

// TestNoProjectWideEventListing: events hang off an error, so there is no `events`
// noun to reach for.
func TestNoProjectWideEventListing(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("events", "list")

	assert.Check(t, is.Equal(got.Code, exitcode.Usage), "the events command was removed")
}
