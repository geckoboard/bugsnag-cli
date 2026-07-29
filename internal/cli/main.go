// Package cli assembles the command tree and is the entry point main.go calls.
//
// It is library code, not package main, so tests import it and drive the real
// command surface through Main rather than exercising commands in isolation.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

// IO is everything the command tree gets from outside the process. Nothing in
// this package reads os.Args or the standard streams directly, and nothing is
// read from a global: there is no viper, no package-level cobra command and no
// swapping of os.Stdout.
type IO struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Version string

	// Now, Git and Sleep are seams for tests, so the harness can drive Main
	// itself — and therefore the real exit-code mapping — with a fixed clock,
	// without shelling out to git, and without waiting out retry backoff. All
	// default to the real thing.
	Now   func() time.Time
	Git   GitRunner
	Sleep func(time.Duration)
}

// Main runs the CLI and returns the process exit code. It is the only place
// that turns an error into a code, so no command can bypass the mapping.
func Main(ctx context.Context, stdio IO) int {
	err := run(ctx, stdio)
	if err == nil {
		return exitcode.OK
	}

	code := exitcode.Of(err)
	reportError(stdio.Stderr, err, code)
	return code
}

func run(ctx context.Context, stdio IO) error {
	root := newRootCmd(stdio)
	root.SetArgs(stdio.Args)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return nil
	}

	// cobra reports flag and argument problems as plain errors. Those are usage
	// problems, and classifying them here keeps the rule that a Kind is assigned
	// where the error originates rather than guessed from its text later.
	var typed *apierr.Error
	if apierr.KindOf(err) == apierr.KindInternal && !errors.As(err, &typed) {
		return apierr.Wrap(apierr.KindUsage, err, "%s", err.Error())
	}
	return err
}

// reportError writes the failure to stderr, never stdout: an error on stdout
// would mix into the data anything piping `--json` into a parser was reading.
//
// The first line carries the machine-readable fields. kind and exit_code let a
// caller branch without parsing prose, and retryable saves it from having to
// know that 7..9 is the retryable band.
func reportError(w io.Writer, err error, code int) {
	if w == nil {
		return
	}

	fmt.Fprintf(w, "bugsnag: %s (kind=%s exit_code=%d retryable=%t)\n",
		err.Error(), apierr.KindOf(err), code, exitcode.Retryable(code))

	var e *apierr.Error
	if errors.As(err, &e) && e.Hint != "" {
		fmt.Fprintf(w, "hint: %s\n", e.Hint)
	}
}
