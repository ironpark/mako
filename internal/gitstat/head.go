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
	gitDir, _ := findGitDir(dir)
	return branchOf(gitDir)
}

// Head is Branch and Repo of dir together, finding its repository once.
func Head(dir string) (branch, repo string) {
	gitDir, top := findGitDir(dir)
	return branchOf(gitDir), repoOf(gitDir, top)
}

// branchOf is Branch for the git directory gitDir, "" for none.
func branchOf(gitDir string) string {
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

// Repo is the main work tree of the repository dir is in: the top of its
// work tree, or for a linked work tree, the one it was added to, so that a
// repository's worktrees and folders all name the same; "" outside a
// repository. Links in it are resolved, as git resolves the paths it
// records for a linked work tree. Like Branch it reads files alone.
func Repo(dir string) string {
	return repoOf(findGitDir(dir))
}

// repoOf is Repo for the git directory gitDir of the work tree at top.
func repoOf(gitDir, top string) string {
	if gitDir == "" {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		// One added to a bare repository, which has no work tree of its
		// own, stays its own.
		if common = filepath.Clean(common); filepath.Base(common) == ".git" {
			top = filepath.Dir(common)
		}
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		return real
	}
	return top
}

// findGitDir is the git directory of the work tree dir is in, a .git
// folder or where a .git file, as a linked work tree or submodule has,
// points, and the top of that work tree.
func findGitDir(dir string) (gitDir, top string) {
	for dir != "" {
		p := filepath.Join(dir, ".git")
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				return p, dir
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", ""
			}
			to, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if !ok {
				return "", ""
			}
			if !filepath.IsAbs(to) {
				to = filepath.Join(dir, to)
			}
			return to, dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", ""
		}
		dir = up
	}
	return "", ""
}
