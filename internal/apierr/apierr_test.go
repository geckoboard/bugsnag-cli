package apierr_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

// TestFromStatusUsesAPIErrorText: the API's own explanation should reach the
// user, since it is more specific than anything we could invent.
func TestFromStatusUsesAPIErrorText(t *testing.T) {

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "documented errors array",
			body: `{"errors":["Bad request"]}`,
			want: "list errors: Bad request",
		},
		{
			name: "multiple errors are joined",
			body: `{"errors":["sort is invalid","direction is invalid"]}`,
			want: "list errors: sort is invalid; direction is invalid",
		},
		{
			name: "single error string",
			body: `{"error":"not found"}`,
			want: "list errors: not found",
		},
		{
			name: "message key",
			body: `{"message":"nope"}`,
			want: "list errors: nope",
		},
		{
			name: "falls back to status when body is not JSON",
			body: "<html>502 Bad Gateway</html>",
			want: "list errors: HTTP 400",
		},
		{
			name: "falls back to status when body is empty",
			body: "",
			want: "list errors: HTTP 400",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := apierr.FromStatus("list errors", 400, tc.body)
			assert.Equal(t, err.Error(), tc.want)
		})
	}
}

// TestFromStatusIgnoresBodyForKind: the body must never influence the Kind. A
// 500 whose body happens to say "not found" is still a server error.
func TestFromStatusIgnoresBodyForKind(t *testing.T) {

	err := apierr.FromStatus("view error", 500, `{"errors":["not found","unauthorized","is required"]}`)
	assert.Equal(t, err.Kind, apierr.KindServer)
}

// TestFromStatusRateLimitHint: a 429 is the one failure where the user needs to
// know the actual limit, which the spec does not document but is real.
func TestFromStatusRateLimitHint(t *testing.T) {

	err := apierr.FromStatus("list errors", 429, "")
	assert.Equal(t, err.Kind, apierr.KindRateLimited)
	assert.Check(t, is.Contains(err.Hint, "30 requests per minute"))
}

func TestFromNetworkClassification(t *testing.T) {

	for _, tc := range []struct {
		name string
		err  error
		want apierr.Kind
	}{
		{"canceled", context.Canceled, apierr.KindCanceled},
		{"deadline", context.DeadlineExceeded, apierr.KindNetwork},
		{
			name: "url error wrapping cancellation",
			err:  &url.Error{Op: "Get", URL: "https://api.bugsnag.com", Err: context.Canceled},
			want: apierr.KindCanceled,
		},
		{
			name: "url error wrapping a deadline",
			err:  &url.Error{Op: "Get", URL: "https://api.bugsnag.com", Err: context.DeadlineExceeded},
			want: apierr.KindNetwork,
		},
		{
			name: "dns failure",
			err:  &net.DNSError{Name: "api.bugsnag.invalid", Err: "no such host"},
			want: apierr.KindNetwork,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
			want: apierr.KindNetwork,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := apierr.FromNetwork("list errors", tc.err)
			assert.Equal(t, got.Kind, tc.want)
		})
	}
}

// TestFromNetworkNamesTheHostOnDNSFailure: "could not resolve X" is actionable;
// Go's raw DNSError text is not.
func TestFromNetworkNamesTheHostOnDNSFailure(t *testing.T) {

	err := apierr.FromNetwork("list errors", &net.DNSError{Name: "api.bugsnag.invalid", Err: "no such host"})
	assert.Check(t, is.Contains(err.Error(), "could not resolve api.bugsnag.invalid"))
}

func TestUnwrap(t *testing.T) {

	cause := errors.New("underlying")
	err := apierr.Wrap(apierr.KindConfig, cause, "could not read config")

	assert.Assert(t, errors.Is(err, cause), "errors.Is could not find the cause")
	assert.Equal(t, err.Error(), "could not read config")
}

// TestKindOfPrefersTheInnermostClassification: wrapping adds context but must
// not change the Kind.
func TestKindOfPrefersTheInnermostClassification(t *testing.T) {

	inner := apierr.New(apierr.KindDecode, "could not decode item 3")
	outer := fmt.Errorf("rendering events list: %w", inner)

	assert.Equal(t, apierr.KindOf(outer), apierr.KindDecode)
}

// TestErrorBodyIsCapped stops a large HTML error page from a proxy becoming the
// message.
func TestErrorBodyIsCapped(t *testing.T) {

	huge := `{"errors":["` + strings.Repeat("x", 64<<10) + `"]}`
	err := apierr.FromStatus("list errors", 400, huge)

	assert.Assert(t, len(err.Error()) <= 16<<10, "message is %d bytes; an oversized body should not be used verbatim", len(err.Error()))
}
