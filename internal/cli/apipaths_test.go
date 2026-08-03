package cli

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/geckoboard/bugsnag-cli/api/openapi"
)

// TestEveryMappedPathIsRealKeeps the catalogue's steer away from the raw
// passthrough working. A key that matches no path renders as a blank cell, so a
// spec refresh that renames an endpoint would silently stop pointing at the
// command that reads it properly.
func TestEveryMappedPathIsReal(t *testing.T) {
	endpoints, err := openapi.Readable()
	assert.NilError(t, err)

	listed := make(map[string]bool, len(endpoints))
	for _, e := range endpoints {
		listed[e.Path] = true
	}

	for path := range commandsByPath {
		assert.Check(t, listed[path], "%q names a command but is in no listed path", path)
	}
}
