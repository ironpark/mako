package forge

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// GitHub is read and acted on through its API, REST and GraphQL, as its
// CLI, gh, does, with the login the node's user gave gh, or a token of
// theirs: see token. What gh asks of it, and how it finds a repository's
// and a branch's, is followed here, from github.com/cli/cli; gh itself is
// not needed.

const (
	githubHost    = "github.com"
	githubAPI     = "https://api.github.com"
	githubGraphQL = githubAPI + "/graphql"
)

// github is a repository on GitHub.
type github struct {
	owner, repo string
}

func (*github) name() string { return "github" }

func (*github) ops() []string {
	return []string{protocol.ForgeOpCheckout, protocol.ForgeOpFetchPR, protocol.ForgeOpComment, protocol.ForgeOpReview,
		protocol.ForgeOpRerun, protocol.ForgeOpCancel, protocol.ForgeOpFailedLogs}
}

// full is the repository as owner/name.
func (g *github) full() string { return g.owner + "/" + g.repo }

// path is the path of the repository's REST API, with what follows it.
func (g *github) path(rest string) string {
	return "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + rest
}

// gitHubOf is the GitHub repository of the work tree at root, found as gh
// finds it: the remote gh repo set-default chose, else the first of the
// remotes that fetch from GitHub, upstream, github and origin before the
// rest; else the GitHub repository a remote pushes to. It is nil for none.
func gitHubOf(root string) *github {
	urls := gitstat.RemoteURLs(root)
	var fetched []gitstat.RemoteURL
	for _, u := range urls {
		if !u.Push && gitHubRepo(u.URL) != "" {
			fetched = append(fetched, u)
		}
	}
	for _, u := range fetched {
		switch resolved := gitstat.Config(root, "remote", u.Remote, "gh-resolved"); {
		case resolved == "base":
			return repoNamed(gitHubRepo(u.URL))
		case resolved != "":
			if _, _, ok := ownerRepo(resolved); ok {
				return repoNamed(resolved)
			}
		}
	}
	if len(fetched) > 0 {
		rank := func(u gitstat.RemoteURL) int {
			if i := slices.Index([]string{"upstream", "github", "origin"}, u.Remote); i >= 0 {
				return i
			}
			return 3
		}
		slices.SortStableFunc(fetched, func(a, b gitstat.RemoteURL) int { return cmp.Compare(rank(a), rank(b)) })
		return repoNamed(gitHubRepo(fetched[0].URL))
	}
	for _, u := range urls {
		if repo := gitHubRepo(u.URL); repo != "" {
			return repoNamed(repo)
		}
	}
	return nil
}

// repoNamed is the GitHub repository owner/name.
func repoNamed(full string) *github {
	owner, repo, _ := ownerRepo(full)
	return &github{owner: owner, repo: repo}
}

// gitHubRepo is owner/name of a github.com remote URL, "" for another's.
func gitHubRepo(url string) string {
	host, path := gitstat.RepoPath(url)
	if _, _, ok := ownerRepo(path); host != githubHost || !ok {
		return ""
	}
	return path
}

// remoteOf is the remote of the work tree at root that fetches from the
// GitHub repository full, owner/name, and its URL: "" and GitHub's own
// URL of it when no remote does.
func remoteOf(root, full string) (remote, fetch string) {
	for _, u := range gitstat.RemoteURLs(root) {
		if !u.Push && strings.EqualFold(gitHubRepo(u.URL), full) {
			return u.Remote, u.URL
		}
	}
	for _, u := range gitstat.RemoteURLs(root) {
		if strings.EqualFold(gitHubRepo(u.URL), full) {
			return "", u.URL
		}
	}
	return "", "https://" + githubHost + "/" + full + ".git"
}

// The API's.

// rest makes a request of the REST API, at path under it, its body JSON,
// and reads the reply into v.
func (g *github) rest(ctx context.Context, method, path string, body, v any) error {
	out, err := githubCall(ctx, method, githubAPI+path, "", body)
	if err != nil || v == nil || len(out) == 0 {
		return err
	}
	return json.Unmarshal(out, v)
}

// graphql asks the GraphQL API query, with vars, and reads its data into
// v. An error of the API's is one, though some data came with it.
func (g *github) graphql(ctx context.Context, query string, vars map[string]any, v any) error {
	out, err := githubCall(ctx, http.MethodPost, githubGraphQL, "", map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return fmt.Errorf("cannot read what GitHub answered: %w", err)
	}
	if len(reply.Errors) > 0 {
		msgs := make([]string, len(reply.Errors))
		for i, e := range reply.Errors {
			msgs[i] = e.Message
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return json.Unmarshal(reply.Data, v)
}

// githubCall makes a request of GitHub's API with the node's login, its
// body JSON, and returns the reply's, as accept asks for it, JSON by
// default. A redirect, as to a log, is followed without the login.
func githubCall(ctx context.Context, method, u, accept string, body any) ([]byte, error) {
	tok, err := githubToken(ctx)
	if err != nil {
		return nil, err
	}
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, in)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", cmp.Or(accept, "application/vnd.github+json"))
	req.Header.Set("Authorization", "token "+tok)
	req.Header.Set("User-Agent", "mako")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := hostClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			forgetGitHubToken()
		}
		return nil, githubError(resp, out)
	}
	return out, nil
}

// githubError is what GitHub said of a request it refused.
func githubError(resp *http.Response, out []byte) error {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Field   string `json:"field"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(out, &e)
	msg := cmp.Or(e.Message, strings.TrimSpace(string(out)), resp.Status)
	var more []string
	for _, x := range e.Errors {
		switch {
		case x.Message != "":
			more = append(more, x.Message)
		case x.Field != "":
			more = append(more, x.Field+" "+x.Code)
		}
	}
	if len(more) > 0 {
		msg += ": " + strings.Join(more, "; ")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("GitHub refused this node's login (%s): %w", msg, errNoLogin)
	}
	return fmt.Errorf("GitHub answered %s: %s", resp.Status, msg)
}
