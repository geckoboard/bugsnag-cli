package render

import (
	"testing"

	"gotest.tools/v3/assert"
)

// TestCodeHandlesBackticks: release versions and hosts go through Code, and a
// value containing a backtick must not break out of the span.
func TestCodeHandlesBackticks(t *testing.T) {

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "1e59939", "`1e59939`"},
		{"empty stays empty", "", ""},
		{"inner backtick uses a longer fence", "a`b", "``a`b``"},
		{"leading backtick is padded", "`x", "`` `x ``"},
		// A padded span loses one space from each end on the way out, so a value
		// with its own edge spaces needs the pad too or it comes back trimmed.
		{"edge spaces are padded", " x ", "`  x  `"},
		{"trailing space is padded", "x ", "` x  `"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, Code(tc.in), tc.want)
			assert.Equal(t, plain(Code(tc.in)), tc.in)
		})
	}
}
