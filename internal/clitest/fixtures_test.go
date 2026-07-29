package clitest_test

import (
	"encoding/json"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/geckoboard/bugsnag-cli/internal/clitest"
	"github.com/geckoboard/bugsnag-cli/internal/view"
)

// TestEventFixturesMatchTheAPIShape guards against a fixture drifting out of the
// shape the real API produces. A key the generated type does not recognise — an
// app.release_stage where the API sends releaseStage — decodes to a nil field and
// the value silently vanishes, which is exactly how that casing slip hid until it
// was checked against the live API. Decoding each event fixture and asserting the
// fields the views read are populated turns that class of drift into a failure.
func TestEventFixturesMatchTheAPIShape(t *testing.T) {
	t.Run("thin list projection", func(t *testing.T) {
		for _, raw := range clitest.DefaultEvents() {
			var e view.Event
			assert.NilError(t, json.Unmarshal(raw, &e))
			assert.Assert(t, e.Class() != "", "error class did not decode: %s", raw)
			assert.Assert(t, e.App != nil && e.App.ReleaseStage != nil,
				"app.releaseStage did not decode; the fixture key may not match the API: %s", raw)
		}
	})

	t.Run("full report", func(t *testing.T) {
		var e view.Event
		assert.NilError(t, json.Unmarshal(clitest.DefaultEvent(), &e))
		assert.Assert(t, e.Class() != "", "error class did not decode")
		assert.Assert(t, e.App != nil && e.App.ReleaseStage != nil, "app.releaseStage did not decode")
		assert.Assert(t, e.App.Version != nil, "app.version did not decode")
	})
}
