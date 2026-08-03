package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/render"
	"github.com/geckoboard/bugsnag-cli/internal/transport"
)

// GitRunner reads a repository's remotes.
type GitRunner interface {
	// Remotes returns remote name to URL for the repository containing dir.
	Remotes(ctx context.Context, dir string) (map[string]string, error)
}

// newRootCmd builds the command tree.
func newRootCmd(d IO) *cobra.Command {
	d = withDefaults(d)
	a := &app{deps: d}

	root := &cobra.Command{
		Use:   "bugsnag",
		Short: "Query Bugsnag errors and events from the terminal",
		Long: "bugsnag reads the Bugsnag Data Access API.\n\n" +
			"Output is text: laid out with gridlines and colour for a terminal, and " +
			"tab-separated when piped, so the same output serves a person and a script. " +
			"Use --json for the API's own JSON values, unchanged.",
		Version:       d.Version,
		SilenceUsage:  true,
		SilenceErrors: true,

		// Commands return errors and Main maps them to exit codes; cobra must
		// not print or exit on its own.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.load(cmd)
		},
	}

	root.SetOut(d.Stdout)

	flags := root.PersistentFlags()
	flags.String("format", "text", "output format: text or json")
	flags.String("time", "auto", "timestamp style: auto, relative, local or raw")
	flags.String("project", "", "project id or slug (default: detected from the git remote)")
	flags.String("org", "", "organization id")
	flags.String("host", "", "API host (default: https://api.bugsnag.com)")
	flags.Bool("json", false, "shorthand for --format json")

	// A plain `version` subcommand as well as --version, because that is what
	// scripts reach for first.
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(d.Stdout, d.Version)
			return nil
		},
	})

	root.AddCommand(
		newAuthCmd(a),
		newOrgCmd(a),
		newProjectCmd(a),
		newErrorsCmd(a),
		newViewCmd(a),
		newAPICmd(a),
	)

	return root
}

// app carries the resolved settings and lazily built API client for one run.
type app struct {
	deps IO

	settings config.Settings
	cfg      config.Config

	// cfgPath is where the config file lives, and cfgPathErr is why it could not
	// be located. Locating it is deferred so `version` and `--help` work with no
	// HOME set.
	cfgPath    string
	cfgPathErr error

	// flags is the running command's flag set, so the filter flags can be read
	// without threading every value through each command function.
	flags *pflag.FlagSet

	// urlFilters replaces the flag-derived filter set when a pasted dashboard
	// URL carried filter state. `view` registers no filter flags of its own, so
	// the two can never both be set.
	urlFilters *filters.Set

	client *bugsnagio.Client
}

// load resolves settings for the command about to run.
func (a *app) load(cmd *cobra.Command) error {
	a.flags = cmd.Flags()

	// Not being able to locate a config directory is not fatal here: `version`
	// and `--help` must work in a bare environment with no HOME. The error is
	// held and returned by configPath, which every command that actually needs
	// the file goes through.
	path, err := config.Path()
	a.cfgPath, a.cfgPathErr = path, err

	var cfg config.Config
	if err == nil {
		if cfg, err = config.Load(path); err != nil {
			return err
		}
	}
	a.cfg = cfg

	// --json is a shorthand, resolved before precedence so it behaves exactly
	// like --format json.
	flags := cmd.Root().PersistentFlags()
	if jsonShorthand, err := flags.GetBool("json"); err == nil && jsonShorthand {
		if err := flags.Set("format", "json"); err != nil {
			return apierr.Wrap(apierr.KindInternal, err, "applying --json")
		}
	}

	settings, err := config.Resolve(config.Resolver{Flags: flags, Config: cfg})
	if err != nil {
		return err
	}
	a.settings = settings

	// A config file left world-readable is worth saying out loud, but not worth
	// refusing to run over.
	if a.cfgPathErr == nil {
		if err := config.Check(path); err != nil {
			warnf(a.deps.Stderr, "%s", err)
		}
	}

	return nil
}

// configPath returns the config file path, or the reason there is not one.
func (a *app) configPath() (string, error) {
	if a.cfgPathErr != nil {
		return "", a.cfgPathErr
	}
	return a.cfgPath, nil
}

// api returns the API client, building it on first use so commands that do not
// call the API work without a token.
func (a *app) api() (*bugsnagio.Client, error) {
	if a.client != nil {
		return a.client, nil
	}

	doer, err := transport.New(transport.Options{
		Token: a.settings.Token,
		// The configured host plus the two documented API hosts. The token
		// cannot be sent anywhere else, whatever a Link header says.
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
	a.client = client
	return client, nil
}

// mode builds the render mode for this run.
func (a *app) mode() render.Mode {
	m := render.DetectMode(a.deps.Stdout, a.deps.Now())
	m.Time = a.settings.TimeStyle
	return m
}

// doc starts a document in the current mode.
func (a *app) doc() *render.Doc { return render.New(a.mode()) }

// requireOrg returns the active organization id or explains how to get one.
func (a *app) requireOrg() (string, error) {
	if a.settings.OrgID != "" {
		return a.settings.OrgID, nil
	}
	return "", &apierr.Error{
		Kind:    apierr.KindConfig,
		Message: "no organization configured",
		Hint:    "run: bugsnag auth login",
	}
}

// groupRunE is the Run for a command that only groups subcommands.
//
// Without it cobra prints the parent's help and exits 0 for an unknown
// subcommand, so `bugsnag project nonsense` looks like it succeeded. Exit 2 for a
// usage error is a documented part of this tool's contract.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return apierr.New(apierr.KindUsage,
			"unknown %s subcommand %q", cmd.Name(), args[0])
	}
	return cmd.Help()
}

const smartBearHost = "https://api.bugsnag.smartbear.com"

func withDefaults(d IO) IO {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Stdout == nil {
		d.Stdout = io.Discard
	}
	if d.Stderr == nil {
		d.Stderr = io.Discard
	}
	if d.Git == nil {
		d.Git = execGit{}
	}
	return d
}
