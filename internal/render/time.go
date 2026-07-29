package render

import (
	"fmt"
	"time"
)

// TimeStyle selects how an API timestamp is rendered.
type TimeStyle int

const (
	// TimeAuto renders relative times on a terminal and the raw API string when
	// piped. Relative time is what a human wants; the raw string is what a
	// caller can feed straight back as a filter value, and it keeps output
	// diffable between runs.
	TimeAuto TimeStyle = iota

	// TimeRelative always renders "4 weeks ago". Worth having explicitly
	// because agents sometimes run inside a pty, so TTY detection alone is not
	// a reliable proxy for "a human is reading this".
	TimeRelative

	// TimeLocal renders an absolute time in the local zone.
	TimeLocal

	// TimeRaw always echoes the API's own string, byte for byte.
	TimeRaw
)

// ParseTimeStyle parses the --time flag.
func ParseTimeStyle(s string) (TimeStyle, error) {
	switch s {
	case "auto", "":
		return TimeAuto, nil
	case "relative":
		return TimeRelative, nil
	case "local":
		return TimeLocal, nil
	case "raw":
		return TimeRaw, nil
	}
	return 0, fmt.Errorf("unknown time style %q: want auto, relative, local or raw", s)
}

// style resolves TimeAuto against the terminal: relative time for a reader, the
// raw API string for a pipe.
func (m Mode) style() TimeStyle {
	if m.Time != TimeAuto {
		return m.Time
	}
	if m.TTY {
		return TimeRelative
	}
	return TimeRaw
}

// Timestamp renders an API timestamp string.
//
// An unparseable value is returned unchanged rather than replaced with a
// placeholder: if the API starts sending a format we do not know, showing it
// verbatim is honest and still useful, whereas "invalid date" throws away the
// only information we had.
func (m Mode) Timestamp(raw string) string { return m.timestamp(raw, Relative) }

// timestamp is the shared path: resolve the style, echo the raw string when that
// is what was asked for or when it will not parse, otherwise render it either
// absolutely or through the caller's relative form.
func (m Mode) timestamp(raw string, relative func(t, now time.Time) string) string {
	if raw == "" {
		return ""
	}
	if m.style() == TimeRaw {
		return raw
	}

	t, err := parseAPITime(raw)
	if err != nil {
		return raw
	}
	if m.style() == TimeLocal {
		return absolute(t.Local(), m.Now)
	}
	return relative(t, m.Now)
}

// TimestampRange renders a first-seen/last-seen pair as the dashboard does:
// "1 minute ago – 4 weeks ago", with absolute dates alongside when the style is
// relative, since a relative range alone makes it hard to line up with a chart.
func (m Mode) TimestampRange(from, to string) string {
	switch {
	case from == "" && to == "":
		return ""
	case from == "":
		return m.Timestamp(to)
	case to == "":
		return m.Timestamp(from)
	}

	// The API's first_seen/last_seen are oldest-first; the dashboard reads
	// newest-first, which is also how the relative wording scans.
	lead := m.Timestamp(to) + " – " + m.Timestamp(from)

	if m.style() != TimeRelative {
		return lead
	}

	ft, ferr := parseAPITime(from)
	tt, terr := parseAPITime(to)
	if ferr != nil || terr != nil {
		return lead
	}
	return fmt.Sprintf("%s (%s – %s)", lead, shortDate(ft.Local()), shortDate(tt.Local()))
}

// durationUnits is the ladder both relative forms walk: the first entry whose
// limit the elapsed time is under supplies the unit, and the final entry is the
// catch-all, so its limit is never read.
//
// Weeks run to 8 before switching to months, so a 30-day window reads as
// "4 weeks ago" the way the dashboard shows it.
var durationUnits = []struct {
	limit time.Duration
	per   time.Duration
	long  string
	short string
}{
	{time.Minute, time.Second, "second", "s"},
	{time.Hour, time.Minute, "minute", "m"},
	{24 * time.Hour, time.Hour, "hour", "h"},
	{7 * 24 * time.Hour, 24 * time.Hour, "day", "d"},
	{56 * 24 * time.Hour, 7 * 24 * time.Hour, "week", "w"},
	{365 * 24 * time.Hour, 30 * 24 * time.Hour, "month", "mo"},
	{0, 365 * 24 * time.Hour, "year", "y"},
}

