// Package bugsnagio moves bytes between the API and a sink.
//
// It exists because the CLI has two output paths with different needs. --json
// must keep the API's values intact, so it passes the API's own item bytes
// through: re-marshalling through the generated types is lossy, since metaData is
// map[string]interface{} and so every number round-trips via float64, keys get
// alphabetised, and the closed _EventApp and _EventDevice structs drop whatever
// field a notifier added. The text path needs the generated types, so it decodes.
//
// Both are fed from one pass over the response. Items are teed at the item
// level rather than by buffering the whole body: a json.Decoder over the
// top-level array yields each element's exact wire bytes as a
// json.RawMessage, which keeps memory at the size of the largest single item
// rather than the whole page.
package bugsnagio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/transport"
)

// Client issues requests against one API host.
type Client struct {
	doer *transport.Client
	base *url.URL
}

// NewClient builds a Client for baseURL.
func NewClient(doer *transport.Client, baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, apierr.Wrap(apierr.KindConfig, err, "invalid API host %q", baseURL)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, apierr.New(apierr.KindConfig, "invalid API host %q", baseURL)
	}
	return &Client{doer: doer, base: u}, nil
}

// Request describes one API call.
type Request struct {
	// Op names the operation for error messages, e.g. "list errors".
	Op string

	// Build constructs the request against a server base URL. The generated
	// New*Request builders in internal/bugsnagapi fit here directly, which is
	// what keeps URL and query construction derived from the spec rather than
	// assembled by hand.
	Build BuildFunc

	// ExtraQuery is appended to the built URL's query, in order. Filters go
	// here: the overlay removed the generated filter parameters because their
	// emitted encoder produced the wrong wire format, so internal/filters
	// encodes them and they join the request at this point.
	//
	// It is an ordered list rather than a url.Values because the filter encoding
	// depends on the order the parameters arrive in: a condition's type has to
	// immediately precede its own value.
	ExtraQuery []filters.Param

	// AllPages follows the Link header until there are no more pages.
	AllPages bool

	// Limit bounds how many items are yielded in total. Zero means no limit.
	Limit int
}

// BuildFunc constructs a request for a server base URL.
type BuildFunc func(server string) (*http.Request, error)

// Meta is what a list response says about itself, beyond the items.
type Meta struct {
	// TotalCount is the X-Total-Count header, or -1 when absent. It is present
	// on every list endpoint v1 uses, which is what lets the pagination footer
	// say "30 of 6,112" instead of implying 30 is all there is.
	TotalCount int

	// NextURL is the Link rel="next" URL, or empty on the last page.
	NextURL string
}

// Sink receives items as they arrive.
//
// Item is given the element's exact wire bytes. A sink either writes them
// through, re-indented but with their values intact (--json), or decodes them
// into a generated type (text).
type Sink interface {
	Item(raw json.RawMessage) error
	Close(meta Meta) error
}

// One fetches a single JSON object and hands its bytes to sink.
func (c *Client) One(ctx context.Context, r Request, sink Sink) error {
	first, err := c.StartURL(r)
	if err != nil {
		return err
	}

	resp, err := c.do(ctx, r, first)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return apierr.Wrap(apierr.KindNetwork, err, "%s: reading response", r.Op)
	}

	if err := sink.Item(json.RawMessage(body)); err != nil {
		return err
	}
	return sink.Close(metaFrom(resp))
}

// Stream fetches a JSON array, following pagination when asked, and hands every
// element to sink.
//
// Pagination is aggregated in the sink rather than by concatenating responses,
// because two JSON arrays cannot be joined by appending their bytes.
func (c *Client) Stream(ctx context.Context, r Request, sink Sink) error {
	next, err := c.StartURL(r)
	if err != nil {
		return err
	}

	meta := Meta{TotalCount: -1}
	seen := map[string]bool{}
	pages, items := 0, 0

	for next != "" {
		if err := ctx.Err(); err != nil {
			return apierr.FromNetwork(r.Op, err)
		}

		// A server that returns a Link pointing back at a page we have already
		// fetched would otherwise loop forever.
		if seen[next] {
			return apierr.New(apierr.KindServer,
				"%s: pagination loop: %s was returned twice", r.Op, next)
		}
		seen[next] = true

		pageMeta, added, err := c.streamPage(ctx, r, next, sink, items)
		if err != nil {
			return err
		}
		pages, items = pages+1, items+added
		if meta.TotalCount < 0 {
			meta.TotalCount = pageMeta.TotalCount
		}
		meta.NextURL = pageMeta.NextURL

		if !r.AllPages {
			break
		}
		if r.Limit > 0 && items >= r.Limit {
			break
		}
		if pages >= defaultMaxPages {
			break
		}

		// The Link URL is followed verbatim. Offsets are never constructed,
		// because the offset is polymorphic across endpoints: an ObjectId, an
		// ISO timestamp, an integer and an opaque token all appear.
		nextURL, err := c.checkSameOrigin(r, pageMeta.NextURL)
		if err != nil {
			return err
		}
		next = nextURL
	}

	return sink.Close(meta)
}

