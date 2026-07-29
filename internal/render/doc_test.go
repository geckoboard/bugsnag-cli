package render_test

import (
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/golden"

	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// pipeMode is the layout an agent sees: no terminal, so unpadded and lossless.
func pipeMode() render.Mode {
	return render.Mode{
		TTY:   false,
		Width: 80,
		Time:  render.TimeAuto,
		Now:   time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}
}

// ttyMode is the layout a human sees.
//
// Colour is absent in both modes here, and deliberately so: the colour profile
// comes from the environment, and stdout under `go test` is a pipe, so every
// style collapses to a no-op. That leaves these tests asserting layout, which is
// the part that was getting broken.
func ttyMode() render.Mode {
	m := pipeMode()
	m.TTY = true
	return m
}

func TestDocStructure(t *testing.T) {

	d := render.New(pipeMode())
	d.H1("Errors — example-api")
	d.Text("11 errors · %s · last 30d", render.Code("status:open"))
	d.H2("Summaries")
	d.Field("Status", "open · handled")
	d.Field("Stages", "production, staging")
	d.Footer("More: %s", render.Code("bugsnag errors events"))

	golden.Assert(t, d.String(), "doc_structure.golden")
}

// TestDocumentStartsWithItsTitle is the cheapest possible check that the laid-out
// path ran rather than something falling through to a raw JSON dump.
func TestDocumentStartsWithItsTitle(t *testing.T) {

	for name, m := range map[string]render.Mode{"piped": pipeMode(), "tty": ttyMode()} {
		t.Run(name, func(t *testing.T) {

			d := render.New(m)
			d.H1("Errors — example-api")
			d.Text("nothing else")

			assert.Assert(t, strings.HasPrefix(d.String(), "Errors — example-api\n"),
				"output does not start with its title:\n%q", firstLine(d.String()))
		})
	}
}

// TestInlineNotationNeverReachesTheReader. The notation is how views mark intent
// and is not an output format: piped, a code span is its contents and an escape
// is the character it was protecting.
func TestInlineNotationNeverReachesTheReader(t *testing.T) {

	d := render.New(pipeMode())
	d.Line("%s · %s", render.Code("*fmt.wrapError"), render.Escape("a_b *c* d|e"))
	d.Footer("More: **bold** and _italic_ and %s", render.Code("`quoted`"))

	got := d.String()
	golden.Assert(t, got, "inline_notation.golden")
	for _, marker := range []string{"\\", "**"} {
		assert.Assert(t, !strings.Contains(got, marker), "output still carries %q:\n%s", marker, got)
	}
}

// TestUnmatchedMarkersStayLiteral: an unclosed marker is content, not a licence
// to eat the rest of the line.
func TestUnmatchedMarkersStayLiteral(t *testing.T) {

	d := render.New(pipeMode())
	d.Line("a ` b ** c _ d")

	assert.Equal(t, d.String(), "a ` b ** c _ d\n")
}

// TestMarkersInsideAWordAreLiteral is what keeps unescaped data intact. Values
// this tool prints are full of underscores, and treating one as emphasis deletes
// both markers: a release stage list came out as "preprod, stagingeu" and a
// dashboard link lost its query parameter.
func TestMarkersInsideAWordAreLiteral(t *testing.T) {

	for _, in := range []string{
		"pre_prod, staging_eu",
		"pre_release · version 1.2.3_beta",
		"https://app.bugsnag.com/acme/my_project/errors/68a?event_id=7",
		"__init__ and __call__",
		"snake_case_all_the_way",
		// Two markers with nothing between them are markers, not an empty span.
		"a__c",
		"****",
	} {
		assert.Equal(t, plainLine(t, in), in)
	}
}

// TestNotationStillWorksAtWordBoundaries: the intraword rule must not disarm the
// notation the views actually use.
func TestNotationStillWorksAtWordBoundaries(t *testing.T) {

	for in, want := range map[string]string{
		"_(empty)_":                    "(empty)",
		"**Caused by** x":              "Caused by x",
		"_… 3 library frames_":         "… 3 library frames",
		"a _quoted phrase_ in a line":  "a quoted phrase in a line",
		"**bold** then _italic_ ended": "bold then italic ended",
	} {
		assert.Equal(t, plainLine(t, in), want)
	}
}

// TestControlCharactersCannotReachTheTerminal. API data arrives unmodified, and
// Escape puts a backslash before the `[` of a CSI sequence which the resolver
// then removes again — reassembling it. An error message could otherwise colour
// itself or set the terminal title.
func TestControlCharactersCannotReachTheTerminal(t *testing.T) {

	hostile := "boom \x1b[31mRED\x1b[0m \x1b]0;pwned\x07 end"

	d := render.New(pipeMode())
	d.Text("%s", render.Escape(hostile))
	tbl := d.Table("Class", "Message")
	tbl.Row("E", render.Escape(hostile))
	tbl.Detail("%s", render.Escape(hostile))
	tbl.Done()

	got := d.String()
	for _, gone := range []string{"\x1b", "\x07"} {
		assert.Assert(t, !strings.Contains(got, gone), "a control character survived %q:\n%q", gone, got)
	}
	// The printable remainder is still there: nothing is silently dropped.
	assert.Assert(t, strings.Contains(got, "boom") && strings.Contains(got, "end"),
		"the message text was lost:\n%q", got)
}

// TestItemBlockLayout covers the stack-trace layout: the lines of a block stay
// separate lines, and continuations line up under the first.
func TestItemBlockLayout(t *testing.T) {

	d := render.New(pipeMode())
	d.Item().
		Line("%s · %s", render.Code("models/manager.go:105"), render.Code("(*Manager).Upsert")).
		Line("%s", render.Code("> 105   return fmt.Errorf(\"store: %w\", err)")).
		Done()
	d.Item().
		Line("%s · %s", render.Code("event.go:73"), render.Code("(*Runner).Run")).
		Done()

	golden.Assert(t, d.String(), "item_block.golden")
}

func TestItemsAreNumberedInOrder(t *testing.T) {

	d := render.New(pipeMode())
	for _, class := range []string{"first", "second", "third"} {
		d.Item().Line("%s", class).Done()
	}

	got := d.String()
	for _, want := range []string{"1. first", "2. second", "3. third"} {
		assert.Check(t, is.Contains(got, want))
	}
}

// TestParagraphsWrapToTheContentWidth, measured in terminal cells.
func TestParagraphsWrapToTheContentWidth(t *testing.T) {

	m := ttyMode()
	m.Width = 40
	d := render.New(m)
	d.Text("%s", strings.Repeat("wordy ", 30))

	for _, line := range strings.Split(strings.TrimRight(d.String(), "\n"), "\n") {
		w := render.StringWidth(line)
		assert.Assert(t, w <= m.Width, "line is %d cells, over the %d width: %q", w, m.Width, line)
	}
}

// TestEscapeFlattensWhitespace is the part of Escape the round-trip below cannot
// state: a newline in an error message would break the line it is dropped into,
// so it becomes a space on the way in and stays one.
func TestEscapeFlattensWhitespace(t *testing.T) {

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"newline", "a\nb", "a b"},
		{"carriage return", "a\rb", "a b"},
		{"tab", "a\tb", "a b"},
		{"plain text untouched", "hello world", "hello world"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, render.Escape(tc.in), tc.want)
		})
	}
}

// TestEscapeRoundTrips is the property that matters: whatever a view escapes, a
// reader sees back unchanged. Escape and the inline pass are two halves of one
// mechanism and neither is useful without the other.
func TestEscapeRoundTrips(t *testing.T) {

	for _, in := range []string{
		"*fmt.wrapError",
		"some_field_name",
		"a|b",
		"a`b",
		"back\\slash",
		"[link](url)",
		"**not bold**",
		"_not italic_",
		"100% of `everything`",
	} {
		assert.Equal(t, plainLine(t, render.Escape(in)), in)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// plainLine renders one line through a piped document, which is the route notation
// actually takes to a reader. It cannot carry a value with edge spaces, because
// each line is written with its trailing spaces trimmed.
func plainLine(t *testing.T, s string) string {
	t.Helper()

	d := render.New(pipeMode())
	d.Line("%s", s)
	return strings.TrimSuffix(d.String(), "\n")
}
