package gitstat

import (
	"os"
	"path/filepath"
	"strings"
)

// Branch is the branch checked out in the work tree dir is in, read from
// its HEAD file alone so it is cheap to ask for often: the short hash when
// HEAD is detached, "" outside a repository.
func Branch(dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(head, "ref: "); ok {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	return head[:min(len(head), 7)]
}

// findGitDir is the git directory of the work tree dir is in: a .git
// folder, or where a .git file, as a linked work tree or submodule has,
// points.
func findGitDir(dir string) string {
	for dir != "" {
		p := filepath.Join(dir, ".git")
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				return p
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return ""
			}
			to, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if !ok {
				return ""
			}
			if !filepath.IsAbs(to) {
				to = filepath.Join(dir, to)
			}
			return to
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
	return ""
}
