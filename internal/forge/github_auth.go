package forge

import (
	"context"
	"errors"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
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
	if cred, ok := gitstat.GetHTTPSCredential("https://" + githubHost); ok && cred.Password != "" {
		return cred.Password, nil
	}
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
	gitstat.ResetGitHubTokenCache()
}

// findToken looks for the login where token says.
func findToken(ctx context.Context) string {
	if tok := gitstat.FindGitHubToken(); tok != "" {
		return tok
	}
	return token(ctx, githubHost)
}
