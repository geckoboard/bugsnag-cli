// Package clitest is the test harness: a fake API server and a runner that
// drives the real command tree.
//
// The server models behaviour rather than returning canned responses. It honours
// per_page, mints opaque Link offsets the CLI could not have constructed, honours
// q for project search, and records every request so filter encoding can be
// asserted against the raw query string. An unrouted request calls t.Errorf: a
// request nobody expected is a test failure, not a silent 404.
package clitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/view"
)

// Server is a fake Data Access API.
type Server struct {
	*httptest.Server

	t *testing.T

	mu       sync.Mutex
	requests []Request

	// Token is the token the server requires.
	Token string

	// Orgs, Projects, Errors, Events, Trend and Pivots are the fixtures served.
	Orgs     []json.RawMessage
	Projects []json.RawMessage
	Errors   []json.RawMessage
	Events   []json.RawMessage
	Trend    []json.RawMessage
	Pivots   []json.RawMessage

	// EventFields is what a project can be filtered on.
	EventFields []json.RawMessage

	// LatestEvent is returned by the latest-event endpoint.
	LatestEvent json.RawMessage

	// Status, when non-zero, is returned for every request instead of data.
	Status int

	// Body overrides the response body when Status is set.
	Body string
}

// Request is one recorded request.
type Request struct {
	Path  string
	Query map[string][]string
	Raw   string
}

// NewServer starts a fake API with the default fixtures.
func NewServer(t *testing.T) *Server {
	t.Helper()

	s := &Server{
		t:           t,
		Token:       "test-token",
		Orgs:        DefaultOrgs(),
		Projects:    DefaultProjects(),
		Errors:      DefaultErrors(),
		Events:      DefaultEvents(),
		Trend:       DefaultTrend(),
		Pivots:      DefaultPivots(),
		EventFields: DefaultEventFields(),
		LatestEvent: DefaultEvent(),
	}

	mux := http.NewServeMux()

	// Go 1.22 method and wildcard patterns, so a path is matched once and
	// precisely rather than by a chain of prefix tests.
	mux.HandleFunc("GET /user/organizations", s.handle(func() []json.RawMessage { return s.Orgs }))
	mux.HandleFunc("GET /organizations/{org}/projects", s.handleProjects)
	mux.HandleFunc("GET /projects/{project}/errors", s.handle(func() []json.RawMessage { return s.Errors }))
	mux.HandleFunc("GET /projects/{project}/errors/{error}", s.handleOne(func() json.RawMessage {
		if len(s.Errors) == 0 {
			return nil
		}
		return s.Errors[0]
	}))
	mux.HandleFunc("GET /projects/{project}/errors/{error}/events", s.handle(func() []json.RawMessage { return s.Events }))
	mux.HandleFunc("GET /projects/{project}/events", s.handle(func() []json.RawMessage { return s.Events }))
	mux.HandleFunc("GET /projects/{project}/events/{event}", s.handleOne(func() json.RawMessage { return s.LatestEvent }))
	mux.HandleFunc("GET /errors/{error}/latest_event", s.handleOne(func() json.RawMessage { return s.LatestEvent }))
	mux.HandleFunc("GET /projects/{project}/errors/{error}/trend", s.handle(func() []json.RawMessage { return s.Trend }))
	mux.HandleFunc("GET /projects/{project}/errors/{error}/pivots", s.handle(func() []json.RawMessage { return s.Pivots }))
	mux.HandleFunc("GET /projects/{project}/event_fields", s.handle(func() []json.RawMessage { return s.EventFields }))

	// Anything unrouted is a test failure. A silent 404 would let a wrong URL
	// look like an empty result.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		s.t.Errorf("unexpected request to the fake API: %s %s", r.Method, r.URL.String())
		http.Error(w, `{"errors":["no such route in the fake API"]}`, http.StatusNotFound)
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Requests returns the recorded requests.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// LastRequest returns the most recent request.
func (s *Server) LastRequest() Request {
	reqs := s.Requests()
	if len(reqs) == 0 {
		s.t.Fatal("no requests were made")
	}
	return reqs[len(reqs)-1]
}

// LastRequestTo is the most recent request whose path ends in suffix, which is how
// a test picks out the call it cares about from the resolution traffic around it.
func (s *Server) LastRequestTo(suffix string) Request {
	reqs := s.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(reqs[i].Path, suffix) {
			return reqs[i]
		}
	}
	s.t.Fatalf("no request to a path ending in %q; got %d requests", suffix, len(reqs))
	return Request{}
}

// RequestCount is how many requests were made.
func (s *Server) RequestCount() int { return len(s.Requests()) }

// handle serves a paginated list.
func (s *Server) handle(items func() []json.RawMessage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if s.fail(w) {
			return
		}
		s.writePage(w, r, applyFilters(r, items()))
	}
}

// applyFilters models the API's filtering, including the part that surprises
// people: a filter on a field id it does not act on is answered 200 and ignored,
// so the result comes back unfiltered rather than empty or 400.
//
// Only the ids verified to work against the live API are acted on here, which is
// what makes the difference testable — event.class filters, error.class is
// ignored, and the CLI has to notice the second one from the rows alone.
func applyFilters(r *http.Request, items []json.RawMessage) []json.RawMessage {
	conditions := parseFilters(r.URL.Query())
	if len(conditions) == 0 {
		return items
	}

	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		if matchesFilters(view.FilterableValues(raw), conditions) {
			out = append(out, raw)
		}
	}
	return out
}

