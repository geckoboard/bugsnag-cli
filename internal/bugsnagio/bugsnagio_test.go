package bugsnagio_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"github.com/geckoboard/bugsnag-cli/internal/bugsnagio"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
	"github.com/geckoboard/bugsnag-cli/internal/filters"
	"github.com/geckoboard/bugsnag-cli/internal/transport"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

// errorItem is a stand-in for a generated type, with the shapes that matter:
// an int count that must stay exact and a nested object the spec implies is a
// bool.
type errorItem struct {
	ID          string `json:"id"`
	ErrorClass  string `json:"error_class"`
	Events      int    `json:"events"`
	ReopenRules *struct {
		ReopenIf    string `json:"reopen_if"`
		Occurrences int    `json:"occurrences"`
	} `json:"reopen_rules"`
}

func newClient(t *testing.T, srv *httptest.Server) *bugsnagio.Client {
	t.Helper()

	doer, err := transport.New(transport.Options{
		Token: "tok",
		Hosts: []string{srv.URL},
		Sleep: func(time.Duration) {},
	})
	assert.NilError(t, err)
	c, err := bugsnagio.NewClient(doer, srv.URL)
	assert.NilError(t, err)
	return c
}

func TestStreamYieldsEveryItem(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Total-Count", "3")
		w.Write([]byte(`[{"id":"a","events":1},{"id":"b","events":2},{"id":"c","events":3}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list errors", Build: path("/errors", nil)}, sink)
	assert.NilError(t, err)

	assert.Equal(t, len(sink.Items), 3)
	assert.Equal(t, sink.Meta.TotalCount, 3)
}

func TestMissingTotalCountIsMinusOne(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":"a"}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)
	assert.NilError(t, err)

	// -1 rather than 0, so a view can tell "not reported" from "none".
	assert.Equal(t, sink.Meta.TotalCount, -1)
}

// TestFollowsLinkVerbatim checks the paginator uses the URL the server gave it
// rather than building an offset. The offset is polymorphic across endpoints —
// ObjectId, ISO timestamp, integer, opaque token — so constructing one is never
// safe. The token here is deliberately something the CLI could not have derived.
func TestFollowsLinkVerbatim(t *testing.T) {

	const opaque = "eyJvZmZzZXQiOiJvcGFxdWUtdG9rZW4tMiJ9"

	var paths []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		w.Header().Set("X-Total-Count", "4")

		switch r.URL.Query().Get("cursor") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/errors?cursor=%s>; rel="next"`, srv.URL, opaque))
			w.Write([]byte(`[{"id":"a"},{"id":"b"}]`))
		case opaque:
			w.Write([]byte(`[{"id":"c"},{"id":"d"}]`))
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list errors", Build: path("/errors", nil), AllPages: true}, sink)
	assert.NilError(t, err)

	assert.Equal(t, len(sink.Items), 4)
	assert.Equal(t, len(paths), 2)
	assert.Check(t, is.Contains(paths[1], opaque), "second request did not use the opaque cursor from the Link header")
}

