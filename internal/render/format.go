// Package render is the output mechanism: how a result becomes bytes.
//
// It holds no policy about what any particular command shows — that lives in
// internal/view, whose functions are pure func(*Doc, T, Mode). Commands never
// see the output format; they build a Doc and the format is applied here.
//
// Text is the single authoring format, and this package owns every part of how
// it is laid out: word wrapping, column widths, truncation, gridlines and
// colour. On a terminal a table is drawn with box-drawing rules; piped it is
// tab-separated. Text is the default piped as well, because on real payloads it
// is several times smaller than the equivalent JSON, and --json is four
// characters away when the raw response is what you want.
//
// Views mark inline intent (a code span, bold, italic) with markdown notation,
// which internal/render/inline.go resolves into styling or drops. That notation
// is internal: nothing serialises it, and markdown is not an output format this
// tool offers.
package render

import "fmt"

// Format is how a result is serialised.
type Format int

const (
	// FormatText is the laid-out text form: gridlines and colour on a terminal,
	// tab-separated and unstyled when piped.
	FormatText Format = iota

	// FormatJSON emits the API's own JSON values, pretty-printed, with a
	// multi-page result concatenated into one array. It is deliberately not a
	// re-marshal of the generated types: metaData is map[string]interface{}, so
	// re-marshalling sends every number through float64, alphabetises keys, and
	// drops any field the closed sub-structs do not model. Passing the item bytes
	// through instead preserves numbers, key order and unknown fields exactly.
	FormatJSON
)

// ParseFormat parses the --format flag.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "text", "txt", "":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	}
	return 0, fmt.Errorf("unknown format %q: want text or json", s)
}
