// Package pathscope says which folders hold which: the rule the window
// groups its sidebar's projects by and the daemon scopes its agents by, so
// the two agree. Paths are a node's, written with either separator.
package pathscope

import "strings"

// Within reports whether dir is root or a folder inside it.
func Within(dir, root string) bool {
	if dir == root {
		return true
	}
	if root == "" {
		return false
	}
	sep := "/"
	if strings.Contains(root, `\`) && !strings.Contains(root, "/") {
		sep = `\`
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(root, sep)+sep)
}

// Covers reports whether a project's folder loc holds a session working
// in dir, in the repository whose main work tree is repo ("" outside one).
// A folder holds the folders inside it, but not a repository of its own
// inside it, which a home folder or a workspace folder would otherwise
// take whole; a repository's folder holds its worktrees, wherever they
// are.
func Covers(loc, dir, repo string) bool {
	if repo == "" {
		return Within(dir, loc)
	}
	return repo == loc || Within(dir, loc) && Within(loc, repo)
}
