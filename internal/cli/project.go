package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/geckoboard/bugsnag-cli/internal/repoid"
)

func newProjectCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Show, list and link Bugsnag projects",
		RunE:  groupRunE,
	}
	cmd.AddCommand(
		newProjectListCmd(a),
		newProjectShowCmd(a),
		newProjectLinkCmd(a),
		newProjectUnlinkCmd(a),
	)
	return cmd
}

// resolvedProject is a project the CLI decided to act on, and how it decided.
type resolvedProject struct {
	config.Project

	Source   config.Source
	Identity repoid.Identity
}

// DashboardURL composes a dashboard link for an error.
//
// Errors and events carry no dashboard URL: their url and project_url fields
// point at api.bugsnag.com. Only projects have html_url, which is why it is
// cached and links are built from it.
func (p resolvedProject) DashboardURL(errorID, eventID string) string {
	if p.HTMLURL == "" || errorID == "" {
		return ""
	}
	u := fmt.Sprintf("%s/errors/%s", p.HTMLURL, errorID)
	if eventID != "" {
		u += "?event_id=" + eventID
	}
	return u
}

// project resolves which project to act on.
//
// The order is: --project, then the repository's cached entry, then
// autodetection from the git remote. The cached entry is only used
// while its organization still matches the active one, so switching organization
// cannot silently return another organization's project.
func (a *app) project(ctx context.Context) (resolvedProject, error) {
	if a.settings.ProjectID != "" {
		p := resolvedProject{
			Project: config.Project{ID: a.settings.ProjectID},
			Source:  a.settings.ProjectSource,
		}
		// An explicitly named project still needs its html_url for dashboard
		// links, and a slug (in any case) has to be resolved to its id. A project
		// that genuinely does not exist is reported as such; only a transient
		// lookup failure falls back to using the value as an id directly, so a
		// correct id still works when the project search itself hiccups.
		enriched, err := a.lookupProject(ctx, a.settings.ProjectID)
		switch {
		case err == nil:
			enriched.Source = p.Source
			return enriched, nil
		case apierr.KindOf(err) == apierr.KindNotFound:
			return resolvedProject{}, err
		default:
			return p, nil
		}
	}

	orgID, err := a.requireOrg()
	if err != nil {
		return resolvedProject{}, err
	}

	identity, ok := a.repoIdentity(ctx)
	if !ok {
		return resolvedProject{}, &apierr.Error{
			Kind:    apierr.KindConfig,
			Message: "cannot tell which project to use: this directory has no usable git remote",
			Hint:    "pass --project <id>, or run: bugsnag project link <id>",
		}
	}

	if cached, ok := a.cfg.Repos[string(identity)]; ok && cached.OrgID == orgID {
		return resolvedProject{
			Project: config.Project{
				ID:      cached.ProjectID,
				Name:    cached.ProjectName,
				Slug:    cached.ProjectSlug,
				HTMLURL: cached.HTMLURL,
			},
			Source:   config.SourceConfig,
			Identity: identity,
		}, nil
	}

	return a.autodetectProject(ctx, orgID, identity)
}

// repoIdentity canonicalises the current repository's remote.
func (a *app) repoIdentity(ctx context.Context) (repoid.Identity, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}

	remotes, err := a.deps.Git.Remotes(ctx, dir)
	if err != nil || len(remotes) == 0 {
		return "", false
	}

	identity, _, ok := repoid.FromRemotes(remotes)
	return identity, ok
}

// autodetectProject searches the organization for a project matching the
// repository name.
func (a *app) autodetectProject(
	ctx context.Context, orgID string, identity repoid.Identity,
) (resolvedProject, error) {
	repoName := identity.RepoName()

	projects, err := a.searchProjects(ctx, orgID, repoName)
	if err != nil {
		return resolvedProject{}, err
	}

	match, ok := matchProject(repoName, projects)
	if !ok {
		return resolvedProject{}, a.explainAmbiguity(repoName, projects)
	}

	resolved := resolvedProject{
		Project:  match,
		Source:   config.SourceConfig,
		Identity: identity,
	}

	if err := a.linkRepo(identity, orgID, resolved); err != nil {
		return resolvedProject{}, err
	}

	// The note goes to stderr so that --json stdout stays pipeable.
	fmt.Fprintf(a.deps.Stderr, "note: using project %s (%s) for %s; name matches the repository\n",
		match.Name, match.ID, identity)

	return resolved, nil
}

