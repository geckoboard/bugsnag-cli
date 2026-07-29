package render

import "strings"

// Align is a column's horizontal alignment.
type Align int

const (
	AlignLeft Align = iota
	AlignRight
)

// Table is the layout for views whose rows are genuinely uniform.
//
// Flattening keeps a tab in the data from forging the piped delimiter. Resolving
// turns inline notation into text, which has to happen before fitting:
// truncating notation cuts inside it and leaves a dangling backtick behind.
// Fitting then trims what is left to the terminal width, and only on a terminal:
// piped output is unpadded and never truncated, so a caller never needs a second
// request to recover a value.
type Table struct {
	doc     *Doc
	headers []string
	aligns  []Align
	rows    []tableRow

	// noTrunc marks columns that must not be shown partially. A truncated id is
	// worse than an absent one, because it looks copyable and is not.
	noTrunc map[int]bool

	// detailHeader names the column the detail rows collapse into when piped.
	detailHeader string

	emptyMsg string
}

// tableRow is a row and the full-width row that follows it, if any.
type tableRow struct {
	cells []string

	// detail is a row spanning the whole table, for a value no column can hold.
	detail string
}

// Table starts a table with the given headers.
func (d *Doc) Table(headers ...string) *Table {
	return &Table{
		doc:          d,
		headers:      headers,
		aligns:       make([]Align, len(headers)),
		noTrunc:      map[int]bool{},
		detailHeader: "Detail",
	}
}

// Align sets column alignments, left to right.
func (t *Table) Align(aligns ...Align) *Table {
	copy(t.aligns, aligns)
	return t
}

// NeverTruncate marks columns whose cells must be shown in full or not at all.
func (t *Table) NeverTruncate(cols ...int) *Table {
	for _, c := range cols {
		t.noTrunc[c] = true
	}
	return t
}

// DetailHeader names the column detail rows become when piped.
func (t *Table) DetailHeader(name string) *Table {
	t.detailHeader = name
	return t
}

// Empty sets the line written instead of the table when there are no rows.
func (t *Table) Empty(msg string) *Table {
	t.emptyMsg = msg
	return t
}

// Row appends a row. Missing cells are treated as empty, so a view does not have
// to pad.
//
// A cell is inline notation the view has already composed, so only what would
// break the row structure is flattened here. Data that needs neutralising goes
// through Code or Escape at the call site: that is why the views put error
// classes, ids, hosts and release versions in code spans, where an asterisk or
// underscore is literal.
func (t *Table) Row(cells ...string) *Table {
	row := make([]string, len(t.headers))
	copy(row, cells)
	t.rows = append(t.rows, tableRow{cells: row})
	return t
}

// Detail attaches a row spanning the whole table to the row just added.
//
// A detail row is never measured for column widths, so it cannot squeeze the
// columns — which is the whole reason this exists rather than another column.
// Piped it folds back into its own row as a trailing field, because the two-row
// split is a consequence of a terminal being narrow and a pipe is not.
func (t *Table) Detail(format string, args ...any) *Table {
	if len(t.rows) == 0 {
		return t
	}
	t.rows[len(t.rows)-1].detail = sprintf(format, args...)
	return t
}

// Done writes the table into the document. The table is rendered from the
// builder itself: every caller builds one, calls Done and drops it, so there is
// nothing to protect against a later mutation.
func (t *Table) Done() {
	if len(t.rows) == 0 {
		if t.emptyMsg != "" {
			t.doc.Footer("%s", t.emptyMsg)
		}
		return
	}
	t.doc.add(node{kind: nodeTable, table: t})
}

// write emits the table in whichever of the two forms the mode calls for.
func (t *Table) write(b *strings.Builder, th theme, m Mode) {
	headers, rows := t.resolved(th.inline)

	if !m.TTY {
		writeTSV(b, headers, rows, t.detailHeader)
		return
	}
	writeGrid(b, headers, rows, t.aligns, t.noTrunc, th, m.Width)
}