// filterableIDs are the field ids the API acts on, mapped to the value they
// select and whether the match is a substring one.
var filterableIDs = map[string]struct {
	key       string
	substring bool
}{
	"event.class":    {"class", true},
	"event.message":  {"message", true},
	"app.context":    {"context", true},
	"error.status":   {"status", false},
	"event.severity": {"severity", false},
	"error.id":       {"id", false},
}

// parseFilters reads the bracket syntax back into conditions grouped by field.
func parseFilters(q url.Values) map[string][]condition {
	out := make(map[string][]condition)
	for key, values := range q {
		field, ok := strings.CutSuffix(strings.TrimPrefix(key, "filters["), "][][value]")
		if !ok || field == key {
			continue
		}
		if _, known := filterableIDs[field]; !known {
			continue
		}

		types := q["filters["+field+"][][type]"]
		for i, v := range values {
			op := "eq"
			if i < len(types) {
				op = types[i]
			}
			out[field] = append(out[field], condition{op: op, value: v})
		}
	}
	return out
}

type condition struct {
	op    string
	value string
}

// matchesFilters reports whether one item satisfies every filtered field.
// Conditions on one field are OR-ed, and different fields are AND-ed.
func matchesFilters(values map[string]string, conditions map[string][]condition) bool {
	for field, conds := range conditions {
		spec := filterableIDs[field]
		got, present := values[spec.key]

		var satisfied bool
		for _, c := range conds {
			matched := present && strings.EqualFold(got, c.value)
			if spec.substring {
				matched = present && strings.Contains(strings.ToLower(got), strings.ToLower(c.value))
			}

			if c.op == "ne" {
				if matched {
					return false
				}
				satisfied = true
				continue
			}
			if matched {
				satisfied = true
			}
		}
		if !satisfied {
			return false
		}
	}
	return true
}

// handleOne serves a single object.
func (s *Server) handleOne(item func() json.RawMessage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if s.fail(w) {
			return
		}

		body := item()
		if body == nil {
			http.Error(w, `{"errors":["not found"]}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Ratelimit-Limit", "30")
		w.Write(body)
	}
}

// handleProjects honours q, so autodetect scoring is exercised against a real
// search rather than a fixed list.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	s.record(r)
	if s.fail(w) {
		return
	}

	q := strings.ToLower(r.URL.Query().Get("q"))
	if q == "" {
		s.writePage(w, r, s.Projects)
		return
	}

	var matched []json.RawMessage
	for _, raw := range s.Projects {
		var p struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(strings.ToLower(p.Slug), q) {
			matched = append(matched, raw)
		}
	}
	s.writePage(w, r, matched)
}

// writePage honours per_page and mints an opaque Link offset.
//
// The offset is deliberately something the CLI could not have derived, so
// "pagination follows Link verbatim" is genuinely tested rather than accidentally
// satisfied by the CLI constructing the same URL.
func (s *Server) writePage(w http.ResponseWriter, r *http.Request, items []json.RawMessage) {
	perPage := 30
	if v := r.URL.Query().Get("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			perPage = n
		}
	}

	start := 0
	if cursor := r.URL.Query().Get("offset"); cursor != "" {
		if n, ok := decodeCursor(cursor); ok {
			start = n
		} else {
			s.t.Errorf("the CLI sent an offset the server never issued: %q", cursor)
		}
	}

	end := min(start+perPage, len(items))
	if start > len(items) {
		start = len(items)
	}
	page := items[start:end]

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
	w.Header().Set("X-Ratelimit-Limit", "30")

	if end < len(items) {
		next := fmt.Sprintf("%s%s?%s", s.URL, r.URL.Path, nextQuery(r, encodeCursor(end)))
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
	}

	if len(page) == 0 {
		w.Write([]byte("[]"))
		return
	}

	w.Write([]byte("["))
	for i, item := range page {
		if i > 0 {
			w.Write([]byte(","))
		}
		w.Write(item)
	}
	w.Write([]byte("]"))
}

func (s *Server) fail(w http.ResponseWriter) bool {
	if s.Status == 0 {
		return false
	}

	body := s.Body
	if body == "" {
		body = `{"errors":["the fake API was told to fail"]}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.Status)
	w.Write([]byte(body))
	return true
}

func (s *Server) record(r *http.Request) {
	auth := r.Header.Get("Authorization")
	if want := "token " + s.Token; auth != want {
		s.t.Errorf("Authorization = %q, want %q", auth, want)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{
		Path:  r.URL.Path,
		Query: r.URL.Query(),
		Raw:   r.URL.RawQuery,
	})
}

func nextQuery(r *http.Request, cursor string) string {
	q := r.URL.Query()
	q.Set("offset", cursor)
	return q.Encode()
}

// encodeCursor produces an opaque token. The CLI must treat it as opaque, and
// the shape here makes an accidental match impossible.
func encodeCursor(index int) string {
	return fmt.Sprintf("opaque-%d-%s", index, cursorSalt)
}

func decodeCursor(cursor string) (int, bool) {
	rest, ok := strings.CutPrefix(cursor, "opaque-")
	if !ok {
		return 0, false
	}
	idx, salt, ok := strings.Cut(rest, "-")
	if !ok || salt != cursorSalt {
		return 0, false
	}
	n, err := strconv.Atoi(idx)
	if err != nil {
		return 0, false
	}
	return n, true
}

const cursorSalt = "z7Qk"
