// Package projectresolve matches API projects against a repository name.
//
// Matching is exact and case-insensitive: the repository name has to equal a
// project's slug or display name. That is enough in practice — measured against
// the live organization, the overwhelming majority of checked-out repositories
// match a project this way, and the rest are repositories with no project or a
// deliberate rename, which `project link` exists to record.
//
// Exact matching cannot cross-match, which is why there is no anti-substring
// protection here: a repository called "widget" can only match a project
// "widget", never "widget-replica".
package projectresolve

import "strings"

// Project is the subset of a project needed to match it.
type Project struct {
	ID      string
	Name    string
	Slug    string
	HTMLURL string
}

// Match returns the single project whose slug or name equals repoName,
// case-insensitively, and reports whether exactly one project did.
//
// More than one match is not a winner: picking either would be a guess, and the
// caller asks the user rather than guessing.
func Match(repoName string, projects []Project) (Project, bool) {
	target := strings.ToLower(strings.TrimSpace(repoName))
	if target == "" {
		return Project{}, false
	}

	var found Project
	matches := 0
	for _, p := range projects {
		if equalFold(p.Slug, target) || equalFold(p.Name, target) {
			found = p
			matches++
		}
	}
	if matches == 1 {
		return found, true
	}
	return Project{}, false
}

func equalFold(value, lowerTarget string) bool {
	value = strings.TrimSpace(value)
	return value != "" && strings.ToLower(value) == lowerTarget
}
