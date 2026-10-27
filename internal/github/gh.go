// Package github reads and acts on the GitHub of a repository on this
// node through the GitHub CLI, gh, with the login the node's user gave it:
// the pull requests, issues and Actions runs, a pull request's change, and
// the comments, reviews and runs a front-end asks for.
package github

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/gitstat"
)

// ErrNoGH is a request of a node that has no gh.
var ErrNoGH = errors.New("the GitHub CLI (gh) is not installed on this node: install it, then run gh auth login there")

// found is where gh was found, once it has been.
var found struct {
	sync.Mutex
	path string
}

// Path is where gh is: on the PATH, or where installers put it. Once
// found it is not looked for again.
func Path() (string, error) {
	found.Lock()
	defer found.Unlock()
	if found.path == "" {
		path, err := agent.Find("gh")
		if err != nil {
			return "", ErrNoGH
		}
		found.path = path
	}
	return found.path, nil
}

// gh runs gh in root and returns what it wrote to stdout; a failure's error
// says what it wrote to stderr. gh never asks, as there is no one to
// answer. A repository gh would not find itself is named to it.
func gh(ctx context.Context, root string, stdin []byte, args ...string) ([]byte, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1",
		"NO_COLOR=1", "CLICOLOR=0", "GIT_TERMINAL_PROMPT=0", "GH_PAGER=", "PAGER=cat")
	if repo := repoOf(root); repo != "" {
		cmd.Env = append(cmd.Env, "GH_REPO="+repo)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			msg, _, _ = strings.Cut(msg, "\n\n") // what went wrong, without the usage after it
			return out, errors.New(msg)
		}
		return out, err
	}
	return out, nil
}

// repoOf is the GitHub repository of the work tree at root, as GH_REPO
// takes it, when gh would not find it: gh reads the remotes' fetch URLs,
// and a remote can fetch from another host and push to GitHub as well.
// It is "" when a fetch URL is GitHub's, or no URL is.
func repoOf(root string) string {
	var pushed string
	for _, u := range gitstat.RemoteURLs(root) {
		repo := gitHubRepo(u.URL)
		switch {
		case repo == "":
		case !u.Push:
			return ""
		case pushed == "":
			pushed = repo
		}
	}
	if pushed == "" {
		return ""
	}
	return "github.com/" + pushed
}

// gitHubRepo is owner/name of a github.com remote URL, "" for another's.
func gitHubRepo(url string) string {
	host, path := gitstat.RepoPath(url)
	if host != "github.com" || strings.Count(path, "/") != 1 {
		return ""
	}
	return path
}
