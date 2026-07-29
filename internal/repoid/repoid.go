// Package repoid turns a git remote into a stable identity for a repository.
//
// The identity is the config key a project resolution is cached under, so it has
// to be the same every time for the same repository, and different for a fork.
// The remotes in one checkout directory are a mix of forms: some end in .git and
// some do not, some are SSH and some HTTPS. All of those name the same
// repository and must produce one key.
package repoid

import (
	"net/url"
	"strings"
)

// Identity is a canonical repository identity, such as
// "github.com/example-org/example-api".
type Identity string

// Preference is the order remotes are considered in: the remote you push to
// first, then the upstream of a fork, and otherwise whatever single remote
// exists.
var Preference = []string{"origin", "upstream"}

// FromRemotes picks a remote and canonicalises it.
//
// A fork keeps its own identity rather than collapsing onto its upstream: two
// checkouts of different forks are usually different projects, and quietly
// treating them as one would report the wrong project's errors.
func FromRemotes(remotes map[string]string) (Identity, string, bool) {
	for _, name := range Preference {
		if raw, ok := remotes[name]; ok {
			if id, ok := Canonical(raw); ok {
				return id, name, true
			}
		}
	}

	// Exactly one remote under some other name is unambiguous.
	if len(remotes) == 1 {
		for name, raw := range remotes {
			if id, ok := Canonical(raw); ok {
				return id, name, true
			}
		}
	}

	return "", "", false
}

// Canonical reduces a remote URL to lowercased host/path, with any port, .git
// suffix, credentials and trailing slash removed.
func Canonical(remote string) (Identity, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}

	host, path, ok := split(remote)
	if !ok {
		return "", false
	}

	host = strings.ToLower(host)
	// The port is not part of the repository's identity: the same repository
	// reached on a non-default port is the same repository.
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.ToLower(path)

	if host == "" || path == "" {
		return "", false
	}
	return Identity(host + "/" + path), true
}

// RepoName is the last path segment, which is what project autodetect searches
// for.
func (i Identity) RepoName() string {
	s := string(i)
	if idx := strings.LastIndexByte(s, '/'); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// split separates a remote into host and path, handling the forms git remotes
// actually take.
func split(remote string) (host, path string, ok bool) {
	if !strings.Contains(remote, "://") {
		// scp-like syntax: [user@]github.com:example-org/example-api.git. Git reads
		// a colon before the first slash as this form, and the user half is
		// optional — so the colon has to be found before any slash rather than
		// after an "@", or a userless remote loses its owner to the port strip
		// below and collides with any repo of that name in another organisation.
		rest := remote
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			rest = rest[at+1:]
		}
		h, p, found := strings.Cut(rest, ":")
		if !found || strings.Contains(h, "/") {
			// Anything else without a scheme is a local path, which is not a
			// shareable identity.
			return "", "", false
		}
		return h, p, true
	}

	u, err := url.Parse(remote)
	if err != nil {
		return "", "", false
	}
	if u.Scheme == "file" {
		return "", "", false
	}
	return u.Host, u.Path, u.Host != "" && u.Path != ""
}
