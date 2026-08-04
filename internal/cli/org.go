package cli

import (
	"net/http"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/spf13/cobra"
)

func newOrgCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Show or change the active organization",
		RunE:  groupRunE,
	}
	cmd.AddCommand(newOrgListCmd(a), newOrgShowCmd(a), newOrgUseCmd(a))
	return cmd
}

func newOrgListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the organizations this token can see",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := bugsnagio.Request{
				Op: "list organizations",
				Build: func(server string) (*http.Request, error) {
					return bugsnagapi.NewListUserOrganizationsRequest(server,
						&bugsnagapi.ListUserOrganizationsParams{})
				},
			}
			return emitList(cmd.Context(), a, req, nil, viewOrganizations(a.cfg.Org.ID))
		},
	}
}

// viewOrganizations renders the organization list, marking the active one.
func viewOrganizations(activeID string) View[bugsnagapi.OrganizationApiView] {
	return func(d *render.Doc, orgs []bugsnagapi.OrganizationApiView, _ bugsnagio.Meta, _ render.Mode) {
		d.H1("Organizations")

		tbl := d.Table("", "Name", "ID", "Slug")
		tbl.NeverTruncate(2)
		for _, o := range orgs {
			marker := ""
			if o.Id == activeID {
				marker = "*"
			}
			tbl.Row(marker, render.Escape(o.Name), render.Code(o.Id), render.Code(o.Slug))
		}
		tbl.Empty("No organizations.")
		tbl.Done()

		if len(orgs) > 1 {
			d.Footer("`*` is the active organization. Change it with `bugsnag org use <id>`.")
		}
	}
}

func newOrgShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the active organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := a.doc()
			d.H1("Active organization")

			if a.settings.OrgID == "" {
				d.Text("No organization is configured.")
				d.Footer("Run: `bugsnag auth login`")
				return emitDoc(a, d)
			}

			if a.cfg.Org.Name != "" {
				d.Field("Name", "%s", render.Escape(a.cfg.Org.Name))
			}
			d.Field("ID", "%s", render.Code(a.settings.OrgID))
			if a.cfg.Org.Slug != "" {
				d.Field("Slug", "%s", render.Code(a.cfg.Org.Slug))
			}
			return emitDoc(a, d)
		},
	}
}

func newOrgUseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "use <id-or-slug>",
		Short: "Set the active organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := args[0]

			orgs, err := fetchOrganizations(cmd.Context(), a, a.settings.Token)
			if err != nil {
				return err
			}

			var chosen config.Org
			for _, o := range orgs {
				if o.ID == want || o.Slug == want {
					chosen = o
					break
				}
			}
			if chosen.ID == "" {
				return apierr.New(apierr.KindNotFound,
					"no organization matching %q", want)
			}

			cfgPath, err := a.configPath()
			if err != nil {
				return err
			}
			if err := config.Update(cfgPath, func(c *config.Config) error {
				c.Org = chosen
				return nil
			}); err != nil {
				return err
			}

			d := a.doc()
			d.H1("Organization changed")
			d.Field("Name", "%s", render.Escape(chosen.Name))
			d.Field("ID", "%s", render.Code(chosen.ID))
			// A cached project belongs to the organization it was resolved in, so
			// changing organization means those entries no longer apply.
			d.Footer("Cached project links for the previous organization are ignored, not deleted.")
			return emitDoc(a, d)
		},
	}
}
