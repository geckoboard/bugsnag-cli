package view

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// FrameScope selects which frames to show, using the dashboard's own wording:
// "Project code" or "Full trace".
type FrameScope int

const (
	// ScopeProject shows only frames in the project's own code. It falls back to
	// showing everything when in_project is absent, which it is on every Go
	// event: filtering on a field that is not there would show nothing at all.
	ScopeProject FrameScope = iota

	// ScopeFull shows every frame.
	ScopeFull
)

// ParseFrameScope parses the --frames flag.
func ParseFrameScope(s string) (FrameScope, error) {
	switch s {
	case "project", "":
		return ScopeProject, nil
	case "full", "all":
		return ScopeFull, nil
	}
	return 0, fmt.Errorf("unknown frame scope %q: want project or full", s)
}

// StacktraceOptions controls how a chain is rendered.
type StacktraceOptions struct {
	Scope FrameScope

	// Code includes the source snippet under each frame.
	Code bool

	// MaxFrames caps how many frames are listed per exception. Zero means no cap.
	MaxFrames int
}

// WriteExceptionChain renders the exception chain, outermost first, with a
// "Caused by" line for each link.
//
// The wording follows the dashboard so there is one shared vocabulary.
func WriteExceptionChain(d *render.Doc, chain []Exception, opts StacktraceOptions, m render.Mode) {
	if len(chain) == 0 {
		d.Text("_No stack trace on this event._")
		return
	}

	writeFrames(d, chain[0], opts, m)

	for _, cause := range chain[1:] {
		d.Blank()
		summary := cause.Message
		if summary == "" {
			summary = "(no message)"
		}
		d.Line("**Caused by** %s — %s",
			render.Code(cause.ErrorClass), render.Escape(summary))

		if opts.Scope == ScopeFull {
			writeFrames(d, cause, opts, m)
		}
	}
}

// writeFrames lists one exception's frames.
//
// The format is `path:line · method`, matching the dashboard exactly. The mixed
// absolute build paths and module paths are deliberately not normalised: on a Go
// service that difference is itself the in-project signal, since in_project is
// absent.
func writeFrames(d *render.Doc, e Exception, opts StacktraceOptions, m render.Mode) {
	frames := selectFrames(e.Stacktrace, opts.Scope)
	if len(frames) == 0 {
		return
	}

	capped := false
	if opts.MaxFrames > 0 && len(frames) > opts.MaxFrames {
		frames = frames[:opts.MaxFrames]
		capped = true
	}

	// Whether any frame carries source at all. Asking for --code and getting
	// nothing back looks like a broken flag, when the real answer is that the
	// notifier never uploaded source for this event.
	haveCode := false
	for _, f := range frames {
		if len(f.Code) > 0 {
			haveCode = true
			break
		}
	}

	d.ResetItems()
	for _, f := range frames {
		if f.collapsed > 0 {
			// A contiguous run of library frames is one line rather than twenty.
			d.Item().Line("_… %d library %s_", f.collapsed, render.Plural(f.collapsed, "frame")).Done()
			continue
		}

		// One frame is one line. Wrapping a module path across two lines makes a
		// trace much harder to scan, and the tail of the path is the part that
		// identifies the frame, so a long one is trimmed from the left.
		loc := location(f.Frame)
		line := fmt.Sprintf("%s · %s", render.Code(loc), render.Code(f.Method))
		if m.TTY {
			line = fitFrameLine(loc, f.Method, m.Width-frameIndent)
		}

		item := d.Item().Line("%s", line)
		if opts.Code && len(f.Code) > 0 {
			for _, line := range codeLines(f.Frame) {
				item.Line("%s", render.Code(line))
			}
		}
		item.Done()
	}

	if capped {
		d.Footer("Frame list truncated. Use `--frames full` for the whole trace.")
	}
	if opts.Code && !haveCode {
		d.Footer("No source snippets on this event: the notifier did not upload any.")
	}
}

