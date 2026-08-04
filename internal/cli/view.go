package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/dashboardurl"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/view"
	"github.com/spf13/cobra"
)

// newViewCmd builds `bugsnag view <url>`.
//
// It is a router, not another view. The URL already says which of the three
// views is wanted, so asking the caller to pick a subcommand as well would be
// asking them to repeat what they just pasted. Every path here delegates to the
// same code as the equivalent `errors` command, so there is no second renderer
// and no second fetch path to drift.
func newViewCmd(a *app) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "view <dashboard-url>",
		Short: "Open whatever a pasted Bugsnag dashboard URL points at",
		Long: "Takes a URL copied from the Bugsnag dashboard and shows what it names, so " +
			"an investigation can be handed over by pasting the address bar.\n\n" +
			"What you get depends on the URL: an event id in it shows that occurrence, " +
			"an error shows the error and its latest stack trace, and a project's " +
			"error list shows the inbox. Filter state in the URL is applied where it " +
			"applies.\n\n" +
			"The equivalent long-form command is written to stderr, so the next step — " +
			"with `--frames full`, `--metadata`, `--limit` or anything else those " +
			"commands take — is always to hand.\n\n" +
			"Caveats:\n" +
			"  - Only a dashboard URL is accepted, never a bare id: an error id and an " +
			"event id are both 24 hex characters and cannot be told apart.\n" +
			"  - The URL's host is checked and then discarded. The API host always " +
			"comes from your configuration, so a pasted URL cannot send your token " +
			"anywhere new.\n" +
			"  - The project comes from the URL for this run only, and is never written " +
			"to the config file. `bugsnag project link` is how that is made permanent.\n" +
			"  - Filter parameters are applied: a field with a flag of its own is " +
			"translated to it, and the rest are passed through as `--filter`. Query " +
			"parameters that are not filters at all are named on stderr rather than " +
			"silently ignored.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runView(cmd.Context(), a, args[0], all)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "include every optional section")
	return cmd
}

func runView(ctx context.Context, a *app, raw string, all bool) error {
	if !dashboardurl.IsURL(raw) {
		return apierr.New(apierr.KindUsage,
			"%s is not a dashboard URL; for an id use `bugsnag errors view <id>` "+
				"or `bugsnag errors event <id>`", raw)
	}

	ref, err := dashboardurl.Parse(raw)
	if err != nil {
		return apierr.Wrap(apierr.KindUsage, err, "cannot read that URL")
	}

	if err := a.adoptRef(ref); err != nil {
		return err
	}

	set, applied, err := a.filtersFromRef(ref)
	if err != nil {
		return err
	}

	// Filters describe a search, so they mean nothing when the URL names one
	// error or one event. Saying so beats applying them somewhere they have no
	// effect, and beats dropping them without a word.
	target := viewTarget(ref)
	if target != targetList && len(applied) > 0 {
		warnf(a.deps.Stderr, "the URL's filters (%s) do not apply to a single %s; ignoring them",
			strings.Join(applied, " "), target)
		set, applied = &filters.Set{}, nil
	}
	if len(ref.Ignored) > 0 {
		warnf(a.deps.Stderr, "ignored URL parameters: %s", strings.Join(ref.Ignored, ", "))
	}
	a.urlFilters = set

	note(a.deps.Stderr, "equivalent: %s", equivalentCommand(ref, target, applied, all))

	switch target {
	case targetEvent:
		return runErrorsEvent(ctx, a, ref.EventID, eventInputFor(all))
	case targetError:
		return runErrorsView(ctx, a, ref.ErrorID, errorsViewOptions{
			Stacktrace: view.StacktraceOptions{
				Scope:     scopeFor(all),
				MaxFrames: maxFramesFor(scopeFor(all)),
			},
			Trend:     all,
			Summaries: true,
			Stats:     all,
		})
	default:
		return runErrorsList(ctx, a, "last_seen", false, a.defaultLimit())
	}
}

// viewTarget is which view a URL names. The most specific thing the URL carries
// wins, because that is what the person handing over was looking at.
type target int

const (
	targetList target = iota
	targetError
	targetEvent
)

func (t target) String() string {
	switch t {
	case targetError:
		return "error"
	case targetEvent:
		return "event"
	}
	return "list"
}

func viewTarget(ref dashboardurl.Ref) target {
	switch {
	case ref.EventID != "":
		return targetEvent
	case ref.ErrorID != "":
		return targetError
	}
	return targetList
}

