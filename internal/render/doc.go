package render

import (
	"fmt"
	"strings"
)

// Doc is a list of nodes, rendered on demand.
//
// It is a node list rather than a string because layout has to be ours. A
// markdown string handed to a renderer gets its layout re-derived from the
// syntax, and that derivation cannot express a row spanning a table's width,
// which is exactly what the error list needs. Owning the nodes means owning the
// layout.
//
// Views mark inline intent with markdown notation (see inline.go), which the
// emitters resolve into styling or drop. Nothing serialises that notation, so it
// is internal markup, not an output format.
type Doc struct {
	mode  Mode
	nodes []node

	// items counts ordered-list entries so Item can number itself.
	items int

	// warnings are collected during rendering and reported once on stderr, so a
	// per-item decode failure degrades that item instead of failing the command.
	warnings []string
}

// New returns an empty Doc.
func New(m Mode) *Doc {
	if m.Width <= 0 {
		m.Width = DefaultWidth
	}
	return &Doc{mode: m}
}

// Mode returns the Doc's mode, so views can ask about width and TTY without it
// being passed separately.
func (d *Doc) Mode() Mode { return d.mode }

// H1 writes a top-level heading. Every document this tool produces starts with
// one, which is asserted in tests: it is the cheapest signal that the styled
// path ran rather than something falling through to raw JSON.
func (d *Doc) H1(format string, args ...any) {
	d.add(node{kind: nodeHeading, text: sprintf(format, args...)})
}

// H2 writes a section heading.
func (d *Doc) H2(format string, args ...any) {
	d.add(node{kind: nodeSubheading, text: sprintf(format, args...)})
}

// Text writes a paragraph, soft-wrapped to the content width on a terminal.
func (d *Doc) Text(format string, args ...any) {
	d.add(node{kind: nodeParagraph, text: sprintf(format, args...)})
}

// Line writes one logical line: a composed run of facts that belongs together.
// On a terminal it folds at spaces if it has to, never inside a word, so an id or
// a URL on it stays copyable.
func (d *Doc) Line(format string, args ...any) {
	d.add(node{kind: nodeLine, text: sprintf(format, args...)})
}

// Field writes a labelled line, the shape the error detail header uses:
//
//	Status: open · handled · severity warning
func (d *Doc) Field(label, format string, args ...any) {
	d.add(node{kind: nodeField, label: label, text: sprintf(format, args...)})
}

// Footer writes a trailing line, used for the "More:" hints and for the
// mandatory line naming what was hidden. It is de-emphasised on a terminal.
func (d *Doc) Footer(format string, args ...any) {
	d.add(node{kind: nodeFooter, text: sprintf(format, args...)})
}

// Blank forces a blank line between two blocks that would otherwise be tight.
func (d *Doc) Blank() { d.add(node{kind: nodeBlank}) }

// Warnf records a warning to be reported once on stderr after the document is
// written. It is how a per-item decode failure degrades to a marked row instead
// of failing the whole command.
func (d *Doc) Warnf(format string, args ...any) {
	d.warnings = append(d.warnings, sprintf(format, args...))
}

// Warnings returns the collected warnings.
func (d *Doc) Warnings() []string { return d.warnings }

// Item opens a numbered block. Lines added to it stay separate lines, and the
// continuation lines are indented to line up under the first.
func (d *Doc) Item() *Item {
	d.items++
	prefix := fmt.Sprintf("%d. ", d.items)
	return &Item{
		doc:    d,
		prefix: prefix,
		indent: strings.Repeat(" ", len(prefix)),
	}
}

// ResetItems restarts ordered-list numbering.
func (d *Doc) ResetItems() { d.items = 0 }

