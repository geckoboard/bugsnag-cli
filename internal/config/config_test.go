package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

func TestPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{
			name: "XDG_CONFIG_HOME wins",
			xdg:  "/xdg",
			home: "/home/glen",
			want: "/xdg/bugsnag/config.yaml",
		},
		{
			name: "falls back to HOME",
			home: "/home/glen",
			want: "/home/glen/.config/bugsnag/config.yaml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			t.Setenv("HOME", tc.home)

			got, err := config.Path()
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}
}

func TestPathWithoutHomeIsAConfigError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	_, err := config.Path()
	assert.Assert(t, err != nil, "expected an error with no HOME set")
	assert.Equal(t, exitcode.Of(err), exitcode.Config)
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	assert.NilError(t, err)
	assert.Equal(t, cfg.Token, "")
}

// TestSaveLoadRoundTripsAwkwardToken guards against building the config with
// string concatenation: a token containing a colon would produce YAML that no
// longer parsed.
func TestSaveLoadRoundTripsAwkwardToken(t *testing.T) {
	for _, token := range []string{
		"plain0123456789abcdef",
		"has:a:colon #hash {braces} *anchor \"quotes\" and a\nnewline",
	} {
		t.Run(token, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			want := config.Config{Token: token, Org: config.Org{ID: "org1", Name: "Example Org"}}

			assert.NilError(t, config.Save(path, want))
			got, err := config.Load(path)
			assert.NilError(t, err)
			assert.Equal(t, got.Token, token)
			assert.Equal(t, got.Org.ID, "org1")
		})
	}
}

// TestSavePreservesUnrelatedKeys: a writer that rebuilt the file from the keys it
// knew about would silently drop everything else. Saving a loaded config must
// keep every field.
func TestSavePreservesUnrelatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := config.Config{
		Token: "tok",
		Org:   config.Org{ID: "org1", Name: "Example Org", Slug: "example-org"},
		Host:  "https://api.bugsnag.smartbear.com",
		Repos: map[string]config.Repo{
			"github.com/example-org/example-api": {ProjectID: "p1", ProjectName: "example-api", OrgID: "org1"},
			"github.com/example-org/queue":       {ProjectID: "p2", ProjectName: "queue", OrgID: "org1"},
		},
	}

	assert.NilError(t, config.Save(path, original))
	loaded, err := config.Load(path)
	assert.NilError(t, err)
	assert.NilError(t, config.Save(path, loaded))
	again, err := config.Load(path)
	assert.NilError(t, err)

	assert.Equal(t, len(again.Repos), 2)
	assert.Equal(t, again.Host, original.Host)
}

func TestSaveIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	assert.NilError(t, config.Save(path, config.Config{Token: "secret"}))

	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
}

// TestSaveLeavesNoTemporaryFile: the write goes to a temporary file and is
// renamed over the target, so a crash mid-write cannot truncate a stored token.
// This covers the visible half of that — no debris, and the newer value wins.
func TestSaveLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	assert.NilError(t, config.Save(path, config.Config{Token: "first"}))
	assert.NilError(t, config.Save(path, config.Config{Token: "second"}))

	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	for _, e := range entries {
		assert.Assert(t, !strings.HasPrefix(e.Name(), ".config.yaml."),
			"temporary file %q was left behind", e.Name())
	}

	cfg, _ := config.Load(path)
	assert.Equal(t, cfg.Token, "second")
}

// TestConcurrentUpdatesKeepEveryEntry is the reason Update holds a lock. Running
// the CLI in several repositories at once is a normal thing for an agent to do,
// and without the lock the last writer wins and the other entries vanish.
func TestConcurrentUpdatesKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const n = 8

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			errs[i] = config.Update(path, func(c *config.Config) error {
				if c.Repos == nil {
					c.Repos = map[string]config.Repo{}
				}
				c.Repos[fmt.Sprintf("github.com/example-org/repo%d", i)] = config.Repo{
					ProjectID: fmt.Sprintf("p%d", i),
					OrgID:     "org1",
				}
				return nil
			})
		})
	}
	wg.Wait()

	for i, err := range errs {
		assert.NilError(t, err, "Update %d", i)
	}

	cfg, err := config.Load(path)
	assert.NilError(t, err)
	assert.Equal(t, len(cfg.Repos), n)
}

func TestUpdateMutateErrorLeavesFileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	assert.NilError(t, config.Save(path, config.Config{Token: "keep"}))

	wantErr := fmt.Errorf("no")
	err := config.Update(path, func(c *config.Config) error {
		c.Token = "clobbered"
		return wantErr
	})
	assert.Equal(t, err, wantErr)

	cfg, _ := config.Load(path)
	assert.Equal(t, cfg.Token, "keep")
}

func TestCheckFlagsOpenPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	assert.NilError(t, config.Save(path, config.Config{Token: "secret"}))
	assert.NilError(t, config.Check(path))

	assert.NilError(t, os.Chmod(path, 0o644))
	err := config.Check(path)
	assert.Assert(t, err != nil, "expected an error for a world-readable config")
	assert.Equal(t, exitcode.Of(err), exitcode.Config)
}

func TestCheckMissingFileIsFine(t *testing.T) {
	assert.Check(t, is.Nil(config.Check(filepath.Join(t.TempDir(), "nope.yaml"))))
}
