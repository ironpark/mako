package gitstat

import (
	"bytes"
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	format "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// RemoteURL is a URL of a remote of a repository: one it fetches from, or,
// with Push, one it only pushes to.
type RemoteURL struct {
	Remote string // the remote's name
	URL    string
	Push   bool
}

// readConfig is the config of the repository dir is in, nil outside one.
func readConfig(dir string) *config.Config {
	gitDir, _ := findGitDir(dir)
	if gitDir == "" {
		return nil
	}
	return configAt(commonDir(gitDir))
}

// configAt is the config of the git directory common, nil without one.
// A branch whose upstream go-git would refuse, as a pull request's,
// refs/pull/n/head, that gh pr checkout sets, is read without it, not
// the config refused whole.
func configAt(common string) *config.Config {
	b, err := os.ReadFile(filepath.Join(common, "config"))
	if err != nil {
		return nil
	}
	return parseConfigBytes(b)
}

func parseConfigBytes(b []byte) *config.Config {
	cfg := config.NewConfig()
	if cfg.Unmarshal(b) == nil {
		return cfg
	}
	raw, stripped := format.New(), format.New()
	if format.NewDecoder(bytes.NewReader(b)).Decode(raw) != nil || format.NewDecoder(bytes.NewReader(b)).Decode(stripped) != nil {
		return nil
	}
	for _, s := range stripped.Section("branch").Subsections {
		if m := s.Option("merge"); m != "" && !plumbing.ReferenceName(m).IsBranch() {
			s.RemoveOption("merge")
		}
		if r := s.Option("rebase"); r != "" && r != "true" && r != "false" && r != "interactive" {
			s.RemoveOption("rebase")
		}
	}
	for _, s := range stripped.Section("remote").Subsections {
		fetches := s.OptionAll("fetch")
		if len(fetches) == 0 {
			continue
		}
		valid := make([]string, 0, len(fetches))
		for _, f := range fetches {
			if config.RefSpec(f).Validate() == nil {
				valid = append(valid, f)
			}
		}
		if len(valid) == len(fetches) {
			continue
		}
		if len(valid) == 0 {
			s.RemoveOption("fetch")
		} else {
			s.SetOption("fetch", valid...)
		}
	}
	var buf bytes.Buffer
	if format.NewEncoder(&buf).Encode(stripped) != nil {
		return nil
	}
	cfg = config.NewConfig()
	if cfg.Unmarshal(buf.Bytes()) != nil {
		return nil
	}
	cfg.Raw = raw // as written, for Config
	return cfg
}

// ConfigRemote parses raw .git/config bytes and returns the repository's
// owner ("host/owner") and remote ("host/owner/repo") from its fetch URL,
// or ("", "") when no remote URL is present.
func ConfigRemote(data []byte) (owner, remote string) {
	cfg := parseConfigBytes(data)
	if cfg == nil {
		return "", ""
	}
	url := fetchURL(cfg)
	return urlOwner(url), urlRemote(url)
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
			out = append(out, RemoteURL{Remote: s.Name, URL: u})
		}
		for _, u := range s.Options.GetAll("pushurl") {
			out = append(out, RemoteURL{Remote: s.Name, URL: u, Push: true})
		}
	}
	return out
}

// Config is the value of key in a subsection of a section of the config of
// the repository dir is in, as git config reads section.subsection.key:
// "" when it is unset.
func Config(dir, section, subsection, key string) string {
	cfg := readConfig(dir)
	if cfg == nil {
		return ""
	}
	return cfg.Raw.Section(section).Subsection(subsection).Option(key)
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

// HTTPSHost returns the normalized host (with non-default port, if any) of an
// HTTP or HTTPS git remote URL: "github.com" for https://github.com/a/b.git
// and https://github.com:443/a/b.git, "git.example.com:3000" for
// https://git.example.com:3000/a/b.git, and "" for SSH, file, or invalid URLs.
func HTTPSHost(rawURL string) string {
	_, host := parseHTTPOrigin(rawURL)
	return host
}

// HTTPSOrigin returns the normalized "scheme://host[:port]" origin of an HTTP
// or HTTPS git remote URL, omitting default ports (:443 for https, :80 for
// http). Returns "" for non-HTTP(S) or invalid URLs.
func HTTPSOrigin(rawURL string) string {
	scheme, host := parseHTTPOrigin(rawURL)
	if scheme == "" || host == "" {
		return ""
	}
	return scheme + "://" + host
}

// IsHTTPURL reports whether rawURL is an HTTP or HTTPS git remote URL.
func IsHTTPURL(rawURL string) bool {
	return HTTPSOrigin(rawURL) != ""
}

func parseHTTPOrigin(rawURL string) (scheme, host string) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", ""
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", ""
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", ""
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme, net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return scheme, "[" + hostname + "]"
	}
	return scheme, hostname
}

// Git runs git in root for what it does, returning all it printed, as the
// operations here do: never asking for a password nor opening an editor.
func Git(ctx context.Context, root string, args ...string) (string, error) {
	return run(ctx, root, nil, args...)
}
