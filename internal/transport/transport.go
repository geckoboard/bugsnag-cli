// Package transport performs HTTP requests against the Data Access API.
//
// Its job beyond plumbing is to make sure the API token can only ever be sent to
// a host we trust. The token is attached inside Do, after the host has been
// checked against an allowlist, so there is no code path where a request to an
// unexpected host has already had the Authorization header written onto it. That
// closes one half of the token-exfiltration hole in --all-pages, which followed
// the Link header to whatever host it named; internal/bugsnagio closes the other
// half with a same-origin check before the request is built at all.
package transport

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
)

// Client is the HTTP client used against the real API.
type Client struct {
	base      *http.Client
	token     string
	userAgent string

	// allowed is the set of hosts the token may be sent to.
	allowed map[string]struct{}

	// sleep is injectable so retry tests do not actually wait.
	sleep func(time.Duration)
}

// Options configures a Client.
type Options struct {
	// Token is the personal auth token.
	Token string

	// Hosts are the hosts the token may be sent to. Any request to a host not
	// listed fails before a header is set. Values may be a bare host, a
	// host:port, or a full URL, whose host is used.
	Hosts []string

	// UserAgent identifies the CLI and its version.
	UserAgent string

	// Sleep is used between retries; time.Sleep when nil.
	Sleep func(time.Duration)
}

// New builds a Client.
func New(opts Options) (*Client, error) {
	if opts.Token == "" {
		return nil, apierr.New(apierr.KindAuth,
			"no API token configured; run: bugsnag auth login")
	}

	allowed := make(map[string]struct{}, len(opts.Hosts))
	for _, h := range opts.Hosts {
		host, err := canonicalHost(h)
		if err != nil {
			return nil, err
		}
		allowed[host] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, apierr.New(apierr.KindInternal,
			"no allowed hosts configured; refusing to send a token anywhere")
	}

	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	return &Client{
		base:      &http.Client{Timeout: defaultTimeout},
		token:     opts.Token,
		userAgent: opts.UserAgent,
		allowed:   allowed,
		sleep:     sleep,
	}, nil
}

// Do sends a request, retrying the failures that are worth retrying.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// The allowlist check comes first, before any header is set. If this
	// returns, no part of the token has been written onto the request.
	if err := c.checkHost(req.URL); err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	// The API versions its responses by header; pinning it means a new default
	// cannot change our parsing underneath us.
	req.Header.Set("X-Version", apiVersion)

	// Only a GET is safe to repeat. A retried POST could add a second comment,
	// so a request with a body is attempted exactly once.
	attempts := defaultMaxAttempts
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, apierr.FromNetwork(operation(req), err)
		}

		resp, err := c.base.Do(req)
		if err != nil {
			lastErr = apierr.FromNetwork(operation(req), err)
			// A cancelled context is a deliberate stop, never a retry.
			if attempt == attempts || apierr.KindOf(lastErr) != apierr.KindNetwork {
				return nil, lastErr
			}
			if werr := c.wait(req, backoff(attempt, 0)); werr != nil {
				return nil, werr
			}
			continue
		}

		if attempt < attempts && retryableStatus(resp.StatusCode) {
			delay := backoff(attempt, retryAfter(resp))
			// The body has to be drained and closed or the connection cannot be
			// reused for the retry.
			drain(resp)
			if werr := c.wait(req, delay); werr != nil {
				return nil, werr
			}
			continue
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = apierr.New(apierr.KindInternal, "request loop ended without a result")
	}
	return nil, lastErr
}

// checkHost fails unless the request targets an allowed host.
func (c *Client) checkHost(u *url.URL) error {
	if u == nil {
		return apierr.New(apierr.KindInternal, "request has no URL")
	}

	// Only HTTPS, except for loopback, which is how tests point the client at a
	// local server without weakening the real rule.
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return apierr.New(apierr.KindUntrustedHost,
			"refusing to send the API token over %s to %s", u.Scheme, u.Host)
	}

	if _, ok := c.allowed[strings.ToLower(u.Host)]; ok {
		return nil
	}
	return apierr.New(apierr.KindUntrustedHost,
		"refusing to send the API token to %s; allowed hosts are %s",
		u.Host, strings.Join(c.allowedHosts(), ", "))
}

func (c *Client) allowedHosts() []string {
	hosts := make([]string, 0, len(c.allowed))
	for h := range c.allowed {
		hosts = append(hosts, h)
	}
	return hosts
}

// wait sleeps for d unless the context is cancelled first, so Ctrl-C during a
// backoff is immediate rather than waiting out the delay.
func (c *Client) wait(req *http.Request, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		c.sleep(d)
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-req.Context().Done():
		return apierr.FromNetwork(operation(req), req.Context().Err())
	}
}

const (
	// defaultTimeout bounds a single attempt. Event payloads can be large, so
	// this is generous.
	defaultTimeout = 60 * time.Second

	// defaultMaxAttempts is the first try plus two retries.
	defaultMaxAttempts = 3

	// apiVersion pins the response shape the vendored spec describes.
	apiVersion = "2"

	// maxBackoff caps a single wait. The API allows 30 requests per minute, so
	// waiting a couple of seconds is proportionate and waiting a minute is not.
	maxBackoff = 8 * time.Second
)

// retryableStatus reports whether a status is worth retrying. These are exactly
// the statuses behind exit codes 7 and 8.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// backoff returns how long to wait before the next attempt, preferring the
// server's own Retry-After when it sent one.
func backoff(attempt int, serverHint time.Duration) time.Duration {
	if serverHint > 0 {
		return min(serverHint, maxBackoff)
	}
	d := time.Duration(math.Pow(2, float64(attempt-1))) * 250 * time.Millisecond
	return min(d, maxBackoff)
}

// retryAfter reads the Retry-After header, which may be seconds or an HTTP date.
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// drain empties and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	if resp.Body == nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// canonicalHost reduces a host, host:port or URL to the host:port form used as
// the allowlist key.
func canonicalHost(s string) (string, error) {
	if s == "" {
		return "", apierr.New(apierr.KindInternal, "empty allowed host")
	}

	raw := s
	if !strings.Contains(s, "//") {
		raw = "https://" + s
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", apierr.Wrap(apierr.KindConfig, err, "cannot parse host %q", s)
	}
	if u.Host == "" {
		return "", apierr.New(apierr.KindConfig, "cannot parse host %q", s)
	}
	return strings.ToLower(u.Host), nil
}

func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

func operation(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "request"
	}
	return fmt.Sprintf("%s %s", req.Method, req.URL.Path)
}