// resolved turns the inline notation in every cell into text and flattens what
// would break a row. Both happen in one pass: resolving emits escape sequences,
// never a tab or a newline, so neither step can undo the other.
func (t *Table) resolved(sty inlineStyle) ([]string, []tableRow) {
	cell := func(s string) string { return flattenCell(resolveInline(s, sty)) }

	headers := make([]string, len(t.headers))
	for i, h := range t.headers {
		headers[i] = cell(h)
	}

	rows := make([]tableRow, len(t.rows))
	for i, row := range t.rows {
		cells := make([]string, len(row.cells))
		for j, c := range row.cells {
			cells[j] = cell(c)
		}
		rows[i] = tableRow{cells: cells, detail: cell(row.detail)}
	}

	return headers, rows
}

// writeTSV emits the piped form: tab-separated, unpadded, nothing truncated.
//
// Tabs are the delimiter because flattenCell has already replaced any tab in the
// data with a space, so content cannot forge one. Padding would add bytes that
// inform nobody who is going to parse or diff this.
func writeTSV(b *strings.Builder, headers []string, rows []tableRow, detailHeader string) {
	if anyDetail(rows) {
		headers = append(append([]string{}, headers...), detailHeader)
	} else {
		detailHeader = ""
	}

	writeLine(b, strings.Join(headers, "\t"))
	for _, row := range rows {
		cells := row.cells
		if detailHeader != "" {
			cells = append(append([]string{}, cells...), row.detail)
		}
		writeLine(b, strings.Join(cells, "\t"))
	}
}

// writeGrid emits the terminal form: box-drawing gridlines, padded columns, and
// each row trimmed to the width available.
//
// The gridlines are the point. Without them nothing anchors the eye across a row
// of unrelated facts, which is what a table in a fenced block or a bare run of
// columns both fail at.
func writeGrid(
	b *strings.Builder, headers []string, rows []tableRow,
	aligns []Align, noTrunc map[int]bool, th theme, width int,
) {
	headers, rows, widths := fit(headers, rows, noTrunc, width)

	writeLine(b, gridRow(headers, widths, aligns, th.tableHead, th.rule))
	writeLine(b, style(th.rule, gridDivider(widths)))

	// Once entries run to more than one line each, a blank line between them is
	// what tells the eye where one ends. A table of single-line rows needs no
	// such help and reads better tight, so the spacing follows the shape rather
	// than being a setting.
	spaced := anyDetail(rows)

	for i, row := range rows {
		if spaced && i > 0 {
			b.WriteByte('\n')
		}
		writeLine(b, gridRow(row.cells, widths, aligns, nil, th.rule))
		// The detail is indented under its row and never measured for the column
		// widths, which is what makes it a span rather than a column.
		for _, line := range detailLines(row.detail, width) {
			writeLine(b, detailIndent+style(th.detail, line))
		}
	}
}

// detailLines wraps a detail row to at most maxDetailLines.
//
// Two lines rather than one because the diagnosis in these messages is usually at
// the end, and rather than unlimited because a row has to stay scannable: a
// 400-character message would otherwise push the next row off the screen. What
// does not fit is folded into the last line and marked, so the cut is visible.
func detailLines(detail string, width int) []string {
	if detail == "" {
		return nil
	}

	// The wrapped text plus its indent must not exceed the width, or a detail row
	// folds into two — the thing this layout exists to avoid. Below MinWidth the
	// terminal is narrower than we target, so we accept the overflow rather than
	// squeeze the text to nothing.
	avail := max(width, MinWidth) - len(detailIndent)
	lines := Wrap(detail, avail)

	if len(lines) > maxDetailLines {
		rest := strings.Join(lines[maxDetailLines-1:], " ")
		lines = append(lines[:maxDetailLines-1], Truncate(rest, avail, "…"))
	}
	return lines
}

const maxDetailLines = 2

// TableEntryLines is how many lines one entry of a table with detail rows can
// take: its row, the lines its detail may wrap to, and the blank line that
// separates it from the next. It is what a page size is derived from.
const TableEntryLines = 1 + maxDetailLines + 1

