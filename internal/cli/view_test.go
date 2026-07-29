package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/internal/clitest"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

// url builds a dashboard URL for the fixture project, in the shape the dashboard
// actually produces.
func dashURL(path string) string {
	return "https://app.bugsnag.com/example-org/example-api/errors" + path
}

// TestViewIsTheLongFormCommand is the property that keeps this router from being a
// second renderer: whatever `view` shows, the command it names on stderr shows
// byte for byte.
func TestViewIsTheLongFormCommand(t *testing.T) {

	for name, tc := range map[string]struct {
		url  string
		long []string
	}{
		"an error": {
			url:  dashURL("/6a3a318a90a602cb08300beb"),
			long: []string{"errors", "view", "6a3a318a90a602cb08300beb", "--project", "example-api"},
		},
		"an event": {
			url:  dashURL("/6a3a318a90a602cb08300beb?event_id=ev00000000000000000001"),
			long: []string{"errors", "event", "ev00000000000000000001", "--project", "example-api"},
		},
		"an inbox": {
			url:  dashURL(""),
			long: []string{"errors", "list", "--project", "example-api"},
		},
		"an inbox with filters": {
			url:  dashURL("?filters[error.status]=open"),
			long: []string{"errors", "list", "--project", "example-api", "--status", "open"},
		},
	} {
		t.Run(name, func(t *testing.T) {

			h := clitest.New(t)
			viaURL := h.Run("view", tc.url)
			assert.Equal(t, viaURL.Code, exitcode.OK, "view failed, stderr:\n%s", viaURL.Stderr)

			h2 := clitest.New(t)
			viaLong := h2.Run(tc.long...)
			assert.Equal(t, viaLong.Code, exitcode.OK, "long form failed, stderr:\n%s", viaLong.Stderr)

			assert.Check(t, is.Equal(viaURL.Stdout, viaLong.Stdout), "stdout differs for %v", tc.long)
		})
	}
}

// TestViewAllLiftsTheFrameCapToo. `view --all` builds its own options rather than
// going through the flags, so the rule that full scope lifts the cap has to come
// from the one shared helper, not be restated here where it could drift.
func TestViewAllLiftsTheFrameCapToo(t *testing.T) {

	h := clitest.New(t)
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

	got := h.Run("view", dashURL("/6a3a318a90a602cb08300beb"), "--all")
	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "frame20.go"))
	assert.Check(t, !strings.Contains(got.Stdout, "Frame list truncated"),
		"--all should not report a truncation:\n%s", got.Stdout)
}

// TestViewNamesTheEquivalentCommand on stderr, so the next and more specific step
// is something to read off rather than work out.
func TestViewNamesTheEquivalentCommand(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("?filters[error.status]=open&filters[event.since]=30d"))

	want := "bugsnag errors list --project example-api --status open --since 30d"
	assert.Check(t, is.Contains(got.Stderr, want))
	// Never on stdout: that would break the byte-identical property above and
	// corrupt a piped table.
	assert.Check(t, !strings.Contains(got.Stdout, "equivalent"),
		"the equivalent command must not reach stdout:\n%s", got.Stdout)
}

// TestViewAppliesTheURLsFilters, translated into the CLI's own encoding rather
// than forwarded, so one request shape reaches the API.
func TestViewAppliesTheURLsFilters(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("?filters[error.status]=open&filters[event.since]=30d"))

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	var req clitest.Request
	for _, r := range h.Server.Requests() {
		if strings.HasSuffix(r.Path, "/errors") {
			req = r
		}
	}

	status := req.Query["filters[error.status][][value]"]
	assert.Check(t, len(status) > 0 && status[0] == "open",
		"the status filter did not reach the request:\n%s", req.Raw)
	// A relative value is resolved against the harness's fixed clock, so the
	// request says what it actually asked for.
	since := req.Query["filters[event.since][][value]"]
	assert.Check(t, len(since) > 0 && since[0] == "2026-06-28T12:00:00Z",
		"since = %v, want the resolved absolute time\n%s", since, req.Raw)
}