// searchProjects asks the API for projects matching q.
func (a *app) searchProjects(ctx context.Context, orgID, q string) ([]config.Project, error) {
	client, err := a.api()
	if err != nil {
		return nil, err
	}

	perPage := 100
	params := &bugsnagapi.GetOrganizationProjectsParams{PerPage: &perPage}
	if q != "" {
		params.Q = &q
	}

	sink := bugsnagio.NewTypedSink[bugsnagapi.ProjectApiView]()
	req := bugsnagio.Request{
		Op: "list projects",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewGetOrganizationProjectsRequest(server, orgID, params)
		},
		AllPages: true,
	}
	if err := client.Stream(ctx, req, sink); err != nil {
		return nil, err
	}

	out := make([]config.Project, 0, len(sink.Items))
	for _, p := range sink.Items {
		out = append(out, config.Project{
			ID:      deref(p.Id),
			Name:    deref(p.Name),
			Slug:    deref(p.Slug),
			HTMLURL: deref(p.HtmlUrl),
		})
	}
	return out, nil
}

// lookupProject finds one project by id or slug within the active organization.
//
// The config caches the organization's projects, so a repeat --project resolves
// with no API call. Only a cache miss lists them, and that refreshes the cache,
// so a project added since is picked up.
func (a *app) lookupProject(ctx context.Context, idOrSlug string) (resolvedProject, error) {
	orgID, err := a.requireOrg()
	if err != nil {
		return resolvedProject{}, err
	}

	if p, ok := a.cachedProject(orgID, idOrSlug); ok {
		return p, nil
	}

	projects, err := a.searchProjects(ctx, orgID, "")
	if err != nil {
		return resolvedProject{}, err
	}
	a.cacheOrgProjects(orgID, projects)

	// The id or slug is matched case-insensitively, so a slug typed in any case —
	// or copied from a display name — still resolves, the same way autodetect
	// matches a repository name.
	target := strings.ToLower(strings.TrimSpace(idOrSlug))
	for _, p := range projects {
		if strings.ToLower(p.ID) == target || strings.ToLower(p.Slug) == target {
			return resolvedProject{Project: p}, nil
		}
	}
	return resolvedProject{}, &apierr.Error{
		Kind:    apierr.KindNotFound,
		Message: fmt.Sprintf("no project matching %q in this organization", idOrSlug),
		Hint:    "list ids and slugs with: bugsnag project list",
	}
}

// cachedProject resolves idOrSlug from the config's project cache, without an API
// call. It reports false when the cache is empty, belongs to another
// organization, or has no matching entry.
func (a *app) cachedProject(orgID, idOrSlug string) (resolvedProject, bool) {
	if a.cfg.ProjectsOrgID != orgID || len(a.cfg.Projects) == 0 {
		return resolvedProject{}, false
	}

	target := strings.ToLower(strings.TrimSpace(idOrSlug))
	if p, ok := a.cfg.Projects[target]; ok {
		return projectFromCache(p), true
	}
	// The map is keyed by slug, so an id is matched by scanning the values.
	for _, p := range a.cfg.Projects {
		if strings.ToLower(p.ID) == target {
			return projectFromCache(p), true
		}
	}
	return resolvedProject{}, false
}

// cacheOrgProjects records the organization's projects in the config, keyed by
// slug, so a later --project resolves without re-listing. It is best-effort: a
// cache that cannot be written just means the next run lists again. It updates
// the in-memory config too, so a resolution within the same run is warm.
func (a *app) cacheOrgProjects(orgID string, projects []config.Project) {
	cache := make(map[string]config.Project, len(projects))
	for _, p := range projects {
		slug := strings.ToLower(strings.TrimSpace(p.Slug))
		if slug == "" {
			continue
		}
		cache[slug] = p
	}

	a.cfg.Projects, a.cfg.ProjectsOrgID = cache, orgID

	cfgPath, err := a.configPath()
	if err != nil {
		return
	}
	_ = config.Update(cfgPath, func(c *config.Config) error {
		c.Projects, c.ProjectsOrgID = cache, orgID
		return nil
	})
}

func projectFromCache(p config.Project) resolvedProject {
	return resolvedProject{Project: p}
}

// explainAmbiguity prints the projects it saw and fails with a config error, so
// a script gets the list it needs rather than a guess.
func (a *app) explainAmbiguity(repoName string, projects []config.Project) error {
	d := a.doc()
	d.H1("Cannot tell which project matches `%s`", repoName)

	if len(projects) == 0 {
		d.Text("No projects in this organization matched.")
		d.Footer("List them with `bugsnag project list`, then `bugsnag project link <id>`.")
	} else {
		d.Text("No project's name or slug matched exactly. " +
			"Pass one with `--project`, or record it with `bugsnag project link <id>`.")
		tbl := d.Table("Name", "ID", "Slug")
		tbl.NeverTruncate(1)
		for _, p := range projects[:min(len(projects), 10)] {
			tbl.Row(render.Escape(p.Name), render.Code(p.ID), render.Code(p.Slug))
		}
		tbl.Done()
	}
	if err := emitDoc(a, d); err != nil {
		return err
	}

	return apierr.New(apierr.KindConfig, "could not determine the project for %q", repoName)
}