// elapsed picks the unit for d and the count of it.
func elapsed(d time.Duration) (n int, long, short string) {
	for _, u := range durationUnits[:len(durationUnits)-1] {
		if d < u.limit {
			return int(d / u.per), u.long, u.short
		}
	}
	last := durationUnits[len(durationUnits)-1]
	return int(d / last.per), last.long, last.short
}

// Relative renders d as a human phrase relative to now.
func Relative(t, now time.Time) string {
	d := now.Sub(t)

	suffix := " ago"
	if d < 0 {
		d = -d
		suffix = " from now"
	}
	if d < time.Second {
		return "just now"
	}

	n, long, _ := elapsed(d)
	return counted(n, long) + suffix
}

// TimestampShort renders a timestamp for a table column, where "1 minute ago" is
// too wide to spend. On a terminal it is a compact relative form; piped it is the
// raw API string, for the same reason Timestamp is.
func (m Mode) TimestampShort(raw string) string { return m.timestamp(raw, RelativeShort) }

// ShortDate renders a timestamp as "May 29", for stating a range compactly.
// Piped it stays the raw API string, like every other timestamp.
func (m Mode) ShortDate(raw string) string {
	if raw == "" {
		return ""
	}
	if m.style() == TimeRaw {
		return raw
	}
	t, err := parseAPITime(raw)
	if err != nil {
		return raw
	}
	return shortDate(t.Local())
}

// ShortDateOf renders a time we hold as a time rather than as an API string,
// which is how the end of a window that runs to now is stated. Piped it is the
// same RFC3339 form the API's own timestamps keep.
func (m Mode) ShortDateOf(t time.Time) string {
	if m.style() == TimeRaw {
		return t.UTC().Format(time.RFC3339)
	}
	return shortDate(t.Local())
}

// RelativeShort is Relative with the units abbreviated and the "ago" dropped:
// "38s", "4w". Only for columns, where the heading already says these are times.
func RelativeShort(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}

	n, _, short := elapsed(d)
	return fmt.Sprintf("%d%s", n, short)
}

// Count formats a whole number with thousands separators, so a six-figure event
// count is readable at a glance.
func Count(n int) string {
	if n < 0 {
		return "-" + Count(-n)
	}
	s := fmt.Sprint(n)

	var out []byte
	for i, digit := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digit)
	}
	return string(out)
}

// Compact formats a large number the way the dashboard header does: "5.7M"
// rather than "5,700,000". Used only where the label makes the scale clear.
func Compact(n int) string {
	switch {
	case n < 0:
		return "-" + Compact(-n)
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return trimZero(float64(n)/1000) + "k"
	case n < 1_000_000_000:
		return trimZero(float64(n)/1_000_000) + "M"
	}
	return trimZero(float64(n)/1_000_000_000) + "B"
}

// apiTimeLayouts are the shapes the API is known to return. RFC3339 covers
// "2017-04-14T03:04:49+00:00" and the Z form; the millisecond form turns up on
// some fields.
var apiTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseAPITime(raw string) (time.Time, error) {
	var err error
	for _, layout := range apiTimeLayouts {
		var t time.Time
		if t, err = time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

func absolute(t, now time.Time) string {
	if t.Year() == now.Year() {
		return t.Format("Jan 02 15:04")
	}
	return t.Format("2006 Jan 02 15:04")
}

func shortDate(t time.Time) string { return t.Format("Jan 02") }

// Plural returns unit pluralised for n: "frame" or "frames". The count is the
// caller's to place, because a phrase often has words between the two, as in
// "3 library frames".
func Plural(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return unit + "s"
}

// counted is the common shape, where the number leads: "3 frames".
func counted(n int, unit string) string {
	return fmt.Sprintf("%d %s", n, Plural(n, unit))
}

func trimZero(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}