// String renders the document.
func (d *Doc) String() string {
	th := plainTheme
	if d.mode.TTY {
		th = terminalTheme()
	}

	var b strings.Builder
	forced, prev := false, nodeBlank

	for _, n := range d.nodes {
		if n.kind == nodeBlank {
			forced = true
			continue
		}
		if b.Len() > 0 && (forced || needsBlankLine(prev, n.kind)) {
			b.WriteByte('\n')
		}
		d.writeNode(&b, n, th)
		forced, prev = false, n.kind
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// node is one block of the document. Each carries its inline notation
// unresolved, because how it is resolved depends on the theme.
type node struct {
	kind nodeKind

	// text is the node's inline notation, or the value half of a field.
	text string

	// label is the label half of a field.
	label string

	// lines are an item block's lines, already positioned.
	lines []string

	// table is the table builder the node renders from.
	table *Table
}

type nodeKind int

const (
	nodeBlank nodeKind = iota
	nodeHeading
	nodeSubheading
	nodeParagraph
	nodeLine
	nodeField
	nodeItem
	nodeTable
	nodeFooter
)

func (d *Doc) add(n node) { d.nodes = append(d.nodes, n) }

// wrapWidth is the width lines are wrapped to, or zero when they are not
// wrapped at all.
//
// Wrapping is a terminal concern. Piped, a paragraph and a field each stay one
// line, for the same reason a piped table is neither padded nor truncated: a
// reader that is going to parse or diff the output should not have to reassemble
// something we broke up to fit a terminal it is not using.
func (d *Doc) wrapWidth() int {
	if !d.mode.TTY {
		return 0
	}
	return proseWidth(d.mode.Width)
}

func (d *Doc) writeNode(b *strings.Builder, n node, th theme) {
	switch n.kind {
	case nodeHeading:
		writeLine(b, style(th.heading, resolveInline(n.text, th.inline)))

	case nodeSubheading:
		writeLine(b, style(th.subheading, resolveInline(n.text, th.inline)))

	case nodeParagraph:
		// Wrapping happens after the markup is resolved, so the widths measured
		// are the widths shown.
		for _, line := range Wrap(resolveInline(n.text, th.inline), d.wrapWidth()) {
			writeLine(b, line)
		}

	case nodeLine:
		// Wrapped at spaces but never inside a word: these lines are composed
		// runs of ids, URLs and type names, and breaking one of those makes it
		// uncopyable. Something has to wrap them, because the terminal's own fold
		// lands mid-word.
		for _, line := range wrapWords(resolveInline(n.text, th.inline), d.wrapWidth()) {
			writeLine(b, line)
		}

	case nodeField:
		text := style(th.label, n.label) + ": " + resolveInline(n.text, th.inline)
		for _, line := range wrapIndented(text, d.wrapWidth(), "  ") {
			writeLine(b, line)
		}

	case nodeItem:
		for _, line := range n.lines {
			writeLine(b, resolveInline(line, th.inline))
		}

	case nodeTable:
		n.table.write(b, th, d.mode)

	case nodeFooter:
		for _, line := range wrapWords(resolveInline(n.text, th.inline), d.wrapWidth()) {
			writeLine(b, style(th.footer, line))
		}
	}
}

// needsBlankLine reports whether a blank line separates two blocks. The list
// kinds are tight against each other, so consecutive fields read as one block
// rather than as a spaced-out list.
func needsBlankLine(prev, cur nodeKind) bool {
	return !(tightKind(prev) && tightKind(cur))
}

func tightKind(k nodeKind) bool {
	return k == nodeField || k == nodeItem
}

// Item is one numbered block, used for a stack trace's frames.
//
// Lines are buffered rather than written straight through, because the block is
// one node and the continuation indent is applied as they are added.
type Item struct {
	doc    *Doc
	prefix string
	indent string
	lines  []string
}

// Line adds a line to the block.
func (it *Item) Line(format string, args ...any) *Item {
	it.lines = append(it.lines, sprintf(format, args...))
	return it
}

// Done writes the block into the document.
func (it *Item) Done() {
	if len(it.lines) == 0 {
		return
	}

	lines := make([]string, len(it.lines))
	for i, line := range it.lines {
		if i == 0 {
			lines[i] = it.prefix + line
		} else {
			lines[i] = it.indent + line
		}
	}
	it.doc.add(node{kind: nodeItem, lines: lines})
}

// Escape makes arbitrary data safe to drop into inline notation.
//
// Error messages are arbitrary text: they contain asterisks (Go type names like
// *fmt.wrapError), underscores and backticks. Left alone a backtick would open
// a code span and a **run** would embolden.
func Escape(s string) string {
	s = flattenLines.Replace(s)

	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '|':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Code marks s as a code span. Data that is meant to be copied — ids, hosts,
// release versions — goes through here so it is coloured as a copy target and
// its contents are not read as notation.
func Code(s string) string {
	if s == "" {
		return ""
	}
	// A code span can contain backticks if it is fenced by a longer run, which
	// is the only escaping mechanism code spans have.
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	// A space at either end needs the pad too, not just a backtick: the resolver
	// strips one space from each end of a padded span, so an unpadded " x " would
	// come back as "x". A value that is nothing but spaces is the exception —
	// the resolver leaves an all-space span alone, so padding one would add two
	// spaces rather than protect any.
	pad := ""
	if strings.TrimSpace(s) != "" && (hasEdge(s, "`") || hasEdge(s, " ")) {
		pad = " "
	}
	return fence + pad + s + pad + fence
}

func hasEdge(s, affix string) bool {
	return strings.HasPrefix(s, affix) || strings.HasSuffix(s, affix)
}

// flattenLines collapses the whitespace that would otherwise break a line or a
// table cell in two. Built once rather than per call, since table cells go
// through it one at a time.
var flattenLines = strings.NewReplacer(
	"\r", " ",
	"\n", " ",
	"\t", " ",
)

func style(f func(string) string, s string) string {
	if f == nil || s == "" {
		return s
	}
	return f(s)
}

func writeLine(b *strings.Builder, s string) {
	b.WriteString(strings.TrimRight(s, " "))
	b.WriteByte('\n')
}

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
