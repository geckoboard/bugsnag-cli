package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/geckoboard/bugsnag-cli/internal/cli"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
)

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		code := cli.Main(context.Background(), cli.IO{
			Args:    args,
			Stdout:  &stdout,
			Stderr:  &stderr,
			Version: "1.2.3",
		})

		assert.Check(t, is.Equal(code, exitcode.OK), "args=%v", args)
		got := strings.TrimSpace(stdout.String())
		assert.Check(t, is.Equal(got, "1.2.3"), "args=%v", args)
		assert.Check(t, is.Equal(stderr.Len(), 0), "args=%v, stderr=%q", args, stderr.String())
	}
}

// TestErrorsGoToStderrOnly: errors go to stderr, never stdout, so they cannot
// corrupt a pipeline reading the JSON.
func TestErrorsGoToStderrOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Main(context.Background(), cli.IO{
		Args:    []string{"nonsense"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Version: "1.2.3",
	})

	assert.Check(t, is.Equal(code, exitcode.Usage))
	assert.Check(t, is.Equal(stdout.Len(), 0), "errors must never reach stdout, stdout=%q", stdout.String())

	// The first stderr line must carry the machine-readable fields, so a caller
	// can branch without parsing prose.
	line := strings.SplitN(stderr.String(), "\n", 2)[0]
	for _, want := range []string{"kind=usage", "exit_code=2", "retryable=false"} {
		assert.Check(t, is.Contains(line, want))
	}
}