// TestWithoutAllPagesStopsAtOnePage but still reports the next URL, so the
// footer can say there is more.
func TestWithoutAllPagesStopsAtOnePage(t *testing.T) {

	var hits atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`<%s/errors?offset=2>; rel="next"`, srv.URL))
		w.Write([]byte(`[{"id":"a"}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list errors", Build: path("/errors", nil)}, sink)
	assert.NilError(t, err)

	assert.Equal(t, hits.Load(), int64(1))
	assert.Assert(t, sink.Meta.NextURL != "", "NextURL is empty; the footer needs it to say there is another page")
}

// TestRefusesToFollowLinkToAnotherHost is the paginator's half of the
// token-exfiltration defence. It asserts the attacker recorded zero requests, not
// merely that the token was absent.
func TestRefusesToFollowLinkToAnotherHost(t *testing.T) {

	var attackerHits atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerHits.Add(1)
		w.Write([]byte(`[]`))
	}))
	defer attacker.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`<%s/steal>; rel="next"`, attacker.URL))
		w.Write([]byte(`[{"id":"a"}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list errors", Build: path("/errors", nil), AllPages: true}, sink)

	assert.Assert(t, err != nil, "expected following a cross-host Link to be refused")
	assert.Equal(t, exitcode.Of(err), exitcode.UntrustedHost)
	assert.Equal(t, attackerHits.Load(), int64(0))
}

func TestDetectsPaginationLoop(t *testing.T) {

	var hits atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// Always points at the same URL.
		w.Header().Set("Link", fmt.Sprintf(`<%s/errors?offset=1>; rel="next"`, srv.URL))
		w.Write([]byte(`[{"id":"a"}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list errors", Build: path("/errors", nil), AllPages: true}, sink)

	assert.Assert(t, err != nil, "expected a pagination loop to be detected")
	assert.Check(t, is.Contains(err.Error(), "loop"), "error = %v, want it to mention a loop", err)
	assert.Assert(t, hits.Load() <= 3, "made %d requests before detecting the loop", hits.Load())
}

func TestLimitStopsEarly(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":"a"},{"id":"b"},{"id":"c"},{"id":"d"}]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil), Limit: 2}, sink)
	assert.NilError(t, err)

	assert.Equal(t, len(sink.Items), 2)
}

func TestNonArrayResponseIsADecodeError(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"not":"an array"}`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)

	assert.Assert(t, err != nil, "expected a decode error")
	assert.Equal(t, exitcode.Of(err), exitcode.Decode)
}

func TestOneFetchesASingleObject(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"a","error_class":"*fmt.wrapError","events":70681}`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).One(context.Background(),
		bugsnagio.Request{Op: "view error", Build: path("/errors/a", nil)}, sink)
	assert.NilError(t, err)

	assert.Equal(t, len(sink.Items), 1)
	assert.Equal(t, sink.Items[0].Events, 70681)
}

func TestQueryIsSent(t *testing.T) {

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	req := bugsnagio.Request{
		Op: "list",
		Build: path("/errors", url.Values{
			"per_page": []string{"30"},
			"sort":     []string{"last_seen"},
		}),
	}
	err := newClient(t, srv).Stream(context.Background(), req, sink)
	assert.NilError(t, err)

	for _, want := range []string{"per_page=30", "sort=last_seen"} {
		assert.Check(t, is.Contains(gotQuery, want), "query %q is missing %q", gotQuery, want)
	}
}

func TestContextCancellationStopsStreaming(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":"a"}]`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(ctx, bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)

	assert.Assert(t, err != nil, "expected a cancellation error")
	assert.Equal(t, exitcode.Of(err), exitcode.Canceled)
}

// TestJSONSinkIsByteFaithful is the promise the raw path makes: key order,
// number literals and escapes survive, so `--json | jq .` matches `curl | jq .`.
func TestJSONSinkIsByteFaithful(t *testing.T) {

	// Deliberately hostile: keys out of alphabetical order, mixed case, a number
	// past float64's exact integer range, and an unknown nested field.
	const body = `[{"zebra":1,"Alpha":"A","events":9007199254740993,` +
		`"metaData":{"zzz":1,"aaa":2,"Mixed":3},"unknown_future_field":{"deep":[1,2,{"x":null}]}}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	var out bytes.Buffer
	sink := bugsnagio.NewJSONSink(&out, true)
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)
	assert.NilError(t, err)

	got := out.String()

	// The huge integer must survive as a literal, not as 9.007199254740992e+15.
	assert.Check(t, is.Contains(got, "9007199254740993"), "the exact integer literal was lost:\n%s", got)
	// Key order must be preserved, not alphabetised.
	zebra, alpha := strings.Index(got, "zebra"), strings.Index(got, "Alpha")
	assert.Assert(t, zebra < alpha, "keys were reordered:\n%s", got)
	zzz, aaa := strings.Index(got, "zzz"), strings.Index(got, "aaa")
	assert.Assert(t, zzz < aaa, "metaData keys were alphabetised:\n%s", got)
	// An unknown field must not be dropped.
	assert.Check(t, is.Contains(got, "unknown_future_field"), "an unknown field was dropped:\n%s", got)

	// Whitespace aside, it must be exactly what the server sent.
	assert.Assert(t, sameJSON(t, got, body), "output is not equivalent to the response:\n got %s\nwant %s", got, body)
}

func TestJSONSinkEmptyArray(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	sink := bugsnagio.NewJSONSink(&out, true)
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)
	assert.NilError(t, err)

	got := strings.TrimSpace(out.String())
	assert.Equal(t, got, "[]")
}

// TestJSONSinkAcrossPagesProducesOneArray: two pages cannot be joined by
// appending their bytes, so the array is rebuilt around the items.
func TestJSONSinkAcrossPagesProducesOneArray(t *testing.T) {

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("p") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/x?p=2>; rel="next"`, srv.URL))
			w.Write([]byte(`[{"id":"a"},{"id":"b"}]`))
			return
		}
		w.Write([]byte(`[{"id":"c"}]`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	sink := bugsnagio.NewJSONSink(&out, true)
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil), AllPages: true}, sink)
	assert.NilError(t, err)

	var items []map[string]any
	err = json.Unmarshal(out.Bytes(), &items)
	assert.NilError(t, err, "output is not a single valid JSON array:\n%s", out.String())
	assert.Equal(t, len(items), 3)
}

