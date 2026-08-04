package render_test

import (
	"strings"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/render"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/golden"
)

// TestTablePipedIsTSV: tab-separated, unpadded, unstyled. Padding is bytes that
// inform nobody who is going to parse or diff the output, and a tab is a
// delimiter no cell can forge, because flattening has already replaced any tab
// in the data with a space.
func TestTablePipedIsTSV(t *testing.T) {
	d := render.New(pipeMode())
	tbl := d.Table("Pivot", "Top value", "Share", "Distinct")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight)
	tbl.Row("Release stages", "production", "57.4%", "2")
	tbl.Row("Hosts", render.Code("230838611af3"), "7.1%", "85")
	tbl.Done()

	golden.Assert(t, d.String(), "table_piped.golden")
}

// TestTableTTYHasGridlines: on a terminal the columns line up and are ruled off
// from each other, which is what lets the eye follow one row across a run of
// unrelated facts.
func TestTableTTYHasGridlines(t *testing.T) {
	d := render.New(ttyMode())
	tbl := d.Table("Pivot", "Top value", "Share", "Distinct")
	tbl.Align(render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight)
	tbl.Row("Release stages", "production", "57.4%", "2")
	tbl.Row("Hosts", render.Code("230838611af3"), "7.1%", "85")
	tbl.Done()

	golden.Assert(t, d.String(), "table_tty.golden")
}

// TestDetailRowSpansTheTable is the layout this renderer exists for: a row the
// columns cannot hold, which is not measured for the column widths and so cannot
// squeeze them.
func TestDetailRowSpansTheTable(t *testing.T) {
	d := render.New(ttyMode())
	tbl := d.Table("ID", "Seen")
	tbl.DetailHeader("Message")
	tbl.Row(render.Code("6a3a318a90a602cb08300beb"), "38s")
	tbl.Detail("%s", render.Escape("store: unable to upsert records: context deadline exceeded"))
	tbl.Row(render.Code("6a68d7fa018f5404872a0000"), "16s")
	tbl.Detail("%s", render.Escape("resolving field #1"))
	tbl.Done()

	golden.Assert(t, d.String(), "detail_row_spans.golden")
}

// TestDetailRowWrapsToTwoLines: the diagnosis in these messages is usually at the
// end, so one line is not enough — but a row still has to stay scannable, so what
// does not fit in two is folded into the second and marked.
func TestDetailRowWrapsToTwoLines(t *testing.T) {
	// Width 80 is the target terminal and the tightest case: a detail line is the
	// content width less its indent, so indent + line must still fit 80. This
	// guards the regression where flooring the wrap width at MinWidth (80) and
	// then adding the indent produced 83-cell lines on an 80-cell terminal.
	m := ttyMode()
	m.Width = 80
	d := render.New(m)

	tbl := d.Table("ID", "Seen")
	tbl.Row("6a1", "38s")
	tbl.Detail("%s", strings.Repeat("wordy ", 60))
	tbl.Row("6a2", "16s")
	tbl.Detail("short enough to fit on one line")
	tbl.Done()

	lines := strings.Split(strings.TrimRight(d.String(), "\n"), "\n")

	// Header, rule, row, two detail lines, blank, row, one detail line.
	assert.Assert(t, is.Len(lines, 8), "output was:\n%s", d.String())
	assert.Equal(t, lines[5], "", "entries should be separated by a blank line")
	for _, i := range []int{3, 4, 7} {
		assert.Assert(t, strings.HasPrefix(lines[i], "   "),
			"line %d is not an indented detail line: %q", i, lines[i])
		assert.Assert(t, render.StringWidth(lines[i]) <= m.Width,
			"detail line %d is over the %d width: %q", i, m.Width, lines[i])
	}
	// The cut is marked rather than silent.
	assert.Assert(t, strings.HasSuffix(lines[4], "…"),
		"a message longer than two lines should be marked: %q", lines[4])
	// A short message does not gain a second line.
	assert.Equal(t, lines[7], "   short enough to fit on one line")
}