// frameIndent is the room the ordinal takes: "12. " at its widest.
const frameIndent = 4

// fitFrameLine keeps a frame on one line, trimming the location from the left so
// the filename and line number survive.
func fitFrameLine(location, method string, width int) string {
	const sep = " · "

	width = max(width, minFrameWidth)

	full := location + sep + method
	if render.StringWidth(full) <= width {
		return render.Code(location) + sep + render.Code(method)
	}

	// The method is the more compact half and rarely the thing to cut, so the
	// location gives up the room.
	budget := max(width-render.StringWidth(method)-render.StringWidth(sep), minLocationWidth)
	return render.Code(trimLeft(location, budget)) + sep + render.Code(method)
}

// trimLeft shortens s from the front, marking the cut, so the informative tail
// stays.
func trimLeft(s string, width int) string {
	if render.StringWidth(s) <= width {
		return s
	}
	// The final candidate is "…" alone, one cell against a width the caller has
	// already floored well above that, so the loop always returns.
	runes := []rune(s)
	for i := range runes {
		candidate := "…" + string(runes[i+1:])
		if render.StringWidth(candidate) <= width {
			return candidate
		}
	}
	return "…"
}

// minFrameWidth and minLocationWidth stop a very narrow terminal from reducing a
// frame to punctuation.
const (
	minFrameWidth    = 24
	minLocationWidth = 12
)

// displayFrame is a frame or a marker standing in for a run of library frames.
type displayFrame struct {
	Frame

	// collapsed is how many library frames this marker replaces, or 0 for a real
	// frame.
	collapsed int
}

// selectFrames applies the scope and collapses runs of library frames.
func selectFrames(frames []Frame, scope FrameScope) []displayFrame {
	// Placeholder frames carrying no location are noise.
	real := make([]Frame, 0, len(frames))
	for _, f := range frames {
		if !isSentinelFrame(f) {
			real = append(real, f)
		}
	}

	if scope == ScopeFull || !anyInProjectKnown(real) {
		// With in_project absent there is nothing to filter or collapse on, so
		// everything is shown. This is the Go case, and showing the whole trace
		// is far better than showing none of it.
		out := make([]displayFrame, 0, len(real))
		for _, f := range real {
			out = append(out, displayFrame{Frame: f})
		}
		return out
	}

	var out []displayFrame
	run := 0
	flush := func() {
		if run > 0 {
			out = append(out, displayFrame{collapsed: run})
			run = 0
		}
	}

	for _, f := range real {
		if inProject, known := f.InProject.Bool(); known && !inProject {
			run++
			continue
		}
		flush()
		out = append(out, displayFrame{Frame: f})
	}
	flush()

	return out
}

// anyInProjectKnown reports whether the field is populated at all.
//
// This is what makes frame filtering disable itself rather than return nothing:
// on every Go event in_project is absent, so "no project frames" would otherwise
// mean "no output".
func anyInProjectKnown(frames []Frame) bool {
	for _, f := range frames {
		if _, known := f.InProject.Bool(); known {
			return true
		}
	}
	return false
}

// location is the `path:line` half of a frame line.
func location(f Frame) string {
	if f.File == "" {
		return "(unknown)"
	}
	if f.LineNumber <= 0 {
		return f.File
	}
	if f.ColumnNumber > 0 {
		return fmt.Sprintf("%s:%d:%d", f.File, f.LineNumber, f.ColumnNumber)
	}
	return fmt.Sprintf("%s:%d", f.File, f.LineNumber)
}

// codeLines returns the snippet in line order.
func codeLines(f Frame) []string {
	nums := make([]int, 0, len(f.Code))
	for k := range f.Code {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)

	out := make([]string, 0, len(nums))
	for _, n := range nums {
		marker := " "
		if n == f.LineNumber {
			marker = ">"
		}
		out = append(out, fmt.Sprintf("%s %d %s", marker, n, strings.TrimRight(f.Code[strconv.Itoa(n)], " \t")))
	}
	return out
}
