package render

import (
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Write writes a document to w.
//
// There is one authoring path: views build a Doc, and the Doc decides what its
// nodes look like from the Mode it was created with. That is what keeps human
// and agent output from drifting apart, because there is no second code path to
// forget to update. The --json path never comes here; it writes the API's item
// bytes directly.
func Write(w io.Writer, d *Doc) error {
	_, err := io.WriteString(w, d.String())
	return err
}

// theme is how each kind of node is styled on a terminal.
//
// Every field is a function rather than a lipgloss.Style so the piped theme can
// leave them nil, and so a caller cannot accidentally apply a width or a border
// from a style it was handed.
type theme struct {
	inline inlineStyle

	heading    func(string) string
	subheading func(string) string
	footer     func(string) string
	label      func(string) string
	tableHead  func(string) string
	rule       func(string) string

	// detail styles a table's full-width rows. They are dimmed because they are
	// the wordiest thing on the screen and the row above is what the eye scans
	// first — but only dimmed, since the message is often the only field that
	// tells two rows apart.
	detail func(string) string
}

// plainTheme drops every marker and keeps the text. It is what a pipe gets: no
// escape sequences to strip, and nothing that changes the bytes a caller reads.
var plainTheme = theme{}

// terminalTheme is the styled theme, built once.
//
// The colour profile is taken from the environment and never by asking the
// terminal. Querying the terminal for its background colour means waiting for a
// reply, which hangs when stdout is a pty nobody is answering for, exactly the
// case when an agent runs this in a pty. termenv.EnvColorProfile reads TERM,
// COLORTERM, NO_COLOR and CLICOLOR_FORCE only, so it cannot block, and it
// collapses to Ascii when NO_COLOR is set,
// which makes every style below a no-op.
var terminalTheme = sync.OnceValue(func() theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.EnvColorProfile())

	// The only decision the background hint drives: the bright variants read well
	// on a dark terminal and wash out on a light one.
	codeColor, headingColor := lipgloss.Color("6"), lipgloss.Color("4")
	if isDarkBackground() {
		codeColor, headingColor = lipgloss.Color("14"), lipgloss.Color("12")
	}

	code := r.NewStyle().Foreground(codeColor)
	bold := r.NewStyle().Bold(true)
	italic := r.NewStyle().Italic(true)
	faint := r.NewStyle().Faint(true)

	// Deliberately not Underline: lipgloss emits underlined text one grapheme at
	// a time, wrapped in its own escape sequence pair, which is thirteen times the
	// bytes for a title and unreadable in any transcript of the output.
	heading := r.NewStyle().Bold(true).Foreground(headingColor)

	return theme{
		inline: inlineStyle{
			code:   styleFunc(code),
			strong: styleFunc(bold),
			emph:   styleFunc(italic),
		},
		heading:    styleFunc(heading),
		subheading: styleFunc(bold),
		footer:     styleFunc(faint),
		label:      styleFunc(bold),
		tableHead:  styleFunc(bold),
		rule:       styleFunc(faint),
		detail:     styleFunc(faint),
	}
})

// styleFunc adapts a lipgloss style to the one-string function a theme holds.
// lipgloss's own Render is variadic, which does not fit and would join its
// arguments with a space if it did.
func styleFunc(s lipgloss.Style) func(string) string {
	return func(text string) string { return s.Render(text) }
}

// isDarkBackground reads the COLORFGBG convention ("15;0" means white on
// black), which some terminals set and which costs nothing to honour. Dark is
// assumed otherwise, being the common default for developer terminals.
func isDarkBackground() bool {
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if len(parts) < 2 {
		return true
	}

	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return true
	}
	// 0-6 and 8 are the dark ANSI background colours.
	return bg <= 6 || bg == 8
}