// TestViewNamesWhatItIgnored. A filter that was silently not applied looks exactly
// like a filter that matched everything, which is the failure this tool exists to
// avoid.
func TestViewNamesWhatItIgnored(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("/6a3a318a90a602cb08300beb?event_id=ev00000000000000000001&i=sk&m=nw"))

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stderr, "ignored URL parameters: i, m"))
}

// TestViewSaysWhenFiltersCannotApply: a single error is fetched by id, so filter
// state in its URL has nothing to act on.
func TestViewSaysWhenFiltersCannotApply(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("/6a3a318a90a602cb08300beb?filters[error.status]=open"))

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stderr, "do not apply to a single error"))
}

// TestViewWarnsAboutAFieldWithNoVerifiedFlag rather than forwarding it. The API
// takes any filter key with a 200 and ignores what it does not act on, so
// forwarding one that does nothing would look like a filter that matched
// everything.
func TestViewWarnsAboutAFieldWithNoVerifiedFlag(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("?filters[event.class]=TypeError"))

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stderr, "event.class"))
	assert.Check(t, is.Contains(got.Stderr, "not applied"))
	for _, r := range h.Server.Requests() {
		assert.Check(t, !strings.Contains(r.Raw, "event.class"),
			"an unverified filter was forwarded anyway: %s", r.Raw)
	}
}

// TestViewUsesTheProjectFromTheURL, for that run only. Persisting it is what
// `project link` is for.
func TestViewUsesTheProjectFromTheURL(t *testing.T) {

	h := clitest.New(t)
	// A repository that would otherwise autodetect to example-api, and a URL naming a
	// different project.
	got := h.Run("view", "https://app.bugsnag.com/example-org/worker/errors")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "worker"))
	repo, ok := h.Config().Repos["github.com/example-org/example-api"]
	assert.Check(t, !(ok && repo.ProjectID == "p-worker"),
		"a project from a URL must not be written to the config")
}

// TestViewRefusesAnotherOrganisation: the configured token may have no access to
// it, and quietly querying elsewhere would be worse than stopping.
func TestViewRefusesAnotherOrganisation(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", "https://app.bugsnag.com/acme/example-api/errors/6a3a318a90a602cb08300beb")

	assert.Assert(t, got.Code != exitcode.OK, "expected a refusal, got:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stderr, "acme"))
	assert.Check(t, is.Contains(got.Stderr, "org use"))
}

// TestViewRefusesABareID: an error id and an event id are indistinguishable, so
// guessing which was meant is not on.
func TestViewRefusesABareID(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", "6a3a318a90a602cb08300beb")

	assert.Check(t, is.Equal(got.Code, exitcode.Usage))
	assert.Check(t, is.Contains(got.Stderr, "errors view"))
}

// TestViewRefusesAForeignHost. A pasted URL never decides where the token goes.
func TestViewRefusesAForeignHost(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", "https://app.bugsnag.com.evil.test/example-org/example-api/errors/6a3a318a90a602cb08300beb")

	assert.Check(t, is.Equal(got.Code, exitcode.Usage))
	assert.Check(t, is.Equal(got.Stdout, ""), "nothing should reach stdout")
	for _, r := range h.Server.Requests() {
		assert.Check(t, !strings.Contains(r.Path, "/errors"),
			"a request was sent for a refused URL: %s", r.Path)
	}
}

// TestViewConflictingProjectIsAUsageError rather than a precedence puzzle.
func TestViewConflictingProjectIsAUsageError(t *testing.T) {

	h := clitest.New(t)
	got := h.Run("view", dashURL("/6a3a318a90a602cb08300beb"), "--project", "p-worker")

	assert.Check(t, is.Equal(got.Code, exitcode.Usage))
	assert.Check(t, is.Contains(got.Stderr, "drop one of them"))
}
