// Command bugsnag is a CLI for the Bugsnag Data Access API.
//
// It lives under cmd/bugsnag rather than at the module root so that
// `go install .../cmd/bugsnag@latest` installs a binary called `bugsnag`: go
// names an installed binary after the last element of the package path, which at
// the root would be the module name, bugsnag-cli.
//
// This file holds the only os.Exit in the repo: every other layer returns an
// error, so nothing can bypass the exit-code mapping or skip a deferred flush.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/geckoboard/bugsnag-cli/internal/cli"
)

func main() {
	os.Exit(run())
}

// run returns the exit code rather than calling os.Exit itself, so that the
// deferred teardown below is not skipped.
func run() int {
	// Signal handling lives here rather than in internal/cli, so that package
	// has no process-global state.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Main(ctx, cli.IO{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version(),
	})
}

// version is read from the build info rather than stamped in with -ldflags, so a
// plain `go install` or `go build` reports something useful with no build
// machinery. A versioned install (`...@v1.2.3`) reports that version; a build
// from a checkout reports the vcs revision, suffixed "-dirty" for a modified
// tree.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	var revision, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		return revision + modified
	}
	return "dev"
}
