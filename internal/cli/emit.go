package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// View renders items into a document. Views live in internal/view and are pure
// functions: no I/O, no clock, no terminal, so they are golden-testable without
// a pty or a network.
type View[T any] func(*render.Doc, []T, bugsnagio.Meta, render.Mode)

// emitList runs a list endpoint and writes it.
//
// This is the only way a list result reaches stdout, and it always writes the
// pagination footer. That makes two mistakes structurally unreachable: a command
// file cannot forget a "if format == table" branch, and pagination state cannot
// be dropped on one of the two output paths.
func emitList[T any](ctx context.Context, a *app, req bugsnagio.Request, view View[T]) error {
	client, err := a.api()
	if err != nil {
		return err
	}

	if a.settings.Format == render.FormatJSON {
		sink := bugsnagio.NewJSONSink(a.deps.Stdout, true)
		return client.Stream(ctx, req, sink)
	}

	sink := bugsnagio.NewTypedSink[T]()
	if err := client.Stream(ctx, req, sink); err != nil {
		return err
	}

	d := a.doc()
	m := d.Mode()
	view(d, sink.Items, sink.Meta, m)
	writeListFooter(d, sink.Meta, len(sink.Items)+len(sink.Degraded))

	reportDegraded(a.deps.Stderr, sink, d)
	return render.Write(a.deps.Stdout, d, render.FormatText)
}

// emitOne runs a single-object endpoint and writes it.
func emitOne[T any](ctx context.Context, a *app, req bugsnagio.Request, view func(*render.Doc, T, render.Mode)) error {
	client, err := a.api()
	if err != nil {
		return err
	}

	if a.settings.Format == render.FormatJSON {
		sink := bugsnagio.NewJSONSink(a.deps.Stdout, false)
		return client.One(ctx, req, sink)
	}

	sink := bugsnagio.NewTypedSink[T]()
	if err := client.One(ctx, req, sink); err != nil {
		return err
	}

	d := a.doc()
	if len(sink.Items) == 0 {
		// The object was readable as JSON but not as the generated type. Saying
		// so and pointing at --json is the honest outcome; failing outright would
		// throw away a response the caller can still get at.
		reportDegraded(a.deps.Stderr, sink, d)
		d.H1("Could not display this response")
		d.Text("The response did not match the shape this version of the CLI expects.")
		d.Footer("Use --json to see the raw response.")
		return render.Write(a.deps.Stdout, d, render.FormatText)
	}

	view(d, sink.Items[0], d.Mode())
	reportDegraded(a.deps.Stderr, sink, d)
	return render.Write(a.deps.Stdout, d, render.FormatText)
}

// emitDoc writes a document built without an API call, such as help or
// `project show`.
func emitDoc(a *app, d *render.Doc) error {
	return render.Write(a.deps.Stdout, d, render.FormatText)
}

// writeListFooter states what was shown against what exists.
//
// X-Total-Count is present on every list endpoint v1 uses, so this can be
// truthful. Without it, thirty rows look like the whole answer when six thousand
// exist.
func writeListFooter(d *render.Doc, meta bugsnagio.Meta, shown int) {
	switch {
	case shown == 0:
		// The view already says there was nothing; a count adds nothing.
		return
	case meta.TotalCount > shown:
		// --all-pages is not suggested here: it fetches the entire result set,
		// which for a large error list is a lot of requests and rarely what is
		// wanted. Raising --limit is the measured way to see more.
		d.Footer("Showing %s of %s · raise `--limit` for more",
			render.Count(shown), render.Count(meta.TotalCount))
	case meta.NextURL != "":
		// No total, but the server says there is another page.
		d.Footer("Showing %s · raise `--limit` for more", render.Count(shown))
	case meta.TotalCount >= 0:
		d.Footer("Showing all %s.", render.Count(shown))
	default:
		d.Footer("Showing %s.", render.Count(shown))
	}
}

// reportDegraded prints the one-line notice for items that could not be fully
// decoded. It goes to stderr and the command still exits 0: the output is
// incomplete but truthful, and failing the whole command over one bad item would
// throw away every good one.
func reportDegraded[T any](w io.Writer, sink *bugsnagio.TypedSink[T], d *render.Doc) {
	if msg := sink.Warning(); msg != "" {
		fmt.Fprintln(w, msg)
	}
	for _, warning := range d.Warnings() {
		fmt.Fprintln(w, warning)
	}
}

func warnf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "warning: "+format+"\n", args...)
}
