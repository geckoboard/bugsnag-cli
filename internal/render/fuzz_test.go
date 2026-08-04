// These tests are in-package rather than in render_test because the property they
// pin is byte-exact: a code span must survive resolution with its edge spaces
// intact, and the document layer trims trailing spaces off every line, so driving
// them through a Doc would compare two equally-trimmed strings and stop testing
// the thing that once broke.
package render

import (
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"
)

// plain resolves notation the way the piped path does: with the zero-value style,
// which is exactly what plainTheme carries.
func plain(s string) string { return resolveInline(s, inlineStyle{}) }

// FuzzInlineResolver covers the resolver against arbitrary input, which is what
// it actually gets: a view composes notation around API data, and that data is
// arbitrary bytes.
//
// The properties are the ones a caller depends on. Termination and no panic go
// without saying. Valid UTF-8 in means valid UTF-8 out, because the resolver
// indexes bytes to find its ASCII markers and must not cut a rune in half. And no
// control character survives, since a reassembled escape sequence would let
// remote data drive the terminal.
func FuzzInlineResolver(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain text",
		"`code`",
		"``a`b``",
		"**bold** and _italic_",
		"\\*escaped\\*",
		"a_b_c",
		"__init__",
		"unterminated `",
		"_(empty)_",
		"世界 `emoji 🎉` café",
		"\x1b[31mred\x1b[0m",
		"a\x00b",
		strings.Repeat("`", 40),
		strings.Repeat("_*", 40),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got := plain(in)

		assert.Assert(t, !utf8.ValidString(in) || utf8.ValidString(got),
			"plain(%q) = %q, which is not valid UTF-8", in, got)
		for _, r := range got {
			//nolint:staticcheck // reads as "is not a control character"
			assert.Assert(t, !(r < 0x20 && r != '\n' && r != '\t' || r == 0x7f),
				"plain(%q) = %q, which carries control character %#U", in, got, r)
		}
	})
}

// FuzzEscapeRoundTrips is the property the two halves of the mechanism owe each
// other: whatever a view escapes, a reader sees back unchanged.
func FuzzEscapeRoundTrips(f *testing.F) {
	for _, seed := range []string{
		"*fmt.wrapError",
		"a|b",
		"back\\slash",
		"100% of `everything`",
		"pre_prod, staging_eu",
		"__init__",
		"世界",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		// Control characters and invalid UTF-8 are deliberately not preserved.
		if !utf8.ValidString(in) || strings.ContainsFunc(in, isControlRune) {
			return
		}

		// Escape flattens these to spaces by design, so compare against that.
		want := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(in)
		assert.Equal(t, plain(Escape(in)), want)
	})
}

// FuzzCodeRoundTrips: a value put in a code span is a copy target, so it has to
// come out byte for byte.
func FuzzCodeRoundTrips(f *testing.F) {
	// " " is here because the fuzzer found it: a span is padded to protect edge
	// spaces, but the resolver leaves an all-space span alone, so padding one
	// added two spaces instead of protecting one.
	for _, seed := range []string{"1e59939", "a`b", "`x", " x ", " ", "   ", "`", "``", "a b"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		if in == "" || !utf8.ValidString(in) || strings.ContainsFunc(in, isControlRune) {
			return
		}
		assert.Equal(t, plain(Code(in)), in)
	})
}

// isControlRune is stricter than the package's own isControl, which exempts \n and
// \t: Escape flattens those to spaces, so an input carrying one cannot round-trip
// byte for byte and is skipped rather than asserted on.
func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }
