package render_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gotest.tools/v3/assert"

	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// TestTruncateIsGraphemeSafe: truncation cuts on grapheme boundaries, so it never
// splits a multi-byte character in half and emits invalid UTF-8, the way byte
// slicing would.
func TestTruncateIsGraphemeSafe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"fits unchanged", "hello", 10, "hello"},
		{"exact fit is unchanged", "hello", 5, "hello"},
		{"ascii truncated", "hello world", 8, "hello w…"},
		{"zero width", "hello", 0, ""},
		{"does not split a cjk character", "日本語です", 5, "日本…"},
		{"does not split an emoji", "🔥🔥🔥", 3, "🔥…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render.Truncate(tc.in, tc.width, "…")
			assert.Equal(t, got, tc.want)
			assert.Assert(t, utf8.ValidString(got), "produced invalid UTF-8: %q", got)
			assert.Assert(t, render.StringWidth(got) <= tc.width)
		})
	}
}

func TestWrap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{
			name:  "short text is one line",
			in:    "hello world",
			width: 20,
			want:  []string{"hello world"},
		},
		{
			name:  "breaks at spaces",
			in:    "the quick brown fox jumps",
			width: 10,
			want:  []string{"the quick", "brown fox", "jumps"},
		},
		{
			name:  "a word longer than the width is split",
			in:    "supercalifragilistic",
			width: 8,
			want:  []string{"supercal", "ifragili", "stic"},
		},
		{
			name:  "empty input",
			in:    "",
			width: 10,
			want:  []string{""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render.Wrap(tc.in, tc.width)
			assert.DeepEqual(t, got, tc.want)
			for _, line := range got {
				assert.Assert(t, render.StringWidth(line) <= tc.width,
					"line %q is over the %d width", line, tc.width)
			}
		})
	}
}

// TestWrapNeverLosesCharacters matters because the Inbox deliberately does not
// truncate the message: the diagnosis in a Go wrapError chain is usually at the
// end, so losing the tail loses the point.
func TestWrapNeverLosesCharacters(t *testing.T) {
	msg := "resolving field #1: cannot read field id: from step #2 " +
		"(*steps.HTTPStep): request failed: Get " +
		"\"https://api.example.com/v1/records?include=fields&page=2\": retries exhausted"

	// Wrapping a word that is longer than the width has to break it, so lines
	// cannot simply be rejoined with spaces. The invariant that matters is that
	// no character is dropped: every non-space character survives, in order.
	for _, width := range []int{20, 40, 80, 200} {
		lines := render.Wrap(msg, width)
		assert.Equal(t, withoutSpaces(strings.Join(lines, "")), withoutSpaces(msg),
			"width %d lost or changed content", width)
	}
}

// TestWrapSplitsAStyledWord. A styled word's head is not a byte prefix of it,
// because Truncate re-emits the reset sequence it cut off, so taking the
// remainder by trimming the head off the front left the word unchanged and the
// loop spun forever. Any paragraph on a colour terminal can contain one: a
// composed filter description is a single unbroken token.
func TestWrapSplitsAStyledWord(t *testing.T) {
	styled := "\x1b[96m" + strings.Repeat("a", 20) + "\x1b[0m"

	done := make(chan []string, 1)
	go func() { done <- render.Wrap(styled, 5) }()

	select {
	case lines := <-done:
		assert.Equal(t, len(lines), 4, "got %q", lines)
		for _, line := range lines {
			assert.Assert(t, render.StringWidth(line) <= 5, "line is over 5: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wrap did not return: the long-word split is not making progress")
	}
}

// TestPageSizeFitsTheTerminal. Asking for more rows than fit means the top of the
// answer has scrolled away before it is read; asking for very few means paging
// through a trickle. Both ends are clamped.
func TestPageSizeFitsTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		height int
		want   int
	}{
		{"a short terminal gets the floor", 20, render.MinPageSize},
		{"a tall terminal gets the ceiling", 80, render.MaxPageSize},
		{"no terminal has no page to fill", 0, render.MaxPageSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := render.Mode{TTY: true, Width: 120, Height: tc.height}
			assert.Equal(t, m.PageSize(render.TableEntryLines), tc.want)
		})
	}
}

// TestAPageActuallyFits is the property behind the arithmetic: a full page of
// entries fits the terminal it was sized for. The chrome around the entries is
// deliberately under-counted, so a full page may scroll the terminal by a line —
// that is accepted, and erring toward showing more is the point.
func TestAPageActuallyFits(t *testing.T) {
	for height := 24; height <= 70; height++ {
		m := render.Mode{TTY: true, Width: 120, Height: height}
		entries := m.PageSize(render.TableEntryLines)

		// The floor is allowed to overflow a very short terminal: showing a few
		// entries is not worth a request.
		if entries > render.MinPageSize {
			assert.Assert(t, entries*render.TableEntryLines <= height,
				"height %d: %d entries need %d lines", height, entries, entries*render.TableEntryLines)
		}
	}
}

func TestContentWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		term int
		want int
	}{
		{"an 80 column terminal gets the floor", 80, render.MinWidth},
		// A wide terminal gets its whole width: the cap applies to prose, at the
		// point it is wrapped, not to the space a table may use.
		{"wide terminal keeps its width", 400, 398},
		{"narrow terminal gets the floor", 20, render.MinWidth},
		{"nothing to measure gets the default", 0, render.DefaultWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, render.ContentWidth(tc.term), tc.want)
		})
	}
}

func withoutSpaces(s string) string { return strings.Join(strings.Fields(s), "") }
