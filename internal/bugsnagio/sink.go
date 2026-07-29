package bugsnagio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
)

// JSONSink writes the API's own item bytes, for --json.
//
// The values are faithful: it does not re-marshal, so numbers, key order and any
// field the generated types do not model all survive exactly as the API sent
// them, which is why the raw path does not go through those types. The bytes are
// not identical to the API's, though — items are re-indented for readability, and
// a multi-page result is concatenated into a single array — so this is not a
// byte-for-byte copy of a `curl`.
type JSONSink struct {
	w     io.Writer
	array bool

	wrote bool
	err   error
}

// NewJSONSink returns a sink writing to w. Set array for a list endpoint, so the
// items are wrapped in a JSON array.
func NewJSONSink(w io.Writer, array bool) *JSONSink {
	return &JSONSink{w: w, array: array}
}

// Item writes one item.
func (s *JSONSink) Item(raw json.RawMessage) error {
	if s.err != nil {
		return s.err
	}

	if s.array {
		if !s.wrote {
			s.write("[\n")
		} else {
			s.write(",\n")
		}
	}

	var buf bytes.Buffer
	indent := ""
	if s.array {
		indent = "  "
	}
	if err := json.Indent(&buf, raw, indent, "  "); err != nil {
		// An item we cannot even re-indent is written through verbatim: the
		// point of this path is to preserve the API's values, not to reformat.
		s.write(string(raw))
	} else {
		if s.array {
			s.write(indent)
		}
		s.write(buf.String())
	}

	s.wrote = true
	return s.err
}

// Close finishes the document.
func (s *JSONSink) Close(Meta) error {
	if s.err != nil {
		return s.err
	}
	if s.array {
		if s.wrote {
			s.write("\n]\n")
		} else {
			s.write("[]\n")
		}
	} else {
		s.write("\n")
	}
	return s.err
}

func (s *JSONSink) write(str string) {
	if s.err != nil {
		return
	}
	if _, err := io.WriteString(s.w, str); err != nil {
		s.err = apierr.Wrap(apierr.KindInternal, err, "writing output")
	}
}

// TypedSink decodes items into T for the text path.
//
// Decoding degrades per item rather than failing the command. A single field the
// spec models wrongly would otherwise take out the whole response: type
// reopen_rules as a bool, as the spec implies, and `errors get` fails outright on
// any error that has one.
//
// Tier 1 is the generated type. Tier 2 is map[string]any, which lets the view
// still show a marked row with whatever it can read. Tier 3, an item that is not
// even valid JSON, is a KindDecode error, because at that point there is nothing
// truthful left to show.
type TypedSink[T any] struct {
	// Items are the successfully decoded items.
	Items []T

	// Degraded holds items that failed the typed decode but were readable as
	// generic JSON, in the order they arrived.
	Degraded []Degraded

	// Meta is set when Close runs.
	Meta Meta
}

// Degraded is an item that could not be decoded into the generated type.
type Degraded struct {
	// Index is the item's position in the response.
	Index int

	// Fields is whatever could be read generically.
	Fields map[string]any

	// Err is why the typed decode failed.
	Err error
}

// NewTypedSink returns a sink collecting T.
func NewTypedSink[T any]() *TypedSink[T] { return &TypedSink[T]{} }

// Item decodes one item.
func (s *TypedSink[T]) Item(raw json.RawMessage) error {
	var item T
	err := json.Unmarshal(raw, &item)
	if err == nil {
		s.Items = append(s.Items, item)
		return nil
	}

	var fields map[string]any
	if jerr := json.Unmarshal(raw, &fields); jerr != nil {
		return apierr.Wrap(apierr.KindDecode, jerr, "item %d is not valid JSON", s.count())
	}

	s.Degraded = append(s.Degraded, Degraded{
		Index:  s.count(),
		Fields: fields,
		Err:    err,
	})
	return nil
}

// Close records the response metadata.
func (s *TypedSink[T]) Close(meta Meta) error {
	s.Meta = meta
	return nil
}

// Warning returns the one-line notice to print on stderr when some items could
// not be fully decoded, or empty when they all decoded.
//
// It goes to stderr and the command still exits 0: the output is incomplete but
// truthful, and failing the whole command over one bad item would throw away
// every good one.
func (s *TypedSink[T]) Warning() string {
	if len(s.Degraded) == 0 {
		return ""
	}
	total := len(s.Items) + len(s.Degraded)
	return fmt.Sprintf(
		"warning: %d of %d items could not be fully decoded (use --json for the raw response)",
		len(s.Degraded), total)
}

func (s *TypedSink[T]) count() int { return len(s.Items) + len(s.Degraded) }

// TeeSink feeds every item to several sinks, so one pass over the response can
// serve more than one consumer.
type TeeSink []Sink

// Item passes raw to each sink.
func (t TeeSink) Item(raw json.RawMessage) error {
	for _, s := range t {
		if err := s.Item(raw); err != nil {
			return err
		}
	}
	return nil
}

// Close closes each sink.
func (t TeeSink) Close(meta Meta) error {
	for _, s := range t {
		if err := s.Close(meta); err != nil {
			return err
		}
	}
	return nil
}
