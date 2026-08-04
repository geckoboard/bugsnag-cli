package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/geckoboard/bugsnag-cli/api/openapi"
	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/spf13/cobra"
)

// newAPICmd builds `bugsnag api <path>`.
//
// The typed commands cover the endpoints worth a view of their own; this reaches
// every other one without waiting for a view to be written for it. It goes through
// the same transport as they do, so the host allowlist, the retry policy, the
// pinned API version and the exit codes all still hold — which is what makes it a
// better answer than a curl with the token read out of the config file.
func newAPICmd(a *app) *cobra.Command {
	var (
		query     []string
		limit     int
		allPages  bool
		listPaths bool
		showSpec  bool
	)

	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Send a GET to any Data Access API path",
		Long: "Requests a path on the configured API host and prints the response as it came " +
			"back.\n\n" +
			"`{project_id}` and `{organization_id}` in the path are filled in from the " +
			"resolved project and the active organization, so a path can be pasted from the " +
			"API reference as it is written there. `{project}` and `{org}` are accepted as " +
			"short forms.\n\n" +
			"One request is made and its body written through unchanged. `--limit` and " +
			"`--all-pages` instead follow the `Link` header and concatenate the pages into " +
			"a single array, which only a list endpoint's response can be joined into.\n\n" +
			"`--list-paths` names every endpoint the API has and `--spec` prints what one " +
			"of them takes, both out of the vendored spec and neither costing a request. " +
			"Where a path has a command of its own, that command is the better way to read " +
			"it: it renders the response rather than printing it, and knows the caveats " +
			"that go with it.\n\n" +
			"Caveats:\n" +
			"  - Read-only, like the rest of this tool: the method is always GET.\n" +
			"  - The output is the API's own JSON whatever `--format` says. There is no " +
			"view for an endpoint this command does not know the shape of.\n" +
			"  - A full URL is accepted, but only its path and query are used. The host " +
			"comes from your configuration, so a URL from elsewhere cannot send your token " +
			"somewhere new.\n" +
			"  - `X-Total-Count` and the next page are reported on stderr, because a bare " +
			"JSON array says nothing about what it is a page of.",
		// The paths are quoted throughout: some shells expand braces, and the
		// query strings carry & and [ ].
		Example: strings.Join([]string{
			"  bugsnag api --list-paths",
			"  bugsnag api '/projects/{project_id}/releases' --spec",
			"  bugsnag api '/projects/{project_id}/releases' --query per_page=5",
			"  bugsnag api '/organizations/{organization_id}/collaborators' --all-pages",
			"  bugsnag api '/projects/{project_id}/errors' --query sort=first_seen --limit 5",
		}, "\n"),
		// The path is optional so --list-paths can stand alone: it is how you find
		// out what to pass, so it cannot require one.
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listPaths {
				return runAPIPaths(a)
			}
			if len(args) == 0 {
				return missingPath("no path given")
			}
			if showSpec {
				return runAPISpec(a, args[0])
			}
			return runAPI(cmd.Context(), a, args[0], apiOptions{
				Query:    query,
				Limit:    limit,
				AllPages: allPages,
			})
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&query, "query", nil,
		"query parameter as `key=value`, escaped for you (repeatable)")
	f.IntVar(&limit, "limit", 0, "stop after this many items, following pages to reach it")
	f.BoolVar(&allPages, "all-pages", false, "follow every page and concatenate them into one array")
	f.BoolVar(&listPaths, "list-paths", false, "list the paths this API offers, then stop")
	f.BoolVar(&showSpec, "spec", false, "print what the path takes, from the vendored spec, then stop")
	f.Bool("dry-run", false, "print the request and stop without sending it")
	return cmd
}