// streamPage reads one page, yielding each element to sink and reporting how many
// it yielded. already is the running total across earlier pages, which is what the
// item limit is measured against.
func (c *Client) streamPage(
	ctx context.Context, r Request, pageURL string, sink Sink, already int,
) (Meta, int, error) {
	resp, err := c.do(ctx, r, pageURL)
	if err != nil {
		return Meta{}, 0, err
	}
	defer resp.Body.Close()

	pageMeta := metaFrom(resp)

	// Decoded element by element rather than into a slice: the array is bounded at
	// a size worth not holding twice, and a sink that writes through gets to start
	// before the body ends.
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))

	tok, err := dec.Token()
	if err != nil {
		return Meta{}, 0, apierr.Wrap(apierr.KindDecode, err, "%s: reading response", r.Op)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return Meta{}, 0, apierr.New(apierr.KindDecode,
			"%s: expected a JSON array, got %v", r.Op, tok)
	}

	added := 0
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return Meta{}, added, apierr.FromNetwork(r.Op, err)
		}

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Meta{}, added, apierr.Wrap(apierr.KindDecode, err, "%s: reading item", r.Op)
		}
		if err := sink.Item(raw); err != nil {
			return Meta{}, added, err
		}
		added++

		if r.Limit > 0 && already+added >= r.Limit {
			return pageMeta, added, nil
		}
	}

	return pageMeta, added, nil
}

// do issues one request and turns a non-2xx into a classified error.
func (c *Client) do(ctx context.Context, r Request, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, apierr.Wrap(apierr.KindInternal, err, "%s: building request", r.Op)
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		resp.Body.Close()
		return nil, apierr.FromStatus(r.Op, resp.StatusCode, string(body))
	}
	return resp, nil
}

// StartURL runs the request builder and merges ExtraQuery, returning the URL of
// the first page.
func (c *Client) StartURL(r Request) (string, error) {
	if r.Build == nil {
		return "", apierr.New(apierr.KindInternal, "%s: request has no builder", r.Op)
	}

	req, err := r.Build(c.base.String())
	if err != nil {
		return "", apierr.Wrap(apierr.KindInternal, err, "%s: building request", r.Op)
	}
	if req.URL == nil {
		return "", apierr.New(apierr.KindInternal, "%s: built request has no URL", r.Op)
	}

	AppendQuery(req.URL, r.ExtraQuery)
	return req.URL.String(), nil
}

// AppendQuery appends parameters to a URL's query, preserving their order.
//
// Appending rather than merging through url.Values is what keeps the order:
// url.Values sorts by key when it encodes, which would separate every filter's
// type from its value. The bracketed keys are escaped to %5B/%5D, which the API
// accepts — verified live.
func AppendQuery(u *url.URL, params []filters.Param) {
	if u == nil || len(params) == 0 {
		return
	}

	encoded := filters.Encode(params)
	if u.RawQuery == "" {
		u.RawQuery = encoded
		return
	}
	u.RawQuery += "&" + encoded
}

// checkSameOrigin refuses a Link header that points somewhere else.
//
// This is the second of the two independent gates on the token: the transport's
// allowlist would also reject it, but checking here means the request to a host
// the Link named is never even built.
func (c *Client) checkSameOrigin(r Request, rawURL string) (string, error) {
	if rawURL == "" {
		return "", nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", apierr.Wrap(apierr.KindServer, err,
			"%s: cannot parse the Link header URL %q", r.Op, rawURL)
	}

	if !strings.EqualFold(u.Host, c.base.Host) || u.Scheme != c.base.Scheme {
		return "", apierr.New(apierr.KindUntrustedHost,
			"%s: refusing to follow pagination from %s to %s",
			r.Op, c.base.Host, u.Host)
	}
	return u.String(), nil
}

const (
	// defaultMaxPages bounds --all-pages. At 30 items a page this is 30,000
	// items, well past anything worth reading, and it stops an unbounded walk
	// against a 30-request-per-minute limit.
	defaultMaxPages = 1000

	// maxBodyBytes bounds a single object or one page of a list. Event payloads
	// with threads can reach a megabyte.
	maxBodyBytes = 64 << 20

	// maxErrorBytes bounds how much of an error body is read for its message.
	maxErrorBytes = 64 << 10
)

// metaFrom reads the pagination headers.
func metaFrom(resp *http.Response) Meta {
	meta := Meta{TotalCount: -1}

	if v := resp.Header.Get("X-Total-Count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			meta.TotalCount = n
		}
	}
	meta.NextURL = nextLink(resp.Header.Get("Link"))

	return meta
}

// nextLink extracts the rel="next" URL from a Link header.
func nextLink(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		open := strings.IndexByte(part, '<')
		close := strings.IndexByte(part, '>')
		if open < 0 || close < open {
			continue
		}
		target := part[open+1 : close]

		for param := range strings.SplitSeq(part[close+1:], ";") {
			param = strings.TrimSpace(param)
			key, value, ok := strings.Cut(param, "=")
			if !ok || strings.TrimSpace(key) != "rel" {
				continue
			}
			if strings.Trim(strings.TrimSpace(value), `"'`) == "next" {
				return target
			}
		}
	}
	return ""
}
