package render_test

import (
	"testing"
	"time"

	"github.com/geckoboard/bugsnag-cli/internal/render"
	"gotest.tools/v3/assert"
)

var now = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func TestRelative(t *testing.T) {
	for _, tc := range []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"just now", 0, "just now"},
		{"seconds", 38 * time.Second, "38 seconds ago"},
		{"one minute", 60 * time.Second, "1 minute ago"},
		{"minutes", 5 * time.Minute, "5 minutes ago"},
		{"one hour", time.Hour, "1 hour ago"},
		{"hours", 5 * time.Hour, "5 hours ago"},
		{"one day", 24 * time.Hour, "1 day ago"},
		{"days", 3 * 24 * time.Hour, "3 days ago"},
		{"one week", 7 * 24 * time.Hour, "1 week ago"},
		// The dashboard shows a 30-day window as "4 weeks ago", which is why
		// weeks run to eight before months take over.
		{"thirty days reads as four weeks", 30 * 24 * time.Hour, "4 weeks ago"},
		{"months", 90 * 24 * time.Hour, "3 months ago"},
		{"years", 400 * 24 * time.Hour, "1 year ago"},
		{"future", -5 * time.Minute, "5 minutes from now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, render.Relative(now.Add(-tc.ago), now), tc.want)
		})
	}
}

// TestTimestampAutoDependsOnTTY covers the reason the default is not simply
// "relative": an agent may feed a timestamp straight back as a filter value, and
// relative time also makes output undiffable between runs.
func TestTimestampAutoDependsOnTTY(t *testing.T) {
	const raw = "2026-07-28T11:59:22+00:00"

	tty := render.Mode{TTY: true, Width: 80, Time: render.TimeAuto, Now: now}
	assert.Equal(t, tty.Timestamp(raw), "38 seconds ago")

	piped := render.Mode{TTY: false, Width: 80, Time: render.TimeAuto, Now: now}
	assert.Equal(t, piped.Timestamp(raw), raw)
}

// TestTimestampRawIsByteFaithful: --time raw must echo exactly what the API sent,
// so the value round-trips as a filter.
func TestTimestampRawIsByteFaithful(t *testing.T) {
	m := render.Mode{TTY: true, Width: 80, Time: render.TimeRaw, Now: now}
	for _, raw := range []string{
		"2026-07-28T11:59:22+00:00",
		"2026-07-28T11:59:22Z",
		"2026-07-28T11:59:22.123Z",
	} {
		assert.Equal(t, m.Timestamp(raw), raw)
	}
}

// TestTimestampUnparseablePassesThrough: if the API starts sending a shape we do
// not know, showing it verbatim keeps the only information we had. Replacing it
// with a placeholder would throw that away.
func TestTimestampUnparseablePassesThrough(t *testing.T) {
	m := render.Mode{TTY: true, Width: 80, Time: render.TimeRelative, Now: now}
	const weird = "last Tuesday-ish"

	assert.Equal(t, m.Timestamp(weird), weird)
}

func TestTimestampEmpty(t *testing.T) {
	m := render.Mode{TTY: true, Width: 80, Now: now}
	assert.Equal(t, m.Timestamp(""), "")
}

// TestTimestampRange reads newest-first, the way the dashboard does, with the
// absolute dates alongside so the range can be lined up with a chart.
func TestTimestampRange(t *testing.T) {
	m := render.Mode{TTY: true, Width: 80, Time: render.TimeRelative, Now: now}
	got := m.TimestampRange("2026-06-28T12:00:00Z", "2026-07-28T11:59:00Z")

	assert.Equal(t, got, "1 minute ago – 4 weeks ago (Jun 28 – Jul 28)")
}

func TestTimestampRangePipedIsRaw(t *testing.T) {
	const from, to = "2026-06-28T12:00:00Z", "2026-07-28T11:59:00Z"

	m := render.Mode{TTY: false, Width: 80, Time: render.TimeAuto, Now: now}
	assert.Equal(t, m.TimestampRange(from, to), to+" – "+from)
}

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{14612, "14,612"},
		{70681, "70,681"},
		{5700000, "5,700,000"},
		// Beyond float64's exact integer range, which is why these fields are
		// int rather than the spec's float-shaped `number`.
		{9007199254740993, "9,007,199,254,740,993"},
		{-1234, "-1,234"},
	} {
		assert.Equal(t, render.Count(tc.in), tc.want)
	}
}

func TestCompact(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{70681, "70.7k"},
		{5700000, "5.7M"},
		{1500000000, "1.5B"},
	} {
		assert.Equal(t, render.Compact(tc.in), tc.want)
	}
}
