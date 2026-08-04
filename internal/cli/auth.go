package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/geckoboard/bugsnag-cli/internal/transport"
	"github.com/spf13/cobra"
)

func newAuthCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the API token and organization",
		RunE:  groupRunE,
	}
	cmd.AddCommand(newAuthLoginCmd(a), newAuthStatusCmd(a), newAuthLogoutCmd(a))
	return cmd
}

func newAuthLoginCmd(a *app) *cobra.Command {
	var tokenFlag string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API token and select an organization",
		Long: "Stores a Personal Auth Token and resolves the organization it belongs to.\n\n" +
			"Create a token at https://app.bugsnag.com/settings/my-account (Personal auth tokens).\n\n" +
			"No endpoint can list projects without an organization id, so login resolves it " +
			"once and caches it. The same request validates the token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLogin(cmd.Context(), a, tokenFlag)
		},
	}
	cmd.Flags().StringVar(&tokenFlag, "token", "", "API token (read from stdin when omitted)")
	return cmd
}

func runAuthLogin(ctx context.Context, a *app, token string) error {
	if token == "" {
		token = a.settings.Token
	}
	if token == "" {
		token = promptToken(a)
	}
	if token == "" {
		return &apierr.Error{
			Kind:    apierr.KindConfig,
			Message: "no API token given",
			Hint:    "pass --token",
		}
	}

	// Validating and enumerating are the same request: listing the user's
	// organizations proves the token works and produces the id we need.
	orgs, err := fetchOrganizations(ctx, a, token)
	if err != nil {
		return err
	}
	if len(orgs) == 0 {
		return apierr.New(apierr.KindConfig, "this token has access to no organizations")
	}

	chosen, err := chooseOrg(a, orgs)
	if err != nil {
		return err
	}

	cfgPath, err := a.configPath()
	if err != nil {
		return err
	}

	if err := config.Update(cfgPath, func(c *config.Config) error {
		c.Token = token
		c.Org = chosen
		return nil
	}); err != nil {
		return err
	}

	d := a.doc()
	d.H1("Signed in")
	d.Field("Organization", "%s", render.Escape(chosen.Name))
	d.Field("Config", "%s", render.Code(a.cfgPath))
	d.Footer("Next: `bugsnag errors list` in a repository with a Bugsnag project.")
	return emitDoc(a, d)
}

// fetchOrganizations lists the token's organizations using a client built for
// this one call, since the app's client is built from stored settings and the
// token being tested is not stored yet.
func fetchOrganizations(ctx context.Context, a *app, token string) ([]config.Org, error) {
	doer, err := transport.New(transport.Options{
		Token:     token,
		Hosts:     []string{a.settings.Host, config.DefaultHost, smartBearHost},
		UserAgent: "bugsnag-cli/" + a.deps.Version,
		Sleep:     a.deps.Sleep,
	})
	if err != nil {
		return nil, err
	}

	client, err := bugsnagio.NewClient(doer, a.settings.Host)
	if err != nil {
		return nil, err
	}

	sink := bugsnagio.NewTypedSink[bugsnagapi.OrganizationApiView]()
	req := bugsnagio.Request{
		Op: "list organizations",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListUserOrganizationsRequest(server, &bugsnagapi.ListUserOrganizationsParams{})
		},
	}
	if err := client.Stream(ctx, req, sink); err != nil {
		return nil, err
	}

	orgs := make([]config.Org, 0, len(sink.Items))
	for _, o := range sink.Items {
		orgs = append(orgs, config.Org{
			ID:   o.Id,
			Name: o.Name,
			Slug: o.Slug,
		})
	}
	return orgs, nil
}

// chooseOrg resolves which organization to use. One organization is used
// silently; several prompt on a terminal, and otherwise the list is printed with
// exit 3 so a script is told exactly what to pass.
func chooseOrg(a *app, orgs []config.Org) (config.Org, error) {
	if a.settings.OrgID != "" {
		for _, o := range orgs {
			if o.ID == a.settings.OrgID || o.Slug == a.settings.OrgID {
				return o, nil
			}
		}
		return config.Org{}, apierr.New(apierr.KindConfig,
			"this token has no access to organization %q", a.settings.OrgID)
	}

	if len(orgs) == 1 {
		return orgs[0], nil
	}

	if render.IsTerminal(a.deps.Stdout) && a.deps.Stdin != nil {
		return promptOrg(a, orgs)
	}

	d := a.doc()
	d.H1("Several organizations")
	d.Text("This token has access to more than one organization. Choose one with `--org`.")
	tbl := d.Table("Name", "ID", "Slug")
	tbl.NeverTruncate(1)
	for _, o := range orgs {
		tbl.Row(render.Escape(o.Name), render.Code(o.ID), render.Code(o.Slug))
	}
	tbl.Done()
	if err := emitDoc(a, d); err != nil {
		return config.Org{}, err
	}

	return config.Org{}, apierr.New(apierr.KindConfig,
		"several organizations available; pass --org")
}

func promptOrg(a *app, orgs []config.Org) (config.Org, error) {
	fmt.Fprintln(a.deps.Stderr, "Select an organization:")
	for i, o := range orgs {
		fmt.Fprintf(a.deps.Stderr, "  %d) %s\n", i+1, o.Name)
	}
	fmt.Fprint(a.deps.Stderr, "Number: ")

	line, err := bufio.NewReader(a.deps.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return config.Org{}, apierr.Wrap(apierr.KindConfig, err, "reading selection")
	}

	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &n); err != nil || n < 1 || n > len(orgs) {
		return config.Org{}, apierr.New(apierr.KindConfig, "not a valid selection: %q", strings.TrimSpace(line))
	}
	return orgs[n-1], nil
}

func promptToken(a *app) string {
	if a.deps.Stdin == nil {
		return ""
	}
	if render.IsTerminal(a.deps.Stdout) {
		fmt.Fprint(a.deps.Stderr, "Personal Auth Token: ")
	}

	line, err := bufio.NewReader(a.deps.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func newAuthStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the stored token and organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := a.doc()
			d.H1("Auth status")

			if a.settings.Token == "" {
				d.Text("Not signed in.")
				d.Footer("Run: `bugsnag auth login`")
				return emitDoc(a, d)
			}

			// Only the shape of the token, never the token.
			d.Field("Token", "%s (%d characters)", redactToken(a.settings.Token), len(a.settings.Token))
			if a.cfg.Org.Name != "" {
				d.Field("Organization", "%s", render.Escape(a.cfg.Org.Name))
			}
			d.Field("Host", "%s", render.Code(a.settings.Host))
			d.Field("Config", "%s", render.Code(a.cfgPath))
			d.Field("Linked repositories", "%d", len(a.cfg.Repos))
			return emitDoc(a, d)
		},
	}
}

func newAuthLogoutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, err := a.configPath()
			if err != nil {
				return err
			}
			if err := config.Update(cfgPath, func(c *config.Config) error {
				c.Token = ""
				c.Org = config.Org{}
				return nil
			}); err != nil {
				return err
			}

			d := a.doc()
			d.H1("Signed out")
			d.Text("The token and organization have been removed from %s.", render.Code(a.cfgPath))
			d.Footer("Linked repositories were kept; `bugsnag project unlink` removes those.")
			return emitDoc(a, d)
		},
	}
}

// redactToken shows only enough to tell two tokens apart.
func redactToken(token string) string {
	if len(token) <= 8 {
		return render.Code(strings.Repeat("*", len(token)))
	}
	return render.Code(token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:])
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
