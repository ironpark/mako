package registry

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/ironpark/mako/internal/plugin"
)

// A registry is a git repository, added by its URL. A node reads only
// its index, the file IndexName at the root of the default branch,
// through the forge's raw files, by HTTPS: no git client and no clone,
// so that what is read is one file, bounded in size as it is read, and
// a repository's history or size cannot weigh on the node.
//
//   - https://github.com/<owner>/<repo> is read at
//     https://raw.githubusercontent.com/<owner>/<repo>/HEAD/mako-registry.json
//   - https://gitlab.com/<group>/.../<repo> at
//     https://gitlab.com/<group>/.../<repo>/-/raw/HEAD/mako-registry.json
//   - any other https URL whose path ends in .json is the index itself:
//     a raw file of any other forge, or of a site.
//
// A URL is https, on the default port, with a host name, not an IP
// address, and no user, query or fragment: a path on this machine, a
// file: URL, ssh, git or any other scheme is refused.

// Normalize checks a registry's URL as the user gave it and returns the
// form the node keeps it by, and its index file's URL. loopback lets
// http on this machine's loopback through, for tests alone.
func Normalize(raw string, loopback bool) (canon, index string, err error) {
	raw = strings.TrimSpace(raw)
	if err := CheckURL(raw, loopback); err != nil {
		return "", "", err
	}
	u, _ := url.Parse(raw)
	if u.RawQuery != "" || u.ForceQuery {
		return "", "", errors.New("a registry's URL has no query")
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	u.RawPath = ""
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	canon = u.String()
	switch {
	case strings.HasSuffix(u.Path, ".json"):
		return canon, canon, nil
	case u.Host == "github.com" && len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return canon, "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/HEAD/" + IndexName, nil
	case u.Host == "gitlab.com" && len(parts) >= 2 && !strings.Contains(u.Path, "/-/"):
		return canon, canon + "/-/raw/HEAD/" + IndexName, nil
	}
	return "", "", fmt.Errorf("%s is not a GitHub or GitLab repository's URL, nor the https URL of a registry's %s", short(raw), IndexName)
}

// CheckURL refuses a URL the node does not fetch: not https, with a
// user, a fragment, an IP address or another port; or, with loopback, an
// http URL of this machine's loopback, for tests.
func CheckURL(raw string, loopback bool) error {
	if len(raw) > maxURL {
		return errors.New("the URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not an https URL", short(raw))
	}
	host := u.Hostname()
	local := loopback && isLoopback(host)
	switch {
	case u.Scheme != "https" && !(u.Scheme == "http" && local):
		return fmt.Errorf("%q is not an https URL (a path on this machine, file:, ssh, git and other schemes are refused)", short(raw))
	case u.Opaque != "" || host == "":
		return fmt.Errorf("%q names no host", short(raw))
	case u.User != nil:
		return fmt.Errorf("%q holds a user or password", short(raw))
	case u.Fragment != "":
		return fmt.Errorf("%q has a fragment", short(raw))
	case local:
		return nil
	case net.ParseIP(host) != nil || strings.HasPrefix(u.Host, "["):
		return fmt.Errorf("%q names an IP address; a registry and its packages are reached by host name", short(raw))
	case !plugin.ValidHost(strings.ToLower(host)):
		return fmt.Errorf("%q does not name a host on the internet", short(raw))
	case u.Port() != "" && u.Port() != "443":
		return fmt.Errorf("%q names a port; only https's own is reached", short(raw))
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
