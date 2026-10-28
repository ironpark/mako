package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// RemoteURL is a URL of a remote of a repository: one it fetches from, or,
// with Push, one it only pushes to.
type RemoteURL struct {
	URL  string
	Push bool
}

// readConfig is the config of the repository dir is in, nil outside one.
func readConfig(dir string) *config.Config {
	gitDir, _ := findGitDir(dir)
	if gitDir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(commonDir(gitDir), "config"))
	if err != nil {
		return nil
	}
	defer f.Close()
	cfg, err := config.ReadConfig(f)
	if err != nil {
		return nil
	}
	return cfg
}

// FetchURL is the URL the repository dir is in fetches from: its origin's,
// else its first remote's by name; "" without one.
func FetchURL(dir string) string {
	if cfg := readConfig(dir); cfg != nil {
		return fetchURL(cfg)
	}
	return ""
}

// RemoteURLs are the URLs of the remotes of the repository dir is in, read
// from its config: each remote's fetch URLs, then its push URLs.
func RemoteURLs(dir string) []RemoteURL {
	cfg := readConfig(dir)
	if cfg == nil {
		return nil
	}
	var out []RemoteURL
	for _, s := range cfg.Raw.Section("remote").Subsections {
		for _, u := range s.Options.GetAll("url") {
			out = append(out, RemoteURL{URL: u})
		}
		for _, u := range s.Options.GetAll("pushurl") {
			out = append(out, RemoteURL{URL: u, Push: true})
		}
	}
	return out
}

// RepoPath is the host of a remote URL and the path of the repository on
// it, without .git: "github.com" and "ironpark/mako" for
// git@github.com:ironpark/mako.git. Both are "" for what is not a URL of a
// host's.
func RepoPath(url string) (host, path string) {
	if url == "" {
		return "", ""
	}
	ep, err := transport.NewEndpoint(url)
	if err != nil || ep.Host == "" {
		return "", ""
	}
	return strings.ToLower(ep.Host), strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git")
}

// Git runs git in root for what it does, returning all it printed, as the
// operations here do: never asking for a password nor opening an editor.
func Git(ctx context.Context, root string, args ...string) (string, error) {
	return run(ctx, root, nil, args...)
}