// adoptRef points this run at the project the URL names.
//
// The organization has to match: the configured token may have no access to
// another one, and quietly querying elsewhere would be worse than stopping. The
// project applies to this run only — persisting it is what `project link` is for.
func (a *app) adoptRef(ref dashboardurl.Ref) error {
	if a.settings.ProjectID != "" && !strings.EqualFold(a.settings.ProjectID, ref.ProjectSlug) {
		return apierr.New(apierr.KindUsage,
			"the URL names project %s but --project says %s; drop one of them",
			ref.ProjectSlug, a.settings.ProjectID)
	}

	if slug := a.cfg.Org.Slug; slug != "" && !strings.EqualFold(slug, ref.OrgSlug) {
		return &apierr.Error{
			Kind: apierr.KindConfig,
			Message: fmt.Sprintf("that URL is in the %s organization, but %s is configured",
				ref.OrgSlug, slug),
			Hint: "switch with: bugsnag org use <id>",
		}
	}

	a.settings.ProjectID = ref.ProjectSlug
	a.settings.ProjectSource = config.SourceURL
	return nil
}

// filtersFromRef translates the URL's filter state into the same conditions the
// flags produce, and returns them rendered as flags for the equivalent command.
//
// A field backed by a curated flag is translated to that flag. Everything else is
// forwarded as an equality condition rather than dropped: the dashboard only puts
// a filter in the URL because it applied one, and the fields it names are the
// project's own — a custom metaData field can appear in no static list. The rows
// that come back are what catches a field the API ignores, so nothing has to be
// refused up front to stay honest.
func (a *app) filtersFromRef(ref dashboardurl.Ref) (*filters.Set, []string, error) {
	set := &filters.Set{}
	var applied []string

	for _, f := range ref.Filters {
		curated, ok := curatedByField(f.Field)
		if !ok {
			// Passed through raw. A relative time is left as the dashboard wrote
			// it, since the API reads 30d here exactly as it reads the absolute
			// form the curated flags send.
			set.Add(f.Field, filters.OpEq, f.Value)
			applied = append(applied, fmt.Sprintf("--filter '%s=%s'", f.Field, f.Value))
			continue
		}

		switch curated.kind {
		case filterTime:
			value, err := resolveTime(f.Value, a.deps.Now())
			if err != nil {
				return nil, nil, apierr.Wrap(apierr.KindUsage, err,
					"the URL's %s filter", f.Field)
			}
			set.Add(curated.field, curated.op, value)
			applied = append(applied, fmt.Sprintf("--%s %s", curated.flag, f.Value))

		case filterBool:
			b, err := strconv.ParseBool(f.Value)
			if err != nil {
				a.warnUnreadable(f)
				continue
			}
			set.Add(curated.field, filters.OpEq, strconv.FormatBool(b))
			applied = append(applied, fmt.Sprintf("--%s=%t", curated.flag, b))

		default:
			set.AddValue(curated.field, f.Value)
			applied = append(applied, fmt.Sprintf("--%s %s", curated.flag, f.Value))
		}
	}

	return set, applied, nil
}

// warnUnreadable covers a URL value the flag it maps to cannot express, which is
// the one case still dropped rather than forwarded: --unhandled is a boolean, and
// there is nothing truthful to send for a value that is not one.
func (a *app) warnUnreadable(f dashboardurl.Filter) {
	warnf(a.deps.Stderr,
		"the URL filters on %s=%s, which is not a value that field takes; not applied. "+
			"See: bugsnag errors list --list-filters", f.Field, f.Value)
}

func curatedByField(field string) (curatedFilter, bool) {
	for _, c := range curatedFilters {
		if c.field == field {
			return c, true
		}
	}
	return curatedFilter{}, false
}

// equivalentCommand renders the long-form command this run is standing in for.
//
// It goes to stderr rather than into the document, so `view <url>` and the
// command it names produce byte-identical stdout. That is the property that stops
// this router from becoming a second way to render the same thing — and it is
// what makes the next, more specific command something an agent can read off
// rather than work out.
func equivalentCommand(ref dashboardurl.Ref, t target, applied []string, all bool) string {
	parts := []string{"bugsnag"}

	switch t {
	case targetEvent:
		parts = append(parts, "errors", "event", ref.EventID)
	case targetError:
		parts = append(parts, "errors", "view", ref.ErrorID)
	default:
		parts = append(parts, "errors", "list")
	}

	parts = append(parts, "--project", ref.ProjectSlug)
	parts = append(parts, applied...)
	if all {
		parts = append(parts, "--all")
	}
	return strings.Join(parts, " ")
}

func eventInputFor(all bool) view.EventDetailInput {
	in := view.EventDetailInput{
		Stacktrace: view.StacktraceOptions{Scope: scopeFor(all)},
		Redact:     true,
	}
	if all {
		in.ShowMetadata, in.ShowRequest, in.ShowBreadcrumbs, in.ShowThreads = true, true, true, true
	}
	return in
}

func scopeFor(all bool) view.FrameScope {
	if all {
		return view.ScopeFull
	}
	return view.ScopeProject
}

// note writes an informational line to stderr, in the same shape as the
// project-autodetect note: never on stdout, so a --json pipeline stays clean.
func note(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "note: "+format+"\n", args...)
}