// TestDetailRowFoldsIntoItsRowWhenPiped. The two-row split is a consequence of a
// terminal being narrow; a pipe has no width, so one error is one line and a
// caller does not have to stitch pairs of lines back together.
func TestDetailRowFoldsIntoItsRowWhenPiped(t *testing.T) {
	d := render.New(pipeMode())
	tbl := d.Table("ID", "Seen")
	tbl.DetailHeader("Message")
	tbl.Row("6a3a318a90a602cb08300beb", "38s")
	tbl.Detail("store: unable to upsert records")
	tbl.Row("6a68d7fa018f5404872a0000", "16s")
	tbl.Detail("resolving field #1")
	tbl.Done()

	golden.Assert(t, d.String(), "detail_piped.golden")
}

// TestDifferingDetailsStayWithTheirRows, which is the case that makes a list of one
// error's occurrences worth reading: every occurrence's message is shown under
// its own row.
func TestDifferingDetailsStayWithTheirRows(t *testing.T) {
	d := render.New(ttyMode())
	tbl := d.Table("Event", "Received")
	tbl.DetailHeader("Message")
	tbl.Row("6a1", "12:00")
	tbl.Detail("from node #2: context deadline exceeded")
	tbl.Row("6a2", "12:05")
	tbl.Detail("from node #15: not following 301 redirect")
	tbl.Done()

	got := d.String()
	for _, want := range []string{"from node #2", "from node #15"} {
		assert.Check(t, is.Contains(got, want))
	}
}

// TestNoDetailColumnWithoutDetails: a table nobody called Detail on gains no
// column for it.
func TestNoDetailColumnWithoutDetails(t *testing.T) {
	d := render.New(pipeMode())
	tbl := d.Table("ID", "Seen")
	tbl.DetailHeader("Message")
	tbl.Row("6a1", "38s")
	tbl.Done()

	assert.Assert(t, !strings.Contains(d.String(), "Message"),
		"an unused detail column was emitted:\n%q", d.String())
}

// TestTableWithoutDetailsStaysTight: single-line rows read better without gaps,
// so the spacing follows the shape of the table rather than being a setting.
func TestTableWithoutDetailsStaysTight(t *testing.T) {
	d := render.New(ttyMode())
	tbl := d.Table("Event", "Received")
	tbl.Row("6a1", "12:00")
	tbl.Row("6a2", "12:05")
	tbl.Row("6a3", "12:09")
	tbl.Done()

	lines := strings.Split(strings.TrimRight(d.String(), "\n"), "\n")
	assert.Assert(t, is.Len(lines, 5), "want a header, a rule and three rows:\n%s", d.String())
	for i, line := range lines {
		assert.Assert(t, line != "", "line %d is blank; single-line rows should be tight", i)
	}
}

func TestTableEmpty(t *testing.T) {
	d := render.New(pipeMode())
	d.Table("Event", "Received").Empty("No events matched.").Done()

	assert.Equal(t, d.String(), "No events matched.\n")
}

// TestTableCannotForgeTheDelimiter is what makes TSV safe for hostile data: a tab
// or a newline in a cell would otherwise add a field or end the row.
func TestTableCannotForgeTheDelimiter(t *testing.T) {
	d := render.New(pipeMode())
	tbl := d.Table("Message", "Count")
	tbl.Row("a\tb", "1")
	tbl.Row("c\nd", "2")
	tbl.Row("e\r\nf", "3")
	tbl.Done()

	got := d.String()
	for _, want := range []string{"a b\t1", "c d\t2", "e  f\t3"} {
		assert.Check(t, is.Contains(got, want))
	}

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	assert.Assert(t, is.Len(lines, 4), "want a header and three rows:\n%q", got)
	for _, line := range lines {
		assert.Equal(t, strings.Count(line, "\t"), 1, "line %q should have exactly one tab", line)
	}
}

