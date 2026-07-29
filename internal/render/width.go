package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	// DefaultWidth is the content width assumed when there is no terminal to
	// measure. A wide guess is the safer one: piped output is not truncated, so
	// it costs nothing there, and an unmeasurable terminal is more often wide
	// than narrow.
	DefaultWidth = 120

	// MinWidth stops a very narrow terminal from producing output so cramped
	// that it is unreadable; below this we accept overflow instead. It is set to
	// the 80 columns we target, so a narrower terminal overflows rather than
	// squeezing every column down.
	MinWidth = 80

	// MaxWidth caps the length of a line of prose. A 160-column paragraph is hard
	// to read, because the eye loses its place on the way back to the left
	// margin.
	//
	// It caps prose only. A table gets the whole terminal: its columns are short
	// and gridlined, so the eye tracks a row across them without help, and extra
	// width goes into showing more of a context or a message rather than into
	// longer lines. Capping tables too was wrong, and made a 162-column terminal
	// render as if it were 122.
	MaxWidth = 120

	// MinPageSize and MaxPageSize bound a page of results. Five is few enough to
	// be worth a request; fifteen is where a list stops being scannable whatever
	// the terminal.
	MinPageSize = 5
	MaxPageSize = 15

	// listChrome is how many lines a list spends on things that are not an
	// entry: the title, the summary line, the column header and its rule, the
	// footer and the pagination line. The blank lines the document puts between
	// those blocks are deliberately not counted — erring low fills more of the
	// screen, and a page one line too tall only scrolls the terminal by one.
	listChrome = 6

	// gutter is the width held back from the terminal. A line that reaches the
	// final column makes some terminals wrap it, which turns one row of a table
	// into two, and a right-hand margin is easier to read besides.
	gutter = 2
)

// ContentWidth converts a measured terminal width into the width available for
// content: the whole terminal less the gutter, with a floor for the unusably
// narrow. Prose narrows itself further, at the point it is wrapped.
//
// A width of zero means there was nothing to measure, which is not the same as a
// narrow terminal and does not get the floor.
func ContentWidth(termWidth int) int {
	if termWidth <= 0 {
		return DefaultWidth
	}
	w := termWidth - gutter
	if w < MinWidth {
		return MinWidth
	}
	return w
}

// proseWidth is the width a paragraph, a field or a footer wraps to.
func proseWidth(width int) int {
	if width > MaxWidth {
		return MaxWidth
	}
	return width
}

// StringWidth is the number of terminal cells s occupies. It is
// grapheme-cluster aware, so an emoji, a combining accent or a CJK character
// counts as the space it actually takes rather than as a count of bytes or
// runes.
func StringWidth(s string) int { return ansi.StringWidth(s) }

// Truncate shortens s to at most width cells, appending tail if it had to cut.
// It cuts on grapheme cluster boundaries, so it cannot split a multi-byte
// character or strand a combining mark the way byte slicing does.
//
// The zero-width case is ours rather than ansi's: a styled string truncated to
// nothing still carries its escape sequences, which would print as an empty but
// non-blank cell.
func Truncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, tail)
}

// Wrap soft-wraps s to lines of at most width cells, breaking at spaces where
// it can and mid-word only when a single word is longer than the width.
//
// This is used for paragraphs and for text that must not be truncated: a Go
// wrapError message routinely runs past 100 characters and the diagnosis is
// usually at the end.
//
// Deliberately not ansi.Wrap, which differs in two ways that matter here: it
// keeps runs of whitespace rather than collapsing them, and it leaves a style
// open across the newline instead of re-emitting it per line. Table detail rows
// get an indent and a gridline inserted between the lines, so an unterminated
// style would colour them.
func Wrap(s string, width int) []string {
	return wrapAll(s, width, true)
}

// wrapWords soft-wraps at spaces only, never inside a word.
//
// This is for composed lines rather than prose: a URL, an id or a Go type name
// has to stay copyable, and breaking one is worse than leaving a single line the
// terminal will fold on its own. Deliberately not ansi.Wordwrap, which treats a
// hyphen as a breakpoint and so splits exactly the slugs and paths this protects.
func wrapWords(s string, width int) []string {
	return wrapAll(s, width, false)
}

// wrapIndented wraps s and indents every line after the first, so a wrapped
// field reads as a continuation rather than as a new one.
func wrapIndented(s string, width int, indent string) []string {
	lines := wrapWords(s, width-StringWidth(indent))
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return lines
}

func wrapAll(s string, width int, breakWords bool) []string {
	if width <= 0 {
		return []string{s}
	}

	var lines []string
	for paragraph := range strings.SplitSeq(s, "\n") {
		lines = append(lines, wrapOne(paragraph, width, breakWords)...)
	}
	return lines
}

func wrapOne(s string, width int, breakWords bool) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}

	var (
		lines []string
		cur   strings.Builder
		curW  int
	)
	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
	}

	for _, word := range words {
		ww := StringWidth(word)

		// A word longer than the width has to be split, but on cluster
		// boundaries rather than bytes.
		//
		// The remainder is taken with TruncateLeft rather than by trimming the
		// head off the front: a styled word's head is not a byte prefix of it,
		// because Truncate re-emits the reset sequence it cut off. Trimming a
		// prefix that is not there left the word unchanged and this loop spun
		// forever.
		if ww > width && breakWords {
			flush()
			for StringWidth(word) > width {
				head := ansi.Truncate(word, width, "")
				rest := ansi.TruncateLeft(word, width, "")
				if head == "" || StringWidth(rest) >= StringWidth(word) {
					break
				}
				lines = append(lines, head)
				word = rest
			}
			if word != "" {
				cur.WriteString(word)
				curW = StringWidth(word)
			}
			continue
		}

		switch {
		case curW == 0:
			cur.WriteString(word)
			curW = ww
		case curW+1+ww <= width:
			cur.WriteByte(' ')
			cur.WriteString(word)
			curW += 1 + ww
		default:
			flush()
			cur.WriteString(word)
			curW = ww
		}
	}
	flush()

	return lines
}
