package dashboardurl_test

import (
	"strings"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/dashboardurl"
	"gotest.tools/v3/assert"
)

// TestParseRealURLs uses URLs copied from the dashboard rather than invented
// ones, because the shape is the whole contract and it is not documented
// anywhere.
func TestParseRealURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want dashboardurl.Ref
	}{
		{
			name: "an error with filter state",
			raw: "https://app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4" +
				"?filters[error.status]=open&filters[event.since]=30d",
			want: dashboardurl.Ref{
				OrgSlug:     "example-org",
				ProjectSlug: "example-api",
				ErrorID:     "63eb71ee1aac6900084b64c4",
				Filters: []dashboardurl.Filter{
					{Field: "error.status", Value: "open"},
					{Field: "event.since", Value: "30d"},
				},
			},
		},
		{
			name: "an error pinned to one event, with UI-only parameters",
			raw: "https://app.bugsnag.com/example-org/app/errors/6a694b36fdca5cb0d61c1b23" +
				"?event_id=6a694b36018f57b4af3f0000&i=sk&m=nw",
			want: dashboardurl.Ref{
				OrgSlug:     "example-org",
				ProjectSlug: "app",
				ErrorID:     "6a694b36fdca5cb0d61c1b23",
				EventID:     "6a694b36018f57b4af3f0000",
				Ignored:     []string{"i", "m"},
			},
		},
		{
			name: "an inbox, which names a project and nothing else",
			raw: "https://app.bugsnag.com/example-org/example-api/errors" +
				"?filters[error.status]=open&filters[event.since]=30d",
			want: dashboardurl.Ref{
				OrgSlug:     "example-org",
				ProjectSlug: "example-api",
				Filters: []dashboardurl.Filter{
					{Field: "error.status", Value: "open"},
					{Field: "event.since", Value: "30d"},
				},
			},
		},
		{
			name: "a trailing slash and the SmartBear host",
			raw:  "https://app.bugsnag.smartbear.com/acme/api/errors/",
			want: dashboardurl.Ref{OrgSlug: "acme", ProjectSlug: "api"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dashboardurl.Parse(tc.raw)
			assert.NilError(t, err)
			assert.Equal(t, got.OrgSlug, tc.want.OrgSlug)
			assert.Equal(t, got.ProjectSlug, tc.want.ProjectSlug)
			assert.Equal(t, got.ErrorID, tc.want.ErrorID)
			assert.Equal(t, got.EventID, tc.want.EventID)
			assert.DeepEqual(t, got.Filters, tc.want.Filters)
			assert.Equal(t, strings.Join(got.Ignored, ","), strings.Join(tc.want.Ignored, ","))
		})
	}
}

// TestParseRefusesHostileURLs. A URL comes from outside the process and the CLI
// holds a live token, so every one of these is a refusal rather than something to
// sanitise and carry on with.
func TestParseRefusesHostileURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"another host entirely":    "https://evil.test/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"a lookalike host":         "https://app.bugsnag.com.evil.test/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"a subdomain of the host":  "https://api.app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"credentials":              "https://user:pass@app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"a port":                   "https://app.bugsnag.com:8443/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"plain http":               "http://app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4",
		"traversal in the id":      "https://app.bugsnag.com/example-org/example-api/errors/..%2f..%2fusers",
		"traversal in the slug":    "https://app.bugsnag.com/example-org/../../errors/63eb71ee1aac6900084b64c4",
		"an id with punctuation":   "https://app.bugsnag.com/example-org/example-api/errors/not-an-id",
		"an event id with a slash": "https://app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4?event_id=a/b",
		"a different section":      "https://app.bugsnag.com/example-org/example-api/releases",
		"no project":               "https://app.bugsnag.com/example-org/errors",
		"extra path segments":      "https://app.bugsnag.com/example-org/example-api/errors/63eb71ee1aac6900084b64c4/events/1",
		"the dashboard root":       "https://app.bugsnag.com/example-org",
	} {
		t.Run(name, func(t *testing.T) {
			ref, err := dashboardurl.Parse(raw)
			assert.Assert(t, err != nil, "Parse(%q) was accepted as %+v", raw, ref)
		})
	}
}

// TestIsURLDoesNotGuess: an error id and an event id are both 24 hex characters,
// so only an explicit scheme makes something a URL.
func TestIsURLDoesNotGuess(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://app.bugsnag.com/example-org/example-api/errors/63eb": true,
		"http://app.bugsnag.com/example-org/example-api/errors/63eb":  true,
		"63eb71ee1aac6900084b64c4":                                    false,
		"app.bugsnag.com/example-org/example-api/errors/63eb":         false,
		"": false,
	} {
		assert.Equal(t, dashboardurl.IsURL(raw), want)
	}
}

// TestSeveralValuesOnOneFieldAreKept, since the dashboard can carry more than one
// and the API reads them as alternatives.
func TestSeveralValuesOnOneFieldAreKept(t *testing.T) {
	ref, err := dashboardurl.Parse("https://app.bugsnag.com/o/p/errors" +
		"?filters[error.status]=open&filters[error.status]=fixed")
	assert.NilError(t, err)

	want := []dashboardurl.Filter{
		{Field: "error.status", Value: "open"},
		{Field: "error.status", Value: "fixed"},
	}
	assert.DeepEqual(t, ref.Filters, want)
}