// runAPISpec prints the spec's own YAML for one path: its parameters with their
// types, defaults and examples, and the shape it answers with.
//
// It writes the fragment and nothing else, the way --json writes the response and
// nothing else, so it can be read by eye or piped into a YAML parser. The path is
// accepted with its ids still in it, since the path you want to know about is
// usually the one you just requested.
func runAPISpec(a *app, path string) error {
	fragment, found, err := openapi.Describe(strings.TrimSpace(path))
	if err != nil {
		return apierr.Wrap(apierr.KindInternal, err, "reading the vendored API spec")
	}
	if !found {
		return &apierr.Error{
			Kind:    apierr.KindNotFound,
			Message: fmt.Sprintf("the vendored spec describes no GET for %q", path),
			// The spec is a snapshot, so its silence is not proof the endpoint is
			// absent — only that nothing here can say what it takes.
			Hint: "list what it does describe with: bugsnag api --list-paths; " +
				"a path missing from it may still answer",
		}
	}

	_, err = io.WriteString(a.deps.Stdout, fragment)
	return err
}

// runAPIPaths lists what there is to ask for.
//
// It reads the vendored spec rather than the API, so it costs no request and
// works signed out. The commands are named alongside the paths they cover,
// because reaching one of those through here is a step backwards: the passthrough
// prints a response where a command would render it.
func runAPIPaths(a *app) error {
	endpoints, err := openapi.Readable()
	if err != nil {
		return apierr.Wrap(apierr.KindInternal, err, "reading the vendored API spec")
	}

	d := a.doc()
	d.H1("API paths")
	d.Text("Every endpoint this API offers a GET for. Pass one to `bugsnag api`.")

	tbl := d.Table("Path", "Command", "Summary")
	tbl.NeverTruncate(0)
	for _, e := range endpoints {
		tbl.Row(render.Code(e.Path), commandFor(e.Path), render.Escape(e.Summary))
	}
	tbl.Empty("The vendored spec describes no readable endpoint, which is a bug in this build.")
	tbl.Done()

	d.Footer("`{project_id}` and `{organization_id}` are filled in for you. " +
		"Where a `Command` is named, read it that way instead. " +
		"What a path takes: `bugsnag api '<path>' --spec`.")
	return emitDoc(a, d)
}

// commandsByPath names the command that covers a path, for the paths that have
// one. It is what keeps the catalogue from reading as an invitation to bypass the
// commands that render these responses properly.
var commandsByPath = map[string]string{
	"/user/organizations":                             "org list",
	"/organizations/{organization_id}/projects":       "project list",
	"/projects/{project_id}/errors":                   "errors list",
	"/projects/{project_id}/errors/{error_id}":        "errors view",
	"/projects/{project_id}/errors/{error_id}/events": "errors events",
	"/projects/{project_id}/events/{event_id}":        "errors event",
	"/projects/{project_id}/errors/{error_id}/pivots": "errors view",
	"/projects/{project_id}/errors/{error_id}/trend":  "errors view --trend",
	"/errors/{error_id}/latest_event":                 "errors view",
	"/projects/{project_id}/event_fields":             "errors list --list-filters",
}

func commandFor(path string) string {
	if cmd, ok := commandsByPath[path]; ok {
		return render.Code("bugsnag " + cmd)
	}
	return ""
}

func missingPath(why string) error {
	return &apierr.Error{
		Kind:    apierr.KindUsage,
		Message: why,
		Hint:    "list what there is to ask for with: bugsnag api --list-paths",
	}
}

// apiOptions is what one passthrough request was asked for.
type apiOptions struct {
	Query    []string
	Limit    int
	AllPages bool
}

func runAPI(ctx context.Context, a *app, target string, opts apiOptions) error {
	ref, err := a.apiTarget(ctx, target)
	if err != nil {
		return err
	}

	params, err := parseQueryParams(opts.Query)
	if err != nil {
		return err
	}

	// Paging is opt-in because the default is a passthrough: one request, and the
	// body exactly as it arrived. Joining pages means assuming the response is an
	// array, which is true of a list endpoint and of nothing else.
	paged := opts.AllPages || opts.Limit > 0
	limit := opts.Limit
	if opts.AllPages {
		limit = 0
	}

	req := bugsnagio.Request{
		Op:         "GET " + ref.Path,
		Build:      apiBuilder(ref),
		ExtraQuery: params,
		AllPages:   paged,
		Limit:      limit,
	}

	if done, err := a.maybeDryRun(req, &filters.Set{}); done || err != nil {
		return err
	}

	client, err := a.api()
	if err != nil {
		return err
	}

	sink := &metaSink{Sink: bugsnagio.NewJSONSink(a.deps.Stdout, paged)}
	if paged {
		err = client.Stream(ctx, req, sink)
	} else {
		err = client.One(ctx, req, sink)
	}
	if err != nil {
		return err
	}

	a.notePageState(sink.meta)
	return nil
}