func gridRow(cells []string, widths []int, aligns []Align, cell func(string) string, rule func(string) string) string {
	var b strings.Builder
	for i, w := range widths {
		if i > 0 {
			b.WriteString(" " + style(rule, "│") + " ")
		} else {
			b.WriteString(" ")
		}

		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		b.WriteString(pad(style(cell, text), w, alignAt(aligns, i)))
	}
	return b.String()
}

// gridDivider draws the rule under the header, crossed at each column boundary.
func gridDivider(widths []int) string {
	var b strings.Builder
	for i, w := range widths {
		if i > 0 {
			b.WriteString("┼")
		}
		// A column occupies its content plus the space on either side of it. The
		// last column has no trailing space, so the rule ends level with the
		// widest row rather than past it.
		segment := w + 2
		if i == len(widths)-1 {
			segment = w + 1
		}
		b.WriteString(strings.Repeat("─", segment))
	}
	return b.String()
}

// detailIndent steps a detail row in under its row, so it reads as belonging to
// it rather than as another row.
const detailIndent = "   "

// fit trims cells so the table fits the terminal.
//
// Columns marked NeverTruncate keep their full width and the budget is taken
// from the others, because a half-printed id cannot be copied and so is worse
// than a wrapped row.
func fit(headers []string, rows []tableRow, noTrunc map[int]bool, available int) ([]string, []tableRow, []int) {
	widths := columnWidths(headers, rows)

	total := gridOverhead(len(widths))
	for _, w := range widths {
		total += w
	}
	if total <= available {
		return headers, rows, widths
	}

	excess := total - available
	budget := make([]int, len(widths))
	copy(budget, widths)

	// Take from the widest truncatable column first, so one runaway column does
	// not force every other column to shrink.
	for excess > 0 {
		widest, widestIdx := 0, -1
		for i, w := range budget {
			if noTrunc[i] || w <= minColumnWidth {
				continue
			}
			if w > widest {
				widest, widestIdx = w, i
			}
		}
		if widestIdx < 0 {
			break
		}
		budget[widestIdx]--
		excess--
	}

	newHeaders := make([]string, len(headers))
	for i, h := range headers {
		newHeaders[i] = Truncate(h, budget[i], "…")
	}

	newRows := make([]tableRow, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row.cells))
		for j, cell := range row.cells {
			if noTrunc[j] {
				cells[j] = cell
				continue
			}
			cells[j] = Truncate(cell, budget[j], "…")
		}
		newRows[i] = tableRow{cells: cells, detail: row.detail}
	}
	// Measured again rather than assumed to be the budget: a cell may be narrower
	// than the budget allowed, and padding to the budget would leave a gap.
	return newHeaders, newRows, columnWidths(newHeaders, newRows)
}

// gridOverhead is what the gridlines cost: a leading space, then " │ " between
// each pair of columns.
func gridOverhead(columns int) int {
	if columns <= 0 {
		return 0
	}
	return 1 + 3*(columns-1)
}

// minColumnWidth is the narrowest a column is squeezed to before the shrinking
// loop gives up: below this a cell shows almost nothing but the ellipsis.
const minColumnWidth = 6

func columnWidths(headers []string, rows []tableRow) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = StringWidth(h)
	}
	for _, row := range rows {
		for i, cell := range row.cells {
			if i < len(widths) && StringWidth(cell) > widths[i] {
				widths[i] = StringWidth(cell)
			}
		}
	}
	return widths
}

// flattenCell makes a cell safe to sit in one field of one line. A newline would
// end the row and a tab would forge the piped delimiter, so both become spaces.
func flattenCell(s string) string { return flattenLines.Replace(s) }

func anyDetail(rows []tableRow) bool {
	for _, row := range rows {
		if row.detail != "" {
			return true
		}
	}
	return false
}

func pad(s string, width int, align Align) string {
	gap := width - StringWidth(s)
	if gap <= 0 {
		return s
	}
	if align == AlignRight {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

func alignAt(aligns []Align, i int) Align {
	if i < len(aligns) {
		return aligns[i]
	}
	return AlignLeft
}