// TestTypedSinkDegradesPerItem: one item the spec models wrongly must not take
// out the rest of the response.
func TestTypedSinkDegradesPerItem(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The middle item has events as a string, which the typed decode
		// rejects.
		w.Write([]byte(`[
			{"id":"a","events":1},
			{"id":"b","events":"not a number"},
			{"id":"c","events":3}
		]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)
	assert.NilError(t, err, "Stream should not fail because one item did not decode")

	assert.Equal(t, len(sink.Items), 2)
	assert.Equal(t, len(sink.Degraded), 1)
	assert.Equal(t, sink.Degraded[0].Fields["id"], "b")
	w := sink.Warning()
	assert.Check(t, is.Contains(w, "1 of 3"), "Warning = %q, want it to say 1 of 3", w)
}

// TestTypedSinkInvalidJSONIsAnError is tier 3: there is nothing truthful left to
// show, so this one does fail.
func TestTypedSinkInvalidJSONIsAnError(t *testing.T) {

	sink := bugsnagio.NewTypedSink[errorItem]()
	err := sink.Item(json.RawMessage(`{"id":`))
	assert.Assert(t, err != nil, "expected an error for invalid JSON")
	assert.Equal(t, exitcode.Of(err), exitcode.Decode)
}

// TestTeeSinkFeedsBoth proves one pass over the response can serve both paths.
func TestTeeSinkFeedsBoth(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":"a","events":1},{"id":"b","events":2}]`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	jsonSink := bugsnagio.NewJSONSink(&out, true)
	typed := bugsnagio.NewTypedSink[errorItem]()

	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)},
		bugsnagio.TeeSink{jsonSink, typed})
	assert.NilError(t, err)

	assert.Equal(t, len(typed.Items), 2)
	var items []map[string]any
	err = json.Unmarshal(out.Bytes(), &items)
	assert.NilError(t, err, "json sink output invalid")
	assert.Equal(t, len(items), 2)
}

// TestGeneratedBuilderPlugsIn: URL and parameter construction comes from the
// spec via the generated New*Request builders, not from hand-assembled paths.
func TestGeneratedBuilderPlugsIn(t *testing.T) {

	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	perPage := 30
	sort := bugsnagapi.LastSeen
	params := &bugsnagapi.ListProjectErrorsParams{PerPage: &perPage, Sort: &sort}

	sink := bugsnagio.NewTypedSink[errorItem]()
	req := bugsnagio.Request{
		Op: "list errors",
		Build: func(server string) (*http.Request, error) {
			return bugsnagapi.NewListProjectErrorsRequest(server, "proj123", params)
		},
	}
	err := newClient(t, srv).Stream(context.Background(), req, sink)
	assert.NilError(t, err)

	assert.Equal(t, gotPath, "/projects/proj123/errors")
	for _, want := range []string{"per_page=30", "sort=last_seen"} {
		assert.Check(t, is.Contains(gotQuery, want), "query %q is missing %q", gotQuery, want)
	}
}

