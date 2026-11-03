package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/zalando/go-keyring"
	"go.yaml.in/yaml/v3"
)

// The node's GitHub login is found where gh keeps it, as gh finds it, so a
// node whose user ran gh auth login once needs no more: GH_TOKEN, else
// GITHUB_TOKEN; else the token gh's hosts.yml holds, else the one gh keeps
// in the system's keyring for the user hosts.yml names. Else it is the
// password git keeps for github.com, as a credential helper or manager
// was given one.

// errNoLogin is a request of a node with no GitHub login.
var errNoLogin = errors.New("this node has no GitHub login: run gh auth login there, set GH_TOKEN, or keep a token for github.com in git's credentials")

// How long a found login is kept; none is looked for again sooner, as
// one may be given at any time.
const (
	loginTTL   = 10 * time.Minute
	noLoginTTL = 30 * time.Second
)

var logins = &keptMap{m: map[string]kept{}}

// githubToken is the node's GitHub login.
func githubToken(ctx context.Context) (string, error) {
	tok := logins.get(githubHost, func() string { return findToken(ctx) }, func(t string) time.Duration {
		if t == "" {
			return noLoginTTL
		}
		return loginTTL
	})
	if tok == "" {
		return "", errNoLogin
	}
	return tok, nil
}

// forgetGitHubToken drops the login kept, as GitHub refused it.
func forgetGitHubToken() {
	logins.mu.Lock()
	delete(logins.m, githubHost)
	logins.mu.Unlock()
}

// findToken looks for the login where token says.
func findToken(ctx context.Context) string {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := os.Getenv(env); t != "" {
			return t
		}
	}
	user, tok := ghHosts()
	if tok != "" {
		return tok
	}
	if tok = ghKeyring(user); tok != "" {
		return tok
	}
	return token(ctx, githubHost)
}

// ghConfigDir is where gh keeps its config: GH_CONFIG_DIR, else gh under
// XDG_CONFIG_HOME, else under AppData on Windows, else ~/.config/gh.
func ghConfigDir() string {
	if d := os.Getenv("GH_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh")
	}
	if d := os.Getenv("AppData"); runtime.GOOS == "windows" && d != "" {
		return filepath.Join(d, "GitHub CLI")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gh")
}

// ghHost is a host in gh's hosts.yml: the user active on it, and, when gh
// keeps logins in the file, not the keyring, their token.
type ghHost struct {
	User  string `yaml:"user"`
	Token string `yaml:"oauth_token"`
	Users map[string]struct {
		Token string `yaml:"oauth_token"`
	} `yaml:"users"`
}

// ghHosts is the user gh has active on github.com, and their token when
// hosts.yml holds it.
func ghHosts() (user, tok string) {
	dir := ghConfigDir()
	if dir == "" {
		return "", ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return "", ""
	}
	var hosts map[string]ghHost
	if yaml.Unmarshal(b, &hosts) != nil {
		return "", ""
	}
	h := hosts[githubHost]
	if h.Token != "" {
		return h.User, h.Token
	}
	return h.User, h.Users[h.User].Token
}

// keyringTimeout bounds a keyring's answer, which may wait on its being
// unlocked, as gh's does.
const keyringTimeout = 3 * time.Second

// ghKeyring is the token gh keeps in the system's keyring for github.com:
// the active user's, which gh keeps under no user too.
func ghKeyring(user string) string {
	got := make(chan string, 1)
	go func() {
		users := []string{""}
		if user != "" {
			users = append(users, user)
		}
		for _, u := range users {
			if t, err := keyring.Get("gh:"+githubHost, u); err == nil && t != "" {
				got <- t
				return
			}
		}
		got <- ""
	}()
	select {
	case t := <-got:
		return t
	case <-time.After(keyringTimeout):
		return ""
	}
}
