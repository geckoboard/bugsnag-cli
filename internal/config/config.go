// Package config is the on-disk state: the API token, the active organization,
// and the project cached for each git repository.
//
// The file is written by marshalling a struct, never by concatenating strings.
// Building it with `content += fmt.Sprintf("api_token: %s\n", token)` produces
// invalid YAML for any token containing a colon, and silently discards every key
// the writer does not know about.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
)

// Config is the whole config file.
type Config struct {
	// Token is the Data Access API personal access token.
	Token string `yaml:"token,omitempty"`

	// Org is the active organization. No endpoint lists projects without an
	// organization id, so this is resolved once at login and cached.
	Org Org `yaml:"org,omitempty"`

	// Host is the API base URL, for organizations on
	// app.bugsnag.smartbear.com. Empty means the default.
	Host string `yaml:"host,omitempty"`

	// Repos maps a canonical repository identity to the project it resolved to.
	// Keeping this centrally means nothing is written into your repositories.
	Repos map[string]Repo `yaml:"repos,omitempty"`
}

// Org is the active organization.
type Org struct {
	ID   string `yaml:"id,omitempty"`
	Name string `yaml:"name,omitempty"`
	Slug string `yaml:"slug,omitempty"`
}

// Repo is a cached project resolution for one repository.
type Repo struct {
	ProjectID   string `yaml:"project_id"`
	ProjectName string `yaml:"project_name,omitempty"`
	ProjectSlug string `yaml:"project_slug,omitempty"`

	// HTMLURL is the project's dashboard URL. Errors and events carry no
	// dashboard link of their own — only api.bugsnag.com URLs — so this is
	// cached here and the error and event links are composed from it.
	HTMLURL string `yaml:"html_url,omitempty"`

	// OrgID records which organization this resolution belongs to. The cache is
	// only trusted while it matches the active organization, so switching
	// organizations cannot silently return another org's project.
	OrgID string `yaml:"org_id,omitempty"`

	// ResolvedAt is when this entry was written, for `bugsnag project show`.
	ResolvedAt time.Time `yaml:"resolved_at,omitempty"`
}

// Path returns the config file path, honouring XDG_CONFIG_HOME then HOME.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", apierr.New(apierr.KindConfig,
				"cannot locate a config directory: neither XDG_CONFIG_HOME nor HOME is set")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "bugsnag", "config.yaml"), nil
}

// Load reads the config file. A missing file is not an error: it is the state
// before `bugsnag auth login`.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, apierr.Wrap(apierr.KindConfig, err, "cannot read %s", path)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, apierr.Wrap(apierr.KindConfig, err, "cannot parse %s", path)
	}
	return cfg, nil
}

// Save writes the config atomically.
//
// The file is written to a temporary name in the same directory and renamed over
// the target, so a crash or a full disk leaves the previous config intact rather
// than a half-written file holding a truncated token.
func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot create %s", dir)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return apierr.Wrap(apierr.KindInternal, err, "cannot serialise config")
	}

	tmp, err := os.CreateTemp(dir, ".config.yaml.*")
	if err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot create a temporary file in %s", dir)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// No Chmod: os.CreateTemp opens with 0600 already, which is what this file
	// wants, so the token is never briefly world-readable.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return apierr.Wrap(apierr.KindConfig, err, "cannot write %s", tmpName)
	}
	if err := tmp.Close(); err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot close %s", tmpName)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot replace %s", path)
	}
	return nil
}

// Update is the only way to change the config.
//
// It takes an exclusive lock for the whole read-modify-write, because the CLI is
// expected to be run concurrently — an agent working in three repositories at
// once would otherwise have two of the three `repos` entries silently lost when
// the last writer overwrote what it read before the others saved.
func Update(path string, mutate func(*Config) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot create %s", dir)
	}

	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot lock %s", path)
	}
	defer lock.Unlock()

	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if err := mutate(&cfg); err != nil {
		return err
	}
	return Save(path, cfg)
}

// fileMode is owner read/write only: the file holds an API token.
const fileMode fs.FileMode = 0o600

// Check reports whether the config file's permissions are too open, so a token
// left world-readable is surfaced rather than ignored.
func Check(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return apierr.Wrap(apierr.KindConfig, err, "cannot stat %s", path)
	}

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return apierr.New(apierr.KindConfig,
			"%s is %s; it holds an API token and should be %s",
			path, fmt.Sprintf("%#o", perm), fmt.Sprintf("%#o", fileMode))
	}
	return nil
}
