// Package dashboardurl reads a Bugsnag dashboard URL.
//
// It exists so an investigation can be handed over by pasting the address bar:
// someone looking at an error in the UI copies the URL, and the CLI works out
// which project, error, event and filters it names without being told.
//
// It is pure, and it treats the URL as hostile input. A URL arrives from outside
// the process while the CLI holds a live API token, so the host is validated
// against a fixed allowlist and then discarded: nothing here can decide where a
// request goes, only what to ask for.
package dashboardurl

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// Ref is what a dashboard URL identifies. Every field is optional except the
// project, because the routes narrow from a project to an error to an event.
type Ref struct {
	OrgSlug     string
	ProjectSlug string

	// ErrorID is empty for an inbox URL, which names a project and nothing else.
	ErrorID string

	// EventID is set when the URL pins one occurrence, which is the one the
	// person doing the handover was actually looking at.
	EventID string

	// Filters are the dashboard's own filter parameters, in the order they
	// appeared, as field id and value. They are not translated here: the mapping
	// from a field id to a flag lives with the flags, so there is one table
	// rather than two that can disagree.
	Filters []Filter

	// Ignored names the query parameters that were not understood. They are
	// reported rather than dropped, because a filter that was silently not
	// applied looks exactly like a filter that matched everything.
	Ignored []string
}

// Filter is one filter parameter from a dashboard URL.
type Filter struct {
	Field string
	Value string
}

// IsURL reports whether s should be read as a dashboard URL rather than an id.
//
// The test is a scheme, not a guess at the shape: an error id and an event id are
// both 24 hex characters and cannot be told apart, so anything that is not
// explicitly a URL stays an id.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Hosts are the dashboard hosts a URL may name: app.bugsnag.com, and the
// SmartBear host for organizations that were migrated.
var Hosts = []string{"app.bugsnag.com", "app.bugsnag.smartbear.com"}

// Parse reads a dashboard URL.
func Parse(raw string) (Ref, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Ref{}, fmt.Errorf("not a URL: %s", raw)
	}

	if err := checkOrigin(u); err != nil {
		return Ref{}, err
	}

	ref, err := parsePath(u.Path)
	if err != nil {
		return Ref{}, err
	}

	if err := parseQuery(u.Query(), &ref); err != nil {
		return Ref{}, err
	}

	if u.Fragment != "" {
		ref.Ignored = append(ref.Ignored, "#"+u.Fragment)
	}
	return ref, nil
}

// checkOrigin is the security boundary.
//
// The host has to be one of the known dashboards, and is then thrown away: the
// API host comes from configuration, so a pasted URL can never send the token
// somewhere new. Credentials, a port and a non-https scheme are refused rather
// than stripped, because each of them means the URL is not what it looks like.
func checkOrigin(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("a dashboard URL must be https, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("a dashboard URL must not carry credentials")
	}
	if u.Port() != "" {
		return fmt.Errorf("a dashboard URL must not name a port")
	}

	if slices.Contains(Hosts, strings.ToLower(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("%s is not a Bugsnag dashboard host; expected %s",
		u.Hostname(), strings.Join(Hosts, " or "))
}

// parsePath reads /{org}/{project}/errors[/{errorId}].
func parsePath(path string) (Ref, error) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 3 || segments[2] != "errors" {
		return Ref{}, fmt.Errorf(
			"expected a URL like https://%s/<org>/<project>/errors/<error-id>", Hosts[0])
	}
	if len(segments) > 4 {
		return Ref{}, fmt.Errorf("unexpected path after the error id: /%s",
			strings.Join(segments[4:], "/"))
	}

	ref := Ref{OrgSlug: segments[0], ProjectSlug: segments[1]}
	if err := checkSlug("organization", ref.OrgSlug); err != nil {
		return Ref{}, err
	}
	if err := checkSlug("project", ref.ProjectSlug); err != nil {
		return Ref{}, err
	}

	if len(segments) == 4 {
		id, err := checkID("error", segments[3])
		if err != nil {
			return Ref{}, err
		}
		ref.ErrorID = id
	}
	return ref, nil
}

// parseQuery reads event_id and the dashboard's filter parameters, and names
// everything else.
//
// The dashboard writes filters flat — filters[error.status]=open — which is the
// API's shorthand for an equality condition, and it uses relative values like
// 30d. Both are translated rather than forwarded: the CLI resolves a relative
// time to an absolute one so the request says what it actually asked for, and
// only fields it has verified are applied at all.
func parseQuery(q url.Values, ref *Ref) error {
	// Sorted so the reported order is stable, which matters because the ignored
	// list and the translated filters both end up in output someone reads.
	for _, key := range slices.Sorted(maps.Keys(q)) {
		values := q[key]

		switch {
		case key == "event_id":
			id, err := checkID("event", values[0])
			if err != nil {
				return err
			}
			ref.EventID = id

		case strings.HasPrefix(key, "filters[") && strings.HasSuffix(key, "]"):
			field := strings.TrimSuffix(strings.TrimPrefix(key, "filters["), "]")
			if field == "" {
				ref.Ignored = append(ref.Ignored, key)
				continue
			}
			for _, v := range values {
				if v == "" {
					continue
				}
				ref.Filters = append(ref.Filters, Filter{Field: field, Value: v})
			}

		default:
			ref.Ignored = append(ref.Ignored, key)
		}
	}
	return nil
}

// checkID keeps an id from becoming a path traversal or an injected query, since
// it goes straight into a request path.
//
// Alphanumeric rather than strictly hex, which is what Bugsnag's ids look like
// today: the property that matters is that an id carries no path or query
// metacharacter, and pinning the alphabet tighter than that would break the day
// an id gains a prefix. The length is not pinned to 24 for the same reason.
func checkID(kind, value string) (string, error) {
	if err := checkToken(kind+" id", value, maxIDLength, ""); err != nil {
		return "", err
	}
	return value, nil
}

// checkSlug keeps an org or project slug to what Bugsnag allows, for the same
// reason as checkID: it reaches a request path.
func checkSlug(kind, value string) error {
	return checkToken(kind, value, maxSlugLength, "-_.")
}

// checkToken is the shared alphanumeric check: bounded length, and nothing beyond
// the alphabet plus whatever extra runes the caller allows.
func checkToken(what, value string, maxLen int, extra string) error {
	reject := fmt.Errorf("%q is not a valid %s", value, what)

	if value == "" || len(value) > maxLen {
		return reject
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case strings.ContainsRune(extra, r):
		default:
			return reject
		}
	}
	return nil
}

const (
	maxIDLength   = 64
	maxSlugLength = 128
)
