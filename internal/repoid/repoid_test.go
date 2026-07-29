package repoid_test

import (
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/repoid"
	"gotest.tools/v3/assert"
)

// TestCanonicalCollapsesEquivalentForms is load-bearing: the remotes across one
// checkout directory are a mix of bare and .git-suffixed, SSH and HTTPS. All of
// these name one repository and must produce one config key, or the same repo
// gets a second cache entry and autodetect runs again.
func TestCanonicalCollapsesEquivalentForms(t *testing.T) {

	const want = repoid.Identity("github.com/example-org/example-api")

	for _, remote := range []string{
		"https://github.com/example-org/example-api",
		"https://github.com/example-org/example-api.git",
		"https://github.com/example-org/example-api/",
		"http://github.com/example-org/example-api",
		"git@github.com:example-org/example-api",
		"git@github.com:example-org/example-api.git",
		"ssh://git@github.com/example-org/example-api.git",
		"ssh://git@github.com:22/example-org/example-api.git",
		"git://github.com/example-org/example-api.git",
		"github.com:example-org/example-api.git",
		"https://GitHub.com/Example-Org/Example-API.git",
		"https://user:token@github.com/example-org/example-api.git",
	} {
		t.Run(remote, func(t *testing.T) {
			got, ok := repoid.Canonical(remote)
			assert.Assert(t, ok, "Canonical(%q) failed", remote)
			assert.Equal(t, got, want)
		})
	}
}

// TestForksStayDistinct: two forks are usually different Bugsnag projects, so
// collapsing them onto the upstream identity would report the wrong project's
// errors.
func TestForksStayDistinct(t *testing.T) {

	mine, _ := repoid.Canonical("git@github.com:glenmailer/example-api.git")
	theirs, _ := repoid.Canonical("git@github.com:example-org/example-api.git")

	assert.Assert(t, mine != theirs, "a fork collapsed onto its upstream: both are %q", mine)
}

// TestDifferentHostsStayDistinct: the same path on another host is another
// repository.
func TestDifferentHostsStayDistinct(t *testing.T) {

	gh, _ := repoid.Canonical("git@github.com:example-org/example-api.git")
	gl, _ := repoid.Canonical("git@gitlab.com:example-org/example-api.git")

	assert.Assert(t, gh != gl, "two hosts collapsed onto %q", gh)
}

func TestCanonicalRejectsUnusableRemotes(t *testing.T) {

	for _, remote := range []string{
		"",
		"   ",
		"/Users/glen/Development/local-only",
		"./relative",
		"file:///Users/glen/repo",
		"justaword",
	} {
		got, ok := repoid.Canonical(remote)
		assert.Assert(t, !ok, "Canonical(%q) = %q, want it rejected", remote, got)
	}
}

func TestFromRemotesPreference(t *testing.T) {

	for _, tc := range []struct {
		name    string
		remotes map[string]string
		want    repoid.Identity
		remote  string
		ok      bool
	}{
		{
			name: "origin wins",
			remotes: map[string]string{
				"origin":   "git@github.com:glenmailer/example-api.git",
				"upstream": "git@github.com:example-org/example-api.git",
			},
			want:   "github.com/glenmailer/example-api",
			remote: "origin",
			ok:     true,
		},
		{
			name: "upstream when there is no origin",
			remotes: map[string]string{
				"upstream": "git@github.com:example-org/example-api.git",
			},
			want:   "github.com/example-org/example-api",
			remote: "upstream",
			ok:     true,
		},
		{
			name: "a single remote under another name",
			remotes: map[string]string{
				"gecko": "git@github.com:example-org/example-api.git",
			},
			want:   "github.com/example-org/example-api",
			remote: "gecko",
			ok:     true,
		},
		{
			name: "several remotes but no known name is ambiguous",
			remotes: map[string]string{
				"a": "git@github.com:example-org/example-api.git",
				"b": "git@github.com:example-org/queue.git",
			},
			ok: false,
		},
		{
			name:    "no remotes",
			remotes: map[string]string{},
			ok:      false,
		},
		{
			name: "origin that cannot be canonicalised falls through",
			remotes: map[string]string{
				"origin":   "/Users/glen/local",
				"upstream": "git@github.com:example-org/example-api.git",
			},
			want:   "github.com/example-org/example-api",
			remote: "upstream",
			ok:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {

			got, remote, ok := repoid.FromRemotes(tc.remotes)
			assert.Equal(t, ok, tc.ok)
			if !ok {
				return
			}
			assert.Equal(t, got, tc.want)
			assert.Equal(t, remote, tc.remote)
		})
	}
}

func TestRepoName(t *testing.T) {

	for _, tc := range []struct {
		id   repoid.Identity
		name string
	}{
		{"github.com/example-org/example-api", "example-api"},
		{"github.com/example-org/worker-replica", "worker-replica"},
		{"gitlab.com/group/subgroup/thing", "thing"},
	} {
		assert.Equal(t, tc.id.RepoName(), tc.name)
	}
}
