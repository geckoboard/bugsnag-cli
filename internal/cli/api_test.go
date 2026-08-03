package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/internal/clitest"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

// TestAPIPassesTheResponseThrough is the point of the command: an endpoint with no
// view of its own still reaches the caller, with the API's values intact.
func TestAPIPassesTheResponseThrough(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Equal(t, h.Server.LastRequestTo("/errors").Path, "/projects/p-example-api/errors")

	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "the passthrough did not produce valid JSON:\n%s", got.Stdout)
	assert.Check(t, is.Len(items, 3))

	// The same promise the --json path makes: a field the generated types do not
	// model, and an integer past float64's exact range, both survive.
	assert.Check(t, is.Contains(got.Stdout, "unknown_future_field"))
	assert.Check(t, is.Contains(got.Stdout, "9007199254740993"))
}

// TestAPIReturnsASingleObjectAsAnObject: nothing is wrapped, so an endpoint that
// answers with one object is not turned into a one-element array.
func TestAPIReturnsASingleObjectAsAnObject(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors/6a3a318a90a602cb08300beb")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, strings.HasPrefix(strings.TrimSpace(got.Stdout), "{"),
		"a single object came back as something else:\n%s", got.Stdout)
}

// TestAPIFillsInTheIdsItAlreadyKnows, so a path can be pasted from the API
// reference without first running two commands to look the ids up.
func TestAPIFillsInTheIdsItAlreadyKnows(t *testing.T) {
	for _, path := range []string{
		"/organizations/{organization_id}/projects",
		"/organizations/{org}/projects",
	} {
		h := clitest.New(t)
		got := h.Run("api", path)

		assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
		assert.Equal(t, h.Server.LastRequestTo("/projects").Path, "/organizations/org1/projects")
	}
}

// TestAPIKeepsOnlyThePathOfAURL. A URL from the reference, or the next-page URL
// this command itself prints, has to work — and must not be a way to send the
// token to whatever host it names.
func TestAPIKeepsOnlyThePathOfAURL(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "https://api.bugsnag.com/projects/p-example-api/errors?per_page=2")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	req := h.Server.LastRequestTo("/errors")
	assert.Equal(t, req.Path, "/projects/p-example-api/errors")
	assert.Check(t, is.DeepEqual(req.Query["per_page"], []string{"2"}))
}

// TestAPIQueryParametersAreEscaped, so a value with a space or an ampersand in it
// needs no encoding by hand.
func TestAPIQueryParametersAreEscaped(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors",
		"--query", "sort=first_seen",
		"--query", "filters[event.class][][value]=a & b")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	req := h.Server.LastRequestTo("/errors")
	assert.Check(t, is.DeepEqual(req.Query["sort"], []string{"first_seen"}))
	assert.Check(t, is.DeepEqual(req.Query["filters[event.class][][value]"], []string{"a & b"}))
}

// TestAPIQueryParametersJoinAQueryAlreadyInThePath, rather than replacing it.
func TestAPIQueryParametersJoinAQueryAlreadyInThePath(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors?per_page=2", "--query", "sort=first_seen")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	req := h.Server.LastRequestTo("/errors")
	assert.Check(t, is.DeepEqual(req.Query["per_page"], []string{"2"}))
	assert.Check(t, is.DeepEqual(req.Query["sort"], []string{"first_seen"}))
}

// TestAPIReportsWhatTheHeadersSaid: a bare JSON array carries neither the total
// nor a cursor, so a page of two would otherwise look like the whole answer. The
// next page is given as the command that fetches it, and both go to stderr so
// stdout stays a clean JSON document.
func TestAPIReportsWhatTheHeadersSaid(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors", "--query", "per_page=2")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stderr, "note: X-Total-Count: 3"))
	assert.Check(t, is.Contains(got.Stderr, "note: next page: bugsnag api '"))
	assert.Check(t, is.Contains(got.Stderr, "offset=opaque-2-"))

	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "the notes leaked onto stdout:\n%s", got.Stdout)
	assert.Check(t, is.Len(items, 2))
}

// TestAPILimitFollowsPagesToReachIt, the same way the typed list commands do.
func TestAPILimitFollowsPagesToReachIt(t *testing.T) {
	h := clitest.New(t)
	h.Server.Errors = manyErrors(35)

	got := h.Run("api", "/projects/{project_id}/errors", "--limit", "32")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "not valid JSON:\n%s", got.Stdout)
	assert.Check(t, is.Len(items, 32))
}

// TestAPIAllPagesConcatenatesThePages into one array, since two JSON arrays cannot
// be joined by appending their bytes.
func TestAPIAllPagesConcatenatesThePages(t *testing.T) {
	h := clitest.New(t)
	h.Server.Errors = manyErrors(35)

	got := h.Run("api", "/projects/{project_id}/errors", "--all-pages")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	var items []map[string]any
	err := json.Unmarshal([]byte(got.Stdout), &items)
	assert.NilError(t, err, "not valid JSON:\n%s", got.Stdout)
	assert.Check(t, is.Len(items, 35))
}

// TestAPIDryRunSendsNothing, so a hand-written path and query can be read back
// before it is used against the live API.
func TestAPIDryRunSendsNothing(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/{project_id}/errors", "--query", "sort=first_seen",
		"--dry-run", "--project", "p-example-api")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "/projects/p-example-api/errors"))
	assert.Check(t, is.Contains(got.Stdout, "sort=first_seen"))
	assert.Check(t, is.Contains(got.Stdout, "Dry run"))
	assert.Check(t, !strings.Contains(got.Stdout, h.Server.Token),
		"the token was printed:\n%s", got.Stdout)

	for _, r := range h.Server.Requests() {
		assert.Check(t, !strings.HasSuffix(r.Path, "/errors"),
			"--dry-run sent a request: %s?%s", r.Path, r.Raw)
	}
}

// TestAPIFailuresKeepTheirExitCodes: the passthrough goes through the same
// classification as everything else, so a script can still branch on the code.
func TestAPIFailuresKeepTheirExitCodes(t *testing.T) {
	h := clitest.New(t)
	h.Server.Status = 404
	h.Server.Body = `{"errors":["Not Found"]}`

	got := h.Run("api", "/projects/p-example-api/errors")

	assert.Equal(t, got.Code, exitcode.NotFound, "stdout:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stderr, "GET /projects/p-example-api/errors: Not Found"))
	assert.Check(t, is.Contains(got.Stderr, "kind=not_found"))
}

// TestAPIRejectsWhatItCannotSend, rather than putting a malformed parameter on the
// wire and reporting whatever the API made of it.
func TestAPIRejectsWhatItCannotSend(t *testing.T) {
	for _, args := range [][]string{
		{"api", "/projects/{project_id}/errors", "--query", "nonsense"},
		{"api", "?per_page=2"},
	} {
		h := clitest.New(t)
		got := h.Run(args...)

		assert.Equal(t, got.Code, exitcode.Usage, "args %v\nstdout:\n%s", args, got.Stdout)
	}
}

// TestAPIWithNoPathSaysHowToFindOne. A passthrough is useless without knowing what
// to pass it, so the failure names the flag that answers that.
func TestAPIWithNoPathSaysHowToFindOne(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api")

	assert.Equal(t, got.Code, exitcode.Usage, "stdout:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stderr, "bugsnag api --list-paths"))
}

// TestListPathsIsTheDiscoveryMechanism: the catalogue comes from the vendored
// spec, so it needs no token, no project and no request — and it covers the
// endpoints the generated client prunes, which are the ones worth discovering.
func TestListPathsIsTheDiscoveryMechanism(t *testing.T) {
	h := clitest.NewSignedOut(t)
	got := h.Run("api", "--list-paths")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Equal(t, h.Server.RequestCount(), 0, "listing the paths called the API")

	// An endpoint with a command, and two with none: the second kind is the
	// reason this command exists.
	assert.Check(t, is.Contains(got.Stdout, "/projects/{project_id}/errors"))
	assert.Check(t, is.Contains(got.Stdout, "/projects/{project_id}/releases"))
	assert.Check(t, is.Contains(got.Stdout, "/organizations/{organization_id}/teams"))

	// The summary is what makes the list readable as a menu.
	assert.Check(t, is.Contains(got.Stdout, "List the Errors on a Project"))
}

// TestSpecSaysWhatAPathTakes, which is the half of discovery the catalogue does
// not cover: a path is no use without knowing the parameters it accepts.
func TestSpecSaysWhatAPathTakes(t *testing.T) {
	h := clitest.NewSignedOut(t)
	got := h.Run("api", "/projects/{project_id}/releases", "--spec")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Equal(t, h.Server.RequestCount(), 0, "reading the spec called the API")

	// Written as the API's own YAML and nothing else, the way --json writes the
	// response and nothing else, so it can be piped into a parser.
	var doc map[string]any
	assert.NilError(t, yaml.Unmarshal([]byte(got.Stdout), &doc),
		"--spec did not produce valid YAML:\n%s", got.Stdout)
	assert.Check(t, is.Len(doc, 1))

	assert.Check(t, is.Contains(got.Stdout, "name: per_page"))
	assert.Check(t, is.Contains(got.Stdout, "name: release_stage"))
}

// TestSpecTakesThePathYouJustRequested, ids and all, rather than only the form
// the spec writes it in.
func TestSpecTakesThePathYouJustRequested(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/projects/p-example-api/errors/6a3a318a90a602cb08300beb/events", "--spec")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)
	assert.Check(t, is.Contains(got.Stdout, "/projects/{project_id}/errors/{error_id}/events:"))
}

// TestSpecSaysSoWhenItCannotAnswer. The vendored spec is a snapshot, so its
// silence is not proof the endpoint is absent, and the message says as much
// rather than implying the request would fail.
func TestSpecSaysSoWhenItCannotAnswer(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "/nope/whatever", "--spec")

	assert.Equal(t, got.Code, exitcode.NotFound, "stdout:\n%s", got.Stdout)
	assert.Check(t, is.Contains(got.Stderr, "may still answer"))
	assert.Check(t, is.Contains(got.Stderr, "bugsnag api --list-paths"))
	assert.Equal(t, h.Server.RequestCount(), 0)
}

// TestListPathsNamesTheCommandThatCoversAPath, so the catalogue steers back to the
// commands that render these responses rather than reading as an invitation to
// bypass them.
func TestListPathsNamesTheCommandThatCoversAPath(t *testing.T) {
	h := clitest.New(t)
	got := h.Run("api", "--list-paths")

	assert.Equal(t, got.Code, exitcode.OK, "stderr:\n%s", got.Stderr)

	for _, want := range []string{
		"/projects/{project_id}/errors\tbugsnag errors list\t",
		"/projects/{project_id}/errors/{error_id}\tbugsnag errors view\t",
		"/user/organizations\tbugsnag org list\t",
	} {
		assert.Check(t, is.Contains(got.Stdout, want))
	}
}