// linkRepo records a resolution centrally. Nothing is written into the
// repository being inspected.
func (a *app) linkRepo(identity repoid.Identity, orgID string, p resolvedProject) error {
	cfgPath, err := a.configPath()
	if err != nil {
		return err
	}
	return config.Update(cfgPath, func(c *config.Config) error {
		if c.Repos == nil {
			c.Repos = map[string]config.Repo{}
		}
		c.Repos[string(identity)] = config.Repo{
			ProjectID:   p.ID,
			ProjectName: p.Name,
			ProjectSlug: p.Slug,
			HTMLURL:     p.HTMLURL,
			OrgID:       orgID,
			ResolvedAt:  a.deps.Now().UTC().Truncate(time.Second),
		}
		return nil
	})
}

func newProjectListCmd(a *app) *cobra.Command {
	var query string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects in the active organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			orgID, err := a.requireOrg()
			if err != nil {
				return err
			}

			perPage := 100
			params := &bugsnagapi.GetOrganizationProjectsParams{PerPage: &perPage}
			if query != "" {
				params.Q = &query
			}

			req := bugsnagio.Request{
				Op: "list projects",
				Build: func(server string) (*http.Request, error) {
					return bugsnagapi.NewGetOrganizationProjectsRequest(server, orgID, params)
				},
				AllPages: true,
			}
			return emitList(cmd.Context(), a, req, nil, viewProjects)
		},
	}
	cmd.Flags().StringVarP(&query, "query", "q", "", "filter projects by name")
	return cmd
}

func viewProjects(d *render.Doc, projects []bugsnagapi.ProjectApiView, _ bugsnagio.Meta, _ render.Mode) {
	d.H1("Projects")

	tbl := d.Table("Name", "Slug", "ID", "Open errors")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight)
	tbl.NeverTruncate(2)
	for _, p := range projects {
		open := ""
		if p.OpenErrorCount != nil {
			open = render.Count(*p.OpenErrorCount)
		}
		tbl.Row(render.Escape(deref(p.Name)), render.Code(deref(p.Slug)), render.Code(deref(p.Id)), open)
	}
	tbl.Empty("No projects.")
	tbl.Done()
}

func newProjectShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show which project this repository resolves to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.project(cmd.Context())
			if err != nil {
				return err
			}

			d := a.doc()
			d.H1("Project")
			if p.Name != "" {
				d.Field("Name", "%s", render.Escape(p.Name))
			}
			d.Field("ID", "%s", render.Code(p.ID))
			if p.Slug != "" {
				d.Field("Slug", "%s", render.Code(p.Slug))
			}
			if p.Identity != "" {
				d.Field("Repository", "%s", render.Code(string(p.Identity)))
			}
			d.Field("Resolved from", "%s", p.Source)
			if p.HTMLURL != "" {
				d.Field("Dashboard", "%s", p.HTMLURL)
			}

			if p.Identity != "" {
				if cached, ok := a.cfg.Repos[string(p.Identity)]; ok && !cached.ResolvedAt.IsZero() {
					d.Field("Linked", "%s", a.mode().Timestamp(cached.ResolvedAt.Format(time.RFC3339)))
				}
			}
			return emitDoc(a, d)
		},
	}
}

func newProjectLinkCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "link <project-id-or-slug>",
		Short: "Record which project this repository belongs to",
		Long: "Records the project for this repository in the central config, so autodetection " +
			"does not run again. Nothing is written into the repository.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, err := a.requireOrg()
			if err != nil {
				return err
			}

			identity, ok := a.repoIdentity(cmd.Context())
			if !ok {
				return &apierr.Error{
					Kind:    apierr.KindConfig,
					Message: "this directory has no usable git remote, so there is nothing to link",
					Hint:    "use --project per command instead",
				}
			}

			p, err := a.lookupProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.linkRepo(identity, orgID, p); err != nil {
				return err
			}

			d := a.doc()
			d.H1("Linked")
			d.Field("Repository", "%s", render.Code(string(identity)))
			d.Field("Project", "%s (%s)", render.Escape(p.Name), render.Code(p.ID))
			return emitDoc(a, d)
		},
	}
}

func newProjectUnlinkCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Forget the project recorded for this repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			identity, ok := a.repoIdentity(cmd.Context())
			if !ok {
				return apierr.New(apierr.KindConfig,
					"this directory has no usable git remote")
			}

			cfgPath, err := a.configPath()
			if err != nil {
				return err
			}

			var existed bool
			if err := config.Update(cfgPath, func(c *config.Config) error {
				_, existed = c.Repos[string(identity)]
				delete(c.Repos, string(identity))
				return nil
			}); err != nil {
				return err
			}

			d := a.doc()
			d.H1("Unlinked")
			if existed {
				d.Text("%s is no longer linked to a project.", render.Code(string(identity)))
			} else {
				d.Text("%s was not linked to a project.", render.Code(string(identity)))
			}
			return emitDoc(a, d)
		},
	}
}
