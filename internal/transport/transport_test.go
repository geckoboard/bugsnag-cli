package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
	"github.com/geckoboard/bugsnag-cli/internal/transport"
	"gotest.tools/v3/assert"
)

const testToken = "secret-token-do-not-leak"

// newClient builds a Client allowed to talk to srv and nothing else.
func newClient(t *testing.T, srv *httptest.Server, opts ...func(*transport.Options)) *transport.Client {
	t.Helper()

	o := transport.Options{
		Token:     testToken,
		Hosts:     []string{srv.URL},
		UserAgent: "bugsnag-cli/test",
		Sleep:     func(time.Duration) {},
	}
	for _, f := range opts {
		f(&o)
	}

	c, err := transport.New(o)
	assert.NilError(t, err)
	return c
}

func get(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	assert.NilError(t, err)
	return req
}

func TestSetsAuthAndHeaders(t *testing.T) {
	var gotAuth, gotAgent, gotAccept, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := newClient(t, srv).Do(get(t, srv.URL+"/user/organizations"))
	assert.NilError(t, err)
	resp.Body.Close()

	assert.Equal(t, gotAuth, "token "+testToken)
	assert.Equal(t, gotAgent, "bugsnag-cli/test")
	assert.Equal(t, gotAccept, "application/json")
	assert.Equal(t, gotVersion, "2")
}

// TestTokenNeverReachesADisallowedHost is the token-exfiltration test.
//
// A second server stands in for whatever host a Link header might name. The
// request must fail, and that server must record no request at all: it is not
// enough for the token to be absent from a request that was still sent.
func TestTokenNeverReachesADisallowedHost(t *testing.T) {
	var attackerHits atomic.Int64
	var sawToken atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), testToken) {
			sawToken.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer allowed.Close()

	// The client is allowed to talk to `allowed` only.
	c := newClient(t, allowed)

	req := get(t, attacker.URL+"/steal")
	_, err := c.Do(req)
	assert.Assert(t, err != nil, "expected the request to be refused")

	assert.Equal(t, exitcode.Of(err), exitcode.UntrustedHost)

	assert.Equal(t, attackerHits.Load(), int64(0))
	assert.Assert(t, !sawToken.Load(), "the token reached a disallowed host")

	// The Authorization header must never have been written onto the request,
	// even though it was never sent.
	assert.Equal(t, req.Header.Get("Authorization"), "")
}

// TestRefusesPlaintextToARemoteHost: the token must not go out over http, but
// loopback stays usable so tests can point at a local server.
func TestRefusesPlaintextToARemoteHost(t *testing.T) {
	c, err := transport.New(transport.Options{
		Token: testToken,
		Hosts: []string{"api.bugsnag.com", "evil.example"},
		Sleep: func(time.Duration) {},
	})
	assert.NilError(t, err)

	// Allowlisted, but plaintext.
	_, err = c.Do(get(t, "http://evil.example/steal"))
	assert.Assert(t, err != nil, "expected plaintext to a remote host to be refused")
	assert.Equal(t, exitcode.Of(err), exitcode.UntrustedHost)
}

func TestNewRequiresATokenAndAHost(t *testing.T) {
	_, err := transport.New(transport.Options{Hosts: []string{"api.bugsnag.com"}})
	assert.Assert(t, err != nil, "expected an error with no token")
	assert.Equal(t, exitcode.Of(err), exitcode.Auth)

	_, err = transport.New(transport.Options{Token: testToken})
	assert.Assert(t, err != nil, "expected an error with no allowed hosts: a token must not be sendable anywhere")
}

func TestRetriesRateLimitAndServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"rate limited", http.StatusTooManyRequests},
		{"server error", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if hits.Add(1) < 3 {
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			resp, err := newClient(t, srv).Do(get(t, srv.URL+"/errors"))
			assert.NilError(t, err)
			resp.Body.Close()

			assert.Equal(t, resp.StatusCode, http.StatusOK)
			assert.Equal(t, hits.Load(), int64(3))
		})
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(status)
		}))

		resp, err := newClient(t, srv).Do(get(t, srv.URL+"/errors"))
		assert.NilError(t, err)
		resp.Body.Close()
		srv.Close()

		assert.Assert(t, hits.Load() == 1, "status %d was retried %d times; it will fail the same way again", status, hits.Load()-1)
	}
}

// TestNeverRetriesPOST: a retried comment would post twice.
func TestNeverRetriesPOST(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/errors/1/comments", strings.NewReader(`{"message":"hi"}`))
	assert.NilError(t, err)

	resp, err := newClient(t, srv).Do(req)
	assert.NilError(t, err)
	resp.Body.Close()

	assert.Assert(t, hits.Load() == 1, "POST was attempted %d times; retrying it could duplicate a comment", hits.Load())
}

// TestHonoursRetryAfter: the server knows its own limit better than our backoff
// curve does.
func TestHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var slept []time.Duration
	c := newClient(t, srv, func(o *transport.Options) {
		o.Sleep = func(d time.Duration) { slept = append(slept, d) }
	})

	resp, err := c.Do(get(t, srv.URL+"/errors"))
	assert.NilError(t, err)
	resp.Body.Close()

	assert.DeepEqual(t, slept, []time.Duration{2 * time.Second})
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := newClient(t, srv).Do(get(t, srv.URL+"/errors"))
	assert.NilError(t, err)
	resp.Body.Close()

	// The last attempt's response is returned rather than an error, so a caller
	// sees the API's own status.
	assert.Equal(t, resp.StatusCode, http.StatusServiceUnavailable)
	assert.Equal(t, hits.Load(), int64(3))
}

// TestContextCancellationIsImmediate: Ctrl-C during a backoff must not wait out
// the delay, and must report as cancelled rather than as a network failure.
func TestContextCancellationIsImmediate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	c := newClient(t, srv, func(o *transport.Options) {
		// A wait long enough that the test would visibly hang if cancellation
		// were not honoured.
		o.Sleep = func(time.Duration) { time.Sleep(30 * time.Second) }
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/errors", nil)
	assert.NilError(t, err)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		resp, err := c.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		assert.Assert(t, err != nil, "expected a cancellation error")
		assert.Equal(t, exitcode.Of(err), exitcode.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt the backoff wait")
	}
}

func TestNetworkFailureIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	c := newClient(t, srv)
	srv.Close() // nothing is listening now

	_, err := c.Do(get(t, url+"/errors"))
	assert.Assert(t, err != nil, "expected a network error")
	assert.Equal(t, exitcode.Of(err), exitcode.Network)
	assert.Assert(t, exitcode.Retryable(exitcode.Of(err)), "a network failure should be reported as retryable")
}

func TestAllowlistAcceptsHostFormsAndIsCaseInsensitive(t *testing.T) {
	for _, host := range []string{
		"api.bugsnag.com",
		"https://api.bugsnag.com",
		"https://api.bugsnag.com/",
		"API.BUGSNAG.COM",
	} {
		c, err := transport.New(transport.Options{
			Token: testToken,
			Hosts: []string{host},
			Sleep: func(time.Duration) {},
		})
		assert.NilError(t, err)

		// A different host must still be refused.
		_, err = c.Do(get(t, "https://evil.example/x"))
		assert.Equal(t, exitcode.Of(err), exitcode.UntrustedHost,
			"New(%q) allowed evil.example", host)
	}
}
