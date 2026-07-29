package cli

import (
	"context"
	"os/exec"
	"strings"
)

// execGit reads remotes by shelling out to git.
//
// Reading .git/config directly would miss includes, conditional includes and
// insteadOf rewrites, all of which are in normal use. Asking git is the only way
// to get the URL git itself would use.
type execGit struct{}

// Remotes returns remote name to URL for the repository containing dir.
//
// Not being in a repository is not an error: it means autodetect has nothing to
// go on, and the caller says so in terms of what to do next.
func (execGit) Remotes(ctx context.Context, dir string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "-v")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	remotes := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// `git remote -v` lists fetch and push separately; the fetch URL is the
		// one that identifies the repository.
		if len(fields) >= 3 && fields[2] != "(fetch)" {
			continue
		}
		if _, seen := remotes[fields[0]]; !seen {
			remotes[fields[0]] = fields[1]
		}
	}
	return remotes, nil
}
