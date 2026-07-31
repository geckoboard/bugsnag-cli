package openapi_test

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/api/openapi"
)

// TestReadableIsTheCatalogueOfWhatCanBeAsked. It is the whole spec rather than
// the pruned client, which is the point: the endpoints codegen leaves out are
// exactly the ones worth discovering.
func TestReadableIsTheCatalogueOfWhatCanBeAsked(t *testing.T) {

	endpoints, err := openapi.Readable()
	assert.NilError(t, err)
	assert.Check(t, len(endpoints) > 50, "only %d endpoints; the spec was not read", len(endpoints))

	paths := make(map[string]string, len(endpoints))
	for i, e := range endpoints {
		paths[e.Path] = e.Summary
		if i > 0 {
			assert.Check(t, endpoints[i-1].Path < e.Path,
				"not sorted by path: %q before %q", endpoints[i-1].Path, e.Path)
		}
	}

	// The summary is most of the value: a path alone rarely says what it returns.
	assert.Equal(t, paths["/projects/{project_id}/errors"], "List the Errors on a Project")

	// A write-only endpoint is something this tool will never send, so listing it
	// would only be a way to reach a refusal.
	_, listed := paths["/organizations/{organization_id}/collaborators/bulk_invite"]
	assert.Check(t, !listed, "a POST-only endpoint is in the catalogue")
}

// TestDescribeHandsBackTheSpecItself, rather than a summary of it: the
// parameters, their defaults and enums, and the shape the endpoint answers with
// are all worth having, and choosing between them is what a summary would have to
// do.
func TestDescribeHandsBackTheSpecItself(t *testing.T) {

	fragment, found, err := openapi.Describe("/projects/{project_id}/releases")
	assert.NilError(t, err)
	assert.Assert(t, found)

	// It has to stay a YAML document, since the point is that it can be piped
	// into a parser as readily as read.
	var doc map[string]map[string]any
	assert.NilError(t, yaml.Unmarshal([]byte(fragment), &doc),
		"the fragment is not valid YAML:\n%s", fragment)

	get, ok := doc["/projects/{project_id}/releases"]["get"].(map[string]any)
	assert.Assert(t, ok, "the fragment is not keyed by path and method:\n%s", fragment)

	// The query parameters are the reason to look: they are what --query takes.
	assert.Check(t, is.Contains(fragment, "name: per_page"))
	assert.Check(t, is.Contains(fragment, "default: 5"))
	assert.Check(t, get["summary"] != nil)
}

// TestDescribeKeepsWhatTheOverlayRemoves. The overlay drops the filter parameters
// so codegen cannot emit its broken encoder for them, but they are real: the
// bracket syntax they describe is what --query sends. Discovery reads the spec as
// vendored so it still names them.
func TestDescribeKeepsWhatTheOverlayRemoves(t *testing.T) {

	fragment, found, err := openapi.Describe("/projects/{project_id}/errors")
	assert.NilError(t, err)
	assert.Assert(t, found)

	assert.Check(t, is.Contains(fragment, "name: filters"))
}

// TestDescribeMatchesAPathAsTyped: the path worth asking about is usually the one
// just requested, which has its ids in it rather than the spec's placeholders.
func TestDescribeMatchesAPathAsTyped(t *testing.T) {

	fragment, found, err := openapi.Describe("/projects/515fb933/errors/6a3272b8/events")
	assert.NilError(t, err)
	assert.Assert(t, found)
	assert.Check(t, is.Contains(fragment, "/projects/{project_id}/errors/{error_id}/events:"))
}

// TestDescribeMissesRatherThanGuesses. A path of another shape has no template it
// could belong to, and answering with the nearest one would describe the wrong
// endpoint.
func TestDescribeMissesRatherThanGuesses(t *testing.T) {

	for _, path := range []string{"/nope/whatever", "/projects", ""} {
		_, found, err := openapi.Describe(path)
		assert.NilError(t, err)
		assert.Check(t, !found, "%q matched something", path)
	}
}

// TestTheTrendPathsAreTheOnesThatAnswer. The spec says /trends at both project
// and error level; verified live, both of those 404 and both singulars answer. A
// catalogue that handed out the spec's paths would be sending people to a dead
// address.
func TestTheTrendPathsAreTheOnesThatAnswer(t *testing.T) {

	endpoints, err := openapi.Readable()
	assert.NilError(t, err)

	var paths []string
	for _, e := range endpoints {
		paths = append(paths, e.Path)
	}

	for _, answers := range []string{
		"/projects/{project_id}/trend",
		"/projects/{project_id}/errors/{error_id}/trend",
	} {
		assert.Check(t, slices.Contains(paths, answers), "%s is missing", answers)
		assert.Check(t, !slices.Contains(paths, answers+"s"), "%ss is still listed", answers)
	}
}