// TestTableCellsAreComposedNotation documents the cell contract: a cell is
// notation the view has already composed, so Table flattens only what would
// break the row. Data that needs neutralising goes through Code or Escape at the
// call site, which is why the views wrap error classes, ids, hosts and releases
// in code spans.
func TestTableCellsAreComposedNotation(t *testing.T) {
	d := render.New(pipeMode())
	tbl := d.Table("Pivot", "Top value")
	tbl.Row("Releases", render.Code("1e59939"))
	tbl.Row("Classes", render.Code("*fmt.wrapError"))
	tbl.Done()

	got := d.String()
	for _, want := range []string{"Releases\t1e59939", "Classes\t*fmt.wrapError"} {
		assert.Check(t, is.Contains(got, want))
	}
	assert.Assert(t, !strings.Contains(got, "`"), "a code fence reached the reader:\n%q", got)
}

// TestTableTruncatesToFitOnTTY: a wide table is trimmed to the terminal, and the
// trimming is measured in cells rather than bytes.
func TestTableTruncatesToFitOnTTY(t *testing.T) {
	m := ttyMode()
	m.Width = 40
	d := render.New(m)

	tbl := d.Table("Message", "Count")
	tbl.Row(strings.Repeat("long ", 40), "1")
	tbl.Row(strings.Repeat("also ", 40), "2")
	tbl.Done()

	for _, line := range strings.Split(strings.TrimRight(d.String(), "\n"), "\n") {
		assert.Assert(t, render.StringWidth(line) <= m.Width,
			"line is over the %d width: %q", m.Width, line)
	}
}

// TestTruncationNeverCutsInsideMarkup. Notation is resolved before anything is
// measured or trimmed, so a cut cell cannot end mid-code-span and leave a
// dangling fence or a stray backslash where a reader would see it.
func TestTruncationNeverCutsInsideMarkup(t *testing.T) {
	m := ttyMode()
	m.Width = 44
	d := render.New(m)

	tbl := d.Table("Class", "Context")
	tbl.Row(render.Code("*fmt.wrapError: models.UpsertStatuses"), render.Escape("some_long_context_name:0"))
	tbl.Row(render.Code("*errors.withStack: models.ReadFields"), render.Escape("another_long_context_name:0"))
	tbl.Done()

	got := d.String()
	assert.Assert(t, !strings.Contains(got, "`"), "truncation left a code fence behind:\n%s", got)
	assert.Assert(t, !strings.Contains(got, "\\"), "truncation left an escape behind:\n%s", got)
	assert.Assert(t, strings.Contains(got, "…"), "expected these cells to be truncated:\n%s", got)
}

// TestTableNeverTruncateDropsNothingPartially: a truncated id looks copyable and
// is not, so a column marked NeverTruncate keeps its cells whole even when the
// table has to give up width elsewhere.
func TestTableNeverTruncateDropsNothingPartially(t *testing.T) {
	const id = "6a68d7fa018f5404872a0000"

	m := ttyMode()
	m.Width = 50
	d := render.New(m)

	tbl := d.Table("Event", "Message")
	tbl.NeverTruncate(0)
	tbl.Row(id, strings.Repeat("verbose ", 30))
	tbl.Row("6a68d7fa018f5404872a0001", strings.Repeat("wordy ", 30))
	tbl.Done()

	assert.Assert(t, strings.Contains(d.String(), id),
		"id was truncated; it must be shown whole or not at all:\n%s", d.String())
}

// TestTablePipedIsNeverTruncated: an agent must never have to make a second
// request to recover a value that was cut to fit a terminal it is not using.
func TestTablePipedIsNeverTruncated(t *testing.T) {
	long := strings.Repeat("verbose ", 40)

	m := pipeMode()
	m.Width = 40
	d := render.New(m)

	tbl := d.Table("Message", "Count")
	tbl.Row(long, "1")
	tbl.Row(long+"x", "2")
	tbl.Detail("%s", long+"detail")
	tbl.Done()

	got := d.String()
	assert.Assert(t, strings.Contains(got, strings.TrimSpace(long)), "piped output truncated a cell:\n%s", got)
	assert.Assert(t, strings.Contains(got, long+"detail"), "piped output truncated a detail row:\n%s", got)
	assert.Assert(t, !strings.Contains(got, "…"), "piped output must not contain an ellipsis:\n%s", got)
}
