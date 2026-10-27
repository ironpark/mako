package gitstat

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Branch is the branch checked out in the work tree dir is in, read from
// its HEAD file alone so it is cheap to ask for often: the short hash when
// HEAD is detached, "" outside a repository.
func Branch(dir string) string {
	gitDir, _ := findGitDir(dir)
	return branchOf(gitDir)
}

// Head is the branch checked out where dir is, as Branch says, its
// repository, as Repo says, and the owner of that repository's origin
// remote, "github.com/ironpark" for git@github.com:ironpark/mako.git,
// finding the repository once. All are "" outside a repository.
func Head(dir string) (branch, repo, owner string) {
	gitDir, top := findGitDir(dir)
	if gitDir == "" {
		return "", "", ""
	}
	common := commonDir(gitDir)
	return branchOf(gitDir), repoOf(gitDir, common, top), ownerOf(common)
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
	gitDir, top := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	return repoOf(gitDir, commonDir(gitDir), top)
}

// ownerOf is the user or organization whose repository the origin remote
// in the config of the git directory common is, or of its first remote by
// name without an origin: "github.com/ironpark" for
// git@github.com:ironpark/mako.git. "" without a remote.
func ownerOf(common string) string {
	f, err := os.Open(filepath.Join(common, "config"))
	if err != nil {
		return ""
	}
	defer f.Close()
	cfg, err := config.ReadConfig(f)
	if err != nil {
		return ""
	}
	remote := cfg.Remotes["origin"]
	if remote == nil {
		for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
			remote = cfg.Remotes[name]
			break
		}
	}
	if remote == nil || len(remote.URLs) == 0 {
		return ""
	}
	return urlOwner(remote.URLs[0])
}

// urlOwner is the user or organization a remote URL's repository is
// under, with the host that serves it: "github.com/ironpark" for an
// scp-like git@github.com:ironpark/mako.git, as for a URL's on any host,
// GitHub's, a Forgejo's or GitLab's, and the folder alone for a local
// path's. The host tells apart the same name on two services.
func urlOwner(url string) string {
	if url == "" {
		return "" // which go-git takes for the folder it runs in
	}
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return ""
	}
	parts := strings.FieldsFunc(ep.Path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) < 2 {
		return ""
	}
	owner := parts[len(parts)-2]
	if ep.Host == "" {
		return owner
	}
	return strings.ToLower(ep.Host) + "/" + owner
}

// repoOf is Repo for the git directory gitDir of the work tree at top,
// whose common git directory is common.
func repoOf(gitDir, common, top string) string {
	// One added to a bare repository, which has no work tree of its own,
	// stays its own.
	if common != gitDir && filepath.Base(common) == ".git" {
		top = filepath.Dir(common)
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		return real
	}
	return top
}

// commonDir is the git directory a linked work tree's gitDir shares with
// the repository it was added to, or gitDir itself.
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common)
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