// TestExtraQueryIsAppendedInOrder is how hand-encoded filters reach the request.
// The overlay removed the generated filter parameters because their encoder
// produced the wrong wire format, so internal/filters builds them and they join
// here.
//
// The order is asserted on the raw query rather than on a parsed map, because it
// is the part that has to survive: the API pairs each condition's type with the
// value that follows it, so sorting them apart changes what was asked for.
func TestExtraQueryIsAppendedInOrder(t *testing.T) {

	var gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	sink := bugsnagio.NewTypedSink[errorItem]()
	req := bugsnagio.Request{
		Op:    "list errors",
		Build: path("/errors", url.Values{"per_page": []string{"30"}}),
		ExtraQuery: []filters.Param{
			{Key: "filters[event.severity][][type]", Value: "eq"},
			{Key: "filters[event.severity][][value]", Value: "error"},
			{Key: "filters[event.severity][][type]", Value: "eq"},
			{Key: "filters[event.severity][][value]", Value: "warning"},
		},
	}
	err := newClient(t, srv).Stream(context.Background(), req, sink)
	assert.NilError(t, err)

	unescaped, err := url.QueryUnescape(gotRaw)
	assert.NilError(t, err, "query is not decodable")

	want := "per_page=30&" +
		"filters[event.severity][][type]=eq&filters[event.severity][][value]=error&" +
		"filters[event.severity][][type]=eq&filters[event.severity][][value]=warning"
	assert.Equal(t, unescaped, want)
}

// countingSink records when each item arrives, so streaming can be asserted
// rather than assumed.
type countingSink struct {
	seen func(raw json.RawMessage)
}

func (c countingSink) Item(raw json.RawMessage) error {
	c.seen(raw)
	return nil
}
func (countingSink) Close(bugsnagio.Meta) error { return nil }

// TestItemsAreStreamedNotBuffered proves items are teed at the item level rather
// than after reading the whole body. The server writes the first item, waits for
// the client to have processed it, and only then writes the second: if the
// decoder buffered the whole array first, this would deadlock.
func TestItemsAreStreamedNotBuffered(t *testing.T) {

	gotFirst := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server does not support flushing")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":"a","events":1}`)
		flusher.Flush()

		select {
		case <-gotFirst:
		case <-time.After(5 * time.Second):
			t.Error("the client had not received the first item before the body was finished")
		}

		io.WriteString(w, `,{"id":"b","events":2}]`)
		flusher.Flush()
	}))
	defer srv.Close()

	var mu sync.Mutex
	var ids []string
	sink := countingSink{seen: func(raw json.RawMessage) {
		var item errorItem
		json.Unmarshal(raw, &item)

		mu.Lock()
		ids = append(ids, item.ID)
		first := len(ids) == 1
		mu.Unlock()

		if first {
			close(gotFirst)
		}
	}}

	err := newClient(t, srv).Stream(context.Background(),
		bugsnagio.Request{Op: "list", Build: path("/x", nil)}, sink)
	assert.NilError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Assert(t, len(ids) == 2 && ids[0] == "a" && ids[1] == "b", "items = %v, want [a b]", ids)
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()

	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		t.Errorf("left side is not valid JSON: %v", err)
		return false
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Errorf("right side is not valid JSON: %v", err)
		return false
	}
	x, _ := json.Marshal(av)
	y, _ := json.Marshal(bv)
	return bytes.Equal(x, y)
}

// path builds a request for a literal path. Production requests all come from the
// generated New*Request builders; this is only for naming a path in a test.
func path(p string, query url.Values) bugsnagio.BuildFunc {
	return func(server string) (*http.Request, error) {
		u, err := url.Parse(strings.TrimSuffix(server, "/") + p)
		if err != nil {
			return nil, err
		}
		if len(query) > 0 {
			u.RawQuery = query.Encode()
		}
		return http.NewRequest(http.MethodGet, u.String(), nil)
	}
}
