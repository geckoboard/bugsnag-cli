package render

import (
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// Mode is everything a view needs to know about how its output will be
// consumed. It is passed by value into every view; views read only their Mode
// and the data they are given, never the clock or the terminal directly, so the
// reference time is carried here rather than read from the clock.
type Mode struct {
	// TTY reports whether output is going to a terminal. It selects the
	// terminal layout (padded, truncated to fit) over the pipe layout
	// (unpadded, lossless).
	TTY bool

	// Width is the number of columns available for content.
	Width int

	// Height is the terminal's rows, or zero when there is no terminal. It sizes
	// a page of results: asking for more than fits means the top of the answer
	// has scrolled away before it is read.
	Height int

	// Time selects how timestamps are rendered.
	Time TimeStyle

	// Now is the reference point for relative times.
	Now time.Time

	// Redact reports whether secrets should be masked. It is on by default for
	// the text output, because metaData and request headers do carry live
	// credentials; --json is never redacted and says so in help.
	Redact bool
}

// DetectMode builds a Mode for writing to w.
func DetectMode(w io.Writer, now time.Time) Mode {
	tty := IsTerminal(w)
	width, height := terminalSize(w)
	return Mode{
		TTY:    tty,
		Width:  ContentWidth(width),
		Height: height,
		Time:   TimeAuto,
		Now:    now,
		Redact: true,
	}
}

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// terminalSize returns w's terminal size, or zero for either dimension there was
// nothing to measure. Zero means "unknown" rather than "small": ContentWidth
// turns an unknown width into the default, and an unknown height means there is
// no page to fill.
func terminalSize(w io.Writer) (width, height int) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, 0
	}
	width, height, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0, 0
	}
	return max(width, 0), max(height, 0)
}

// PageSize is how many entries of linesPerEntry lines each fit on one screen.
//
// It is clamped at both ends. Too many and the top of the answer scrolls away
// before it is read, which is the whole reason not to use the API's own default
// of 30; too few and you are paging through a trickle. With no terminal to
// measure there is no page to fill, so a pipe gets the maximum.
func (m Mode) PageSize(linesPerEntry int) int {
	if m.Height <= 0 || linesPerEntry <= 0 {
		return MaxPageSize
	}
	fits := (m.Height - listChrome) / linesPerEntry
	return min(max(fits, MinPageSize), MaxPageSize)
}
