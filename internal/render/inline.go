package render

import "strings"

// Inline notation.
//
// Views mark intent with a small subset of markdown — `code`, **bold**,
// _italic_ and backslash escapes — and this file is the only thing that reads
// it. It is internal notation, not an output format: every emitter resolves it
// before writing, so the notation never reaches a reader.
//
// The subset is deliberately smaller than markdown. A single asterisk is not
// emphasis, because Go type names are full of them and a cell that escaped its
// data would otherwise be at the mercy of one that did not.

// inlineStyle is how resolved markup is rendered. A nil function means "keep
// the text, drop the markup", which is the piped form.
type inlineStyle struct {
	code   func(string) string
	strong func(string) string
	emph   func(string) string
}

// resolveInline turns inline notation into text, styled by sty.
func resolveInline(s string, sty inlineStyle) string {
	s = withoutControls(s)

	if !strings.ContainsAny(s, "`*_\\") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s) && isEscapable(s[i+1]):
			b.WriteByte(s[i+1])
			i += 2

		case s[i] == '`':
			fence := runLength(s, i, '`')
			end := indexRun(s, i+fence, '`', fence)
			if end < 0 {
				// An unmatched backtick is literal, exactly as in markdown.
				b.WriteString(s[i : i+fence])
				i += fence
				continue
			}
			// A code span's content is literal: no further notation inside it,
			// which is why data that must survive intact goes through Code.
			b.WriteString(apply(sty.code, unpadCode(s[i+fence:end])))
			i = end + fence

		case strings.HasPrefix(s[i:], "**") && canOpen(s, i):
			end := closingRun(s, i+2, "**")
			if end < 0 {
				b.WriteString("**")
				i += 2
				continue
			}
			b.WriteString(apply(sty.strong, resolveInline(s[i+2:end], sty)))
			i = end + 2

		case s[i] == '_':
			// Only a lone underscore is a marker, and only outside a word. A run
			// of them never is, which is what leaves Python's __init__ and any
			// snake_case value alone — markdown itself would embolden the one and
			// italicise inside the other.
			run := runLength(s, i, '_')
			end := -1
			if run == 1 && canOpen(s, i) {
				end = closingSingle(s, i+1, '_')
			}
			if end < 0 {
				b.WriteString(s[i : i+run])
				i += run
				continue
			}
			b.WriteString(apply(sty.emph, resolveInline(s[i+1:end], sty)))
			i = end + 1

		default:
			b.WriteByte(s[i])
			i++
		}
	}

	return b.String()
}

func apply(f func(string) string, s string) string {
	if f == nil || s == "" {
		return s
	}
	return f(s)
}

// canOpen reports whether a marker at i opens a span rather than sitting inside
// a word.
//
// This is markdown's own intraword rule, and it is what keeps data safe. Values
// this tool prints are full of underscores — release stages like `pre_prod`, app
// versions like `1.2.3_beta`, a dashboard URL's `?event_id=` — and a view that
// did not escape one would otherwise have both markers silently deleted and the
// text between them emphasised.
func canOpen(s string, i int) bool {
	if i == 0 {
		return true
	}
	return !isWordByte(s[i-1])
}

// closingSingle finds the lone c that closes a span, skipping any that is part of
// a run or that sits inside a word.
func closingSingle(s string, i int, c byte) int {
	for at := i; at < len(s); at++ {
		if s[at] != c {
			continue
		}
		if run := runLength(s, at, c); run != 1 {
			at += run - 1
			continue
		}
		if at == i {
			return -1
		}
		if at+1 < len(s) && isWordByte(s[at+1]) {
			continue
		}
		return at
	}
	return -1
}

// closingRun finds the marker that closes a span opened at or after i, skipping
// any that would sit inside a word, and rejecting an empty span so a bare `**`
// stays literal rather than resolving to nothing.
func closingRun(s string, i int, marker string) int {
	for at := i; at < len(s); at++ {
		if !strings.HasPrefix(s[at:], marker) {
			continue
		}
		if at == i {
			// An empty span: not emphasis, just two markers.
			return -1
		}
		after := at + len(marker)
		if after < len(s) && isWordByte(s[after]) {
			continue
		}
		return at
	}
	return -1
}

// isWordByte reports whether c is the kind of character that makes a marker next
// to it intraword. Any byte of a multi-byte rune counts, so an accented word is
// treated like an unaccented one.
func isWordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c >= 0x80:
		return true
	}
	return false
}

// withoutControls replaces control characters with spaces.
//
// API data reaches here unmodified, and an error message that carries an escape
// sequence would otherwise have it reassembled into the output: Escape puts a
// backslash before the `[` of a CSI sequence, and this pass then removes that
// backslash again. A message could colour itself, or set the terminal's title
// with an OSC sequence. Tabs and newlines are left alone, because a paragraph is
// allowed to contain them and a table cell has already had them flattened.
func withoutControls(s string) string {
	if strings.IndexFunc(s, isControl) < 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isControl(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f
}

// isEscapable reports whether a backslash before c is an escape rather than a
// literal backslash.
//
// The set is exactly what Escape emits, plus '>' to keep the angle-bracket pair
// symmetric. Accepting more than that would silently eat a literal backslash in
// data that happens to precede one of them.
func isEscapable(c byte) bool {
	switch c {
	case '\\', '`', '*', '_', '[', ']', '<', '>', '|':
		return true
	}
	return false
}

// unpadCode drops the single space markdown uses to hold a backtick away from
// the fence, which is what Code adds for a value that starts or ends with one.
func unpadCode(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ") && strings.TrimSpace(s) != "" {
		return s[1 : len(s)-1]
	}
	return s
}

func runLength(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// indexRun finds a run of exactly n cs at or after i, which is how a code span
// closes: a shorter run is content, a longer one is a different span.
func indexRun(s string, i int, c byte, n int) int {
	for i < len(s) {
		if s[i] != c {
			i++
			continue
		}
		if runLength(s, i, c) == n {
			return i
		}
		i += runLength(s, i, c)
	}
	return -1
}