// apiTarget reads what the caller typed into the path and query to request.
//
// The result carries no host: only the path and query survive, so a URL copied
// from the API reference or from an earlier response's `Link` header works without
// becoming a way to point the token at another host.
func (a *app) apiTarget(ctx context.Context, raw string) (*url.URL, error) {
	expanded, err := a.expandPlaceholders(ctx, strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(expanded)
	if err != nil {
		return nil, apierr.Wrap(apierr.KindUsage, err, "cannot read %q as an API path", raw)
	}

	if u.Path == "" {
		return nil, missingPath(fmt.Sprintf("%q names no path", raw))
	}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path, u.RawPath = "/"+u.Path, ""
	}

	return &url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}, nil
}

// expandPlaceholders substitutes the path parameters the CLI already knows.
//
// Each is resolved only when it appears, because the project costs a lookup and an
// endpoint that names neither should need neither.
func (a *app) expandPlaceholders(ctx context.Context, raw string) (string, error) {
	if hasPlaceholder(raw, projectPlaceholders) {
		p, err := a.project(ctx)
		if err != nil {
			return "", err
		}
		raw = replacePlaceholders(raw, projectPlaceholders, p.ID)
	}

	if hasPlaceholder(raw, orgPlaceholders) {
		orgID, err := a.requireOrg()
		if err != nil {
			return "", err
		}
		raw = replacePlaceholders(raw, orgPlaceholders, orgID)
	}

	return raw, nil
}

// The spec's own parameter names come first, so a path pasted from the reference
// needs no editing; the short forms are what anyone types by hand.
var (
	projectPlaceholders = []string{"{project_id}", "{project}"}
	orgPlaceholders     = []string{"{organization_id}", "{org}"}
)

func hasPlaceholder(raw string, names []string) bool {
	for _, name := range names {
		if strings.Contains(raw, name) {
			return true
		}
	}
	return false
}

func replacePlaceholders(raw string, names []string, value string) string {
	for _, name := range names {
		raw = strings.ReplaceAll(raw, name, value)
	}
	return raw
}

// apiBuilder resolves the path against the API host at request time, which is what
// keeps the host out of the caller's hands.
func apiBuilder(ref *url.URL) bugsnagio.BuildFunc {
	return func(server string) (*http.Request, error) {
		base, err := url.Parse(server)
		if err != nil {
			return nil, err
		}

		u := *base
		u.Path, u.RawPath, u.RawQuery = ref.Path, ref.RawPath, ref.RawQuery
		return http.NewRequest(http.MethodGet, u.String(), nil)
	}
}

// parseQueryParams reads the --query expressions in the order they were given.
func parseQueryParams(exprs []string) ([]filters.Param, error) {
	params := make([]filters.Param, 0, len(exprs))
	for _, raw := range exprs {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, &apierr.Error{
				Kind:    apierr.KindUsage,
				Message: fmt.Sprintf("cannot read --query %q", raw),
				Hint:    "use key=value",
			}
		}
		params = append(params, filters.Param{Key: key, Value: value})
	}
	return params, nil
}

// notePageState reports what the response headers said about the wider result set.
//
// A bare JSON array carries neither a total nor a cursor, so without this a page
// of thirty looks like the whole answer. It goes to stderr, leaving a pipeline
// reading stdout untouched, and names the next page as the command that fetches
// it rather than as a URL to reassemble.
func (a *app) notePageState(meta bugsnagio.Meta) {
	if meta.TotalCount >= 0 {
		note(a.deps.Stderr, "X-Total-Count: %d", meta.TotalCount)
	}
	if meta.NextURL != "" {
		note(a.deps.Stderr, "next page: bugsnag api '%s'", meta.NextURL)
	}
}

// metaSink records what a response said about itself on the items' way through, so
// the pagination headers can still be reported after the body has been written.
type metaSink struct {
	bugsnagio.Sink

	meta bugsnagio.Meta
}

func (s *metaSink) Close(meta bugsnagio.Meta) error {
	s.meta = meta
	return s.Sink.Close(meta)
}
