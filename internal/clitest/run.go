package clitest

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/cli"
	"github.com/geckoboard/bugsnag-cli/internal/config"
)

// Now is the fixed reference time every test renders against, so relative
// timestamps are deterministic without a fake clock.
var Now = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// Result is the outcome of one CLI run.
type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// Harness drives the real command tree.
type Harness struct {
	t *testing.T

	// Server is the fake API. Nil means no API is available, which is the right
	// setup for testing commands that should not call one.
	Server *Server

	// ConfigPath is the config file this run reads and writes.
	ConfigPath string

	// Stdin feeds prompts.
	Stdin string

	// Remotes is what the fake git returns.
	Remotes map[string]string
}

// New returns a harness with a fake API, a signed-in config and a git remote
// that resolves to the example-api fixture project.
//
// It points the CLI at a throwaway config by setting HOME to a temp directory,
// which is why the harness cannot run in parallel: t.Setenv forbids it. The host
// is carried in the config file itself, since the allowlist is built from it, so
// the token stays confined to the fake server.
func New(t *testing.T) *Harness {
	t.Helper()

	srv := NewServer(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgPath := filepath.Join(home, ".config", "bugsnag", "config.yaml")

	if err := config.Save(cfgPath, config.Config{
		Token: srv.Token,
		Org:   config.Org{ID: "org1", Name: "Example Org", Slug: "example-org"},
		Host:  srv.URL,
	}); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	return &Harness{
		t:          t,
		Server:     srv,
		ConfigPath: cfgPath,
		Remotes:    map[string]string{"origin": "git@github.com:example-org/example-api.git"},
	}
}

// NewSignedOut returns a harness with no token, for first-run behaviour.
func NewSignedOut(t *testing.T) *Harness {
	t.Helper()

	h := New(t)
	if err := config.Save(h.ConfigPath, config.Config{Host: h.Server.URL}); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return h
}

// Run executes the CLI with args and returns what it wrote.
func (h *Harness) Run(args ...string) Result {
	h.t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Main(context.Background(), cli.IO{
		Args:    args,
		Stdin:   strings.NewReader(h.Stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Version: "test",
		Now:     func() time.Time { return Now },
		Git:     fakeGit{remotes: h.Remotes},
		// A no-op sleep so retry tests exercise the backoff logic without
		// waiting out the real delays.
		Sleep: func(time.Duration) {},
	})

	return Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// fakeGit returns fixed remotes, so repository detection is exercised without a
// real checkout and without shelling out.
type fakeGit struct {
	remotes map[string]string
}

func (g fakeGit) Remotes(context.Context, string) (map[string]string, error) {
	return g.remotes, nil
}

// Config reads the config file back, for asserting what a command persisted.
func (h *Harness) Config() config.Config {
	h.t.Helper()

	cfg, err := config.Load(h.ConfigPath)
	if err != nil {
		h.t.Fatalf("reading config: %v", err)
	}
	return cfg
}
