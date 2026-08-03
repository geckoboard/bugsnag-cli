package exitcode_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
	"gotest.tools/v3/assert"
)

// TestEveryKindHasItsCode pins every code. These are the CLI's contract with
// scripts and agents, so a change here is a breaking change and should be a
// deliberate edit to this table.
func TestEveryKindHasItsCode(t *testing.T) {
	for _, tc := range []struct {
		kind apierr.Kind
		name string
		code int
	}{
		{apierr.KindInternal, "internal", 1},
		{apierr.KindUsage, "usage", 2},
		{apierr.KindConfig, "config", 3},
		{apierr.KindAuth, "auth", 4},
		{apierr.KindNotFound, "not_found", 5},
		{apierr.KindBadRequest, "bad_request", 6},
		{apierr.KindRateLimited, "rate_limited", 7},
		{apierr.KindServer, "server", 8},
		{apierr.KindNetwork, "network", 9},
		{apierr.KindCanceled, "canceled", 10},
		{apierr.KindUntrustedHost, "untrusted_host", 11},
		{apierr.KindDecode, "decode", 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, exitcode.Of(apierr.New(tc.kind, "failed")), tc.code)
			assert.Equal(t, tc.kind.String(), tc.name)
		})
	}
}

func TestOfNilIsOK(t *testing.T) {
	assert.Equal(t, exitcode.Of(nil), exitcode.OK)
}

// TestRetryableBand is the test behind the documented rule `7 <= code <= 9
// means retry`. Rate limited, server and network are retryable; nothing else
// is, because nothing else will behave differently on a second attempt.
func TestRetryableBand(t *testing.T) {
	want := map[int]bool{
		exitcode.OK:            false,
		exitcode.Internal:      false,
		exitcode.Usage:         false,
		exitcode.Config:        false,
		exitcode.Auth:          false,
		exitcode.NotFound:      false,
		exitcode.BadRequest:    false,
		exitcode.RateLimited:   true,
		exitcode.Server:        true,
		exitcode.Network:       true,
		exitcode.Canceled:      false,
		exitcode.UntrustedHost: false,
		exitcode.Decode:        false,
	}

	for code, retryable := range want {
		assert.Equal(t, exitcode.Retryable(code), retryable)
	}

	// The band must stay contiguous, since that is what the documented rule
	// relies on.
	for code := exitcode.RetryableMin; code <= exitcode.RetryableMax; code++ {
		assert.Assert(t, exitcode.Retryable(code), "Retryable(%d) = false, breaking the contiguous 7..9 band", code)
	}
}

// TestOfClassifiesFromStatusNotMessage: exit codes come from the HTTP status, not
// from strings.Contains on the human-readable message. Identical message text
// with different statuses must produce different codes.
func TestOfClassifiesFromStatusNotMessage(t *testing.T) {
	const sameMessage = `{"errors":["project_id is required"]}`

	for _, tc := range []struct {
		status int
		want   int
	}{
		{400, exitcode.BadRequest},
		{401, exitcode.Auth},
		{403, exitcode.Auth},
		{404, exitcode.NotFound},
		{422, exitcode.BadRequest},
		{429, exitcode.RateLimited},
		{500, exitcode.Server},
		{503, exitcode.Server},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			err := apierr.FromStatus("list errors", tc.status, sameMessage)
			assert.Equal(t, exitcode.Of(err), tc.want)
		})
	}
}

// TestOfWrappedError checks the Kind survives wrapping, since errors are
// wrapped with context as they travel up to main.
func TestOfWrappedError(t *testing.T) {
	base := apierr.New(apierr.KindUntrustedHost, "refusing to follow Link to evil.example")
	wrapped := fmt.Errorf("paginating: %w", fmt.Errorf("page 2: %w", base))

	assert.Equal(t, exitcode.Of(wrapped), exitcode.UntrustedHost)
}

// TestOfUnclassifiedIsInternal: an error that never got a Kind is a bug in this
// tool, so it must report as internal rather than defaulting to something that
// looks like an expected failure.
func TestOfUnclassifiedIsInternal(t *testing.T) {
	assert.Equal(t, exitcode.Of(errors.New("boom")), exitcode.Internal)
}

// TestOfContextErrors: Ctrl-C must be exit 10 and must not be retryable, even
// when the context error arrives unwrapped.
func TestOfContextErrors(t *testing.T) {
	assert.Equal(t, exitcode.Of(context.Canceled), exitcode.Canceled)
	assert.Assert(t, !exitcode.Retryable(exitcode.Canceled), "canceled must not be retryable: the user asked us to stop")

	// A deadline is a timeout, which is worth retrying.
	assert.Equal(t, exitcode.Of(apierr.FromNetwork("list errors", context.DeadlineExceeded)), exitcode.Network)
}
