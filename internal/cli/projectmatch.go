package cli

import (
	"strings"

	"github.com/geckoboard/bugsnag-cli/internal/config"
)

// matchProject returns the single project whose slug or name equals repoName,
// case-insensitively, and reports whether exactly one project did.
//
// Matching is exact: the repository name has to equal a project's slug or display
// name. That is enough in practice — measured against the live organization, the
// overwhelming majority of checked-out repositories match a project this way, and
// the rest are repositories with no project or a deliberate rename, which
// `project link` exists to record.
//
// Exact matching cannot cross-match, which is why there is no anti-substring
// protection here: a repository called "widget" can only match a project
// "widget", never "widget-replica".
//
// More than one match is not a winner: picking either would be a guess, and the
// caller asks the user rather than guessing.
func matchProject(repoName string, projects []config.Project) (config.Project, bool) {
	target := strings.TrimSpace(repoName)
	if target == "" {
		return config.Project{}, false
	}

	var found config.Project
	matches := 0
	for _, p := range projects {
		if equalsFold(p.Slug, target) || equalsFold(p.Name, target) {
			found = p
			matches++
		}
	}
	if matches == 1 {
		return found, true
	}
	return config.Project{}, false
}

func equalsFold(value, target string) bool {
	return strings.EqualFold(strings.TrimSpace(value), target)
}
