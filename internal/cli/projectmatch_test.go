package cli

import (
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/config"
	"gotest.tools/v3/assert"
)

// matchable includes a prefix pair — campus and campus-replicator — which is what
// makes exact matching worth stating: campus can only match campus, never
// campus-replicator.
func matchable() []config.Project {
	return []config.Project{
		{ID: "p1", Name: "example-api", Slug: "example-api"},
		{ID: "p2", Name: "queue", Slug: "queue"},
		{ID: "p3", Name: "campus", Slug: "campus"},
		{ID: "p4", Name: "campus-replicator", Slug: "campus-replicator"},
		{ID: "p5", Name: "Marketing Site", Slug: "marketing-site"},
	}
}

func TestMatchProjectOnSlug(t *testing.T) {
	got, ok := matchProject("example-api", matchable())
	assert.Assert(t, ok, "example-api should match")
	assert.Equal(t, got.ID, "p1")
}

func TestMatchProjectOnName(t *testing.T) {
	got, ok := matchProject("Marketing Site", matchable())
	assert.Assert(t, ok, "the display name should match")
	assert.Equal(t, got.ID, "p5")
}

// TestPrefixPairsDoNotCrossMatch: exact matching cannot cross-match, so campus
// resolves to campus and campus-replicator to campus-replicator.
func TestPrefixPairsDoNotCrossMatch(t *testing.T) {
	for repo, wantID := range map[string]string{
		"campus":            "p3",
		"campus-replicator": "p4",
	} {
		t.Run(repo, func(t *testing.T) {
			got, ok := matchProject(repo, matchable())
			assert.Assert(t, ok, "%s should match", repo)
			assert.Equal(t, got.ID, wantID)
		})
	}
}

// TestSeparatorDifferencesDoNotResolve: matching is exact, so a repository named
// with a different separator no longer resolves — that is a `project link` case.
func TestSeparatorDifferencesDoNotResolve(t *testing.T) {
	for _, repo := range []string{"marketing_site", "marketing.site", "MarketingSite"} {
		_, ok := matchProject(repo, matchable())
		assert.Assert(t, !ok, "%s must not match under exact matching", repo)
	}
}

func TestNoMatchDoesNotResolve(t *testing.T) {
	_, ok := matchProject("something-else-entirely", matchable())
	assert.Assert(t, !ok, "an unmatched repository must not resolve silently")
}

// TestTiesDoNotResolve: two projects with the same name is a question, not an
// answer.
func TestTiesDoNotResolve(t *testing.T) {
	tied := []config.Project{
		{ID: "a", Name: "example-api", Slug: "example-api"},
		{ID: "b", Name: "example-api", Slug: "example-api"},
	}
	_, ok := matchProject("example-api", tied)
	assert.Assert(t, !ok, "a tie should ask rather than resolve")
}

func TestMatchProjectIsCaseInsensitive(t *testing.T) {
	got, ok := matchProject("EXAMPLE-API", matchable())
	assert.Assert(t, ok)
	assert.Equal(t, got.ID, "p1")
}

func TestEmptyProjectList(t *testing.T) {
	_, ok := matchProject("example-api", nil)
	assert.Assert(t, !ok, "an empty project list must not resolve")
}
