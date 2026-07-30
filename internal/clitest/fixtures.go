package clitest

import (
	"embed"
	"encoding/json"
	"fmt"
)

// The default fixtures model shapes the real API produces — verified against two
// live projects — with deliberately hostile bits so the ordinary path through
// every test exercises them rather than only a test that remembers to: a huge
// integer past float64's exact range, an unknown nested field, mixed-case
// metaData, a live-looking secret. The values are synthetic (no real project
// names, hosts or tokens), so the payloads are safe to commit.
//
// They live as JSON files under testdata/ and are embedded here. Keeping them as
// files means a fixture can be diffed against a captured-then-scrubbed real
// payload, and fixtures_test.go decodes each through the generated types to fail
// loudly when one drifts out of shape — which is how an app.releaseStage casing
// slip in the fixture stayed hidden until it was checked against the live API.

//go:embed testdata/*.json
var fixtureFS embed.FS

// DefaultOrgs returns one organization, so login resolves silently.
func DefaultOrgs() []json.RawMessage { return fixtureArray("orgs.json") }

// DefaultProjects includes the prefix pair worker/worker-replica, the case exact
// matching must resolve without ever cross-matching.
func DefaultProjects() []json.RawMessage { return fixtureArray("projects.json") }

// DefaultErrors carries, between them: an unknown nested field whose value must
// survive --json unchanged, a reopen_rules object with an n_occurrences_in_m_hours
// rule (the shape the spec's ErrorReopenRules defines), a trend tuple array — the
// error object's own [date, count] pairs, unlike the trend endpoint's
// {from, to, events_count} — an events count past float64's exact integer range,
// and a message far longer than any table column.
func DefaultErrors() []json.RawMessage { return fixtureArray("errors.json") }

// DefaultEvents is the thin list projection the events-on-error endpoint returns
// by default: no top-level error_class or message (they live in exceptions[0]),
// camelCase exception keys (errorClass), no stacktrace, and an app object
// carrying releaseStage but no version. Verified against two live projects: the
// projection omits the stacktrace, device, metaData and app.version that a full
// report carries, so none of those appear here either.
//
// The third event carries a different message from the first two, which is what
// real data looks like: a third of one error's occurrences came from a different
// call site, and a browser app's interpolated ids differ every time. Two
// identical messages and one that differs is what makes the message row testable
// both when occurrences agree and when they do not.
func DefaultEvents() []json.RawMessage { return fixtureArray("events.json") }

// DefaultEvent is a full event report: snake_case exception keys, a column_number
// integer where the spec says string, a frame whose in_project is null (the absent
// case the Tristate reader has to survive), and a request carrying a live-looking
// secret plus an x-honeycomb-team ingest key.
func DefaultEvent() json.RawMessage { return fixtureObject("event.json") }

// DefaultTrend is the bucketed trend, in the shape the trend endpoint actually
// returns: {from, to, events_count}, not the [date, count] pairs the error
// object's own trend field uses.
func DefaultTrend() []json.RawMessage { return fixtureArray("trend.json") }

// DefaultEventFields is the project's event-field catalogue.
//
// It models the verified relationship between that catalogue and the keys the
// filter API acts on. The catalogue lists event.class, which filters; it does not
// list error.class or app.version, which are the ids someone reaches for first
// and which the API accepts and then ignores. That is the whole reason a filter
// has to be checked against the rows it returns rather than trusted.
//
// It omits event.since and event.before, which do filter, because the catalogue
// is not a complete account of what works either.
//
// The custom field is a metaData id, which is what a custom field actually looks
// like: across one organization's 49 projects, all 22 custom ids were metaData.*
// and each appeared in exactly one project.
func DefaultEventFields() []json.RawMessage { return fixtureArray("event_fields.json") }

// DefaultPivots covers the Summaries table, shown by default on errors view.
//
// list, no_value and other are nested under `summary` because that is what the API
// sends, even though the spec declares them at the top level. It also carries a
// count-only pivot, which must be left out of the table rather than reporting its
// event total as a distinct count, and a zero-cardinality pivot, which must sort
// last rather than be hidden.
func DefaultPivots() []json.RawMessage { return fixtureArray("pivots.json") }

// fixtureArray loads a JSON array fixture as one RawMessage per element, so each
// item's exact bytes reach the sink the way the API's would — numbers, key order
// and unknown fields intact.
func fixtureArray(name string) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(fixtureBytes(name), &items); err != nil {
		panic(fmt.Sprintf("clitest: fixture %s is not a JSON array: %v", name, err))
	}
	return items
}

// fixtureObject loads a single-object fixture as its raw bytes.
func fixtureObject(name string) json.RawMessage {
	return json.RawMessage(fixtureBytes(name))
}

func fixtureBytes(name string) []byte {
	data, err := fixtureFS.ReadFile("testdata/" + name)
	if err != nil {
		panic(fmt.Sprintf("clitest: missing fixture %s: %v", name, err))
	}
	return data
}
