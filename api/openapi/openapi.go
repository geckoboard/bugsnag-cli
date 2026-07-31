// Package openapi reads the vendored spec, for finding out what the API offers.
//
// It sits beside the spec because that is the only way to embed it: go:embed
// cannot reach outside its own package directory. Embedding is what makes this
// available to an installed binary, and reading it from the same file the client
// is generated from is what stops the two disagreeing about what the API offers —
// including the endpoints codegen prunes, which are exactly the ones the raw
// passthrough exists for.
//
// The spec is read as vendored: overlay.yaml is not applied, and should not be.
// That overlay exists to make codegen work, not to describe the API more
// truthfully — it removes the filter parameters because their generated encoder
// is wrong, and narrows `number` to `integer` because float32 is the wrong Go
// type. Neither changes what the API accepts. For discovery the unpatched spec is
// the better document: the filter parameters it still describes are exactly what
// `--query` can send, and every other patch lands on a response schema, which
// appears here only as the $ref it is.
//
// The spec is parsed on first use and kept, so the cost is paid by the discovery
// flags and by nothing else.
package openapi

import (
	"bytes"
	_ "embed"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed bugsnag-data-access-api.yaml
var spec []byte

// Endpoint is one path the spec describes.
type Endpoint struct {
	Path string

	// Summary is the spec's own one-line description, or empty when it gives none.
	Summary string
}

// Readable returns every endpoint the spec describes a GET for, sorted by path.
//
// Only GET is listed because that is all this tool sends; the write operations in
// the spec would be a catalogue of things it will refuse to do.
func Readable() ([]Endpoint, error) {
	ops, err := operations()
	if err != nil {
		return nil, err
	}

	out := make([]Endpoint, 0, len(ops))
	for path, get := range ops {
		out = append(out, Endpoint{Path: path, Summary: text(get, "summary")})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Describe returns the spec's own YAML for one endpoint: its parameters with
// their types, defaults and examples, and the shape it answers with.
//
// The spec is handed over rather than summarised into a table because everything
// in it is worth having and a summary would have to choose. It is re-encoded from
// the parsed document, so it stays a YAML document that can be read by eye or
// piped into a parser.
//
// A path is matched as typed or as a template, so a path that was just requested
// can be looked up with its ids still in it rather than put back into the form the
// spec writes it in.
func Describe(path string) (string, bool, error) {
	ops, err := operations()
	if err != nil {
		return "", false, err
	}

	get, ok := ops[path]
	if !ok {
		if path, get, ok = match(ops, path); !ok {
			return "", false, nil
		}
	}

	// Wrapped back up under its path and method, so the fragment says what it
	// describes and reads as the part of the spec it is.
	doc := mapping(scalar(path), mapping(scalar("get"), get))

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", false, err
	}
	if err := enc.Close(); err != nil {
		return "", false, err
	}
	return buf.String(), true, nil
}

// match finds the template a concrete path fits, e.g. /projects/abc/errors for
// /projects/{project_id}/errors. A templated segment matches any one segment, so
// a path can only match a template of its own shape.
func match(ops map[string]*yaml.Node, path string) (string, *yaml.Node, bool) {
	want := segments(path)

	// The spec's paths are walked in sorted order, so a path that could fit two
	// templates resolves to the same one every time.
	candidates := make([]string, 0, len(ops))
	for candidate := range ops {
		candidates = append(candidates, candidate)
	}
	sort.Strings(candidates)

	for _, candidate := range candidates {
		got := segments(candidate)
		if len(got) != len(want) {
			continue
		}

		fits := true
		for i, segment := range got {
			if !isPlaceholder(segment) && segment != want[i] {
				fits = false
				break
			}
		}
		if fits {
			return candidate, ops[candidate], true
		}
	}
	return "", nil, false
}

func segments(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}

// isPlaceholder reports whether a path segment is a template parameter.
func isPlaceholder(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// operations returns the GET operation of every path, keyed by the path to
// request. Parsing happens once: the spec is half a megabyte, and every caller
// here reads the same tree.
var operations = sync.OnceValues(parse)

func parse() (map[string]*yaml.Node, error) {
	var doc struct {
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, err
	}

	out := make(map[string]*yaml.Node, len(doc.Paths))
	for path, item := range doc.Paths {
		get, ok := field(&item, "get")
		if !ok {
			continue
		}
		if fixed, wrong := corrections[path]; wrong {
			path = fixed
		}
		out[path] = get
	}
	return out, nil
}

// corrections are the paths the spec gets wrong, so discovery does not hand out
// an address that 404s.
//
// An entry goes here only once both halves are verified against the live API.
// The trend endpoints are the known case, and the spec gets both of them wrong
// the same way: /trend answers at project and at error level, /trends 404s at
// both. It is the only route-level error found by requesting every path whose
// parameters could be filled in — the other failures there were missing
// parameters, plan features and one 500, all of them real routes. The error-level
// one is also why internal/cli patches the URL it builds for the trend.
//
// This cannot move to the overlay, and would still be needed here if it did. An
// overlay action can update or remove its target but not rename a key, so
// renaming a path means inlining the whole path item — which a spec refresh would
// leave stale while strict mode still called the action applied. And the overlay
// is a separate document from the spec this reads.
var corrections = map[string]string{
	"/projects/{project_id}/errors/{error_id}/trends": "/projects/{project_id}/errors/{error_id}/trend",
	"/projects/{project_id}/trends":                   "/projects/{project_id}/trend",
}

// field returns a mapping node's value for key.
//
// The spec is walked as nodes rather than decoded into typed maps because the
// fragment is handed back as it was written, which only the nodes still hold.
func field(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

// text returns a scalar field's value, or empty when it is absent.
func text(node *yaml.Node, key string) string {
	v, ok := field(node, key)
	if !ok {
		return ""
	}
	return v.Value
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func mapping(key, value *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{key, value}}
}
