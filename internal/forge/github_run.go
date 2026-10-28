package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// run acts through gh, which prints what it did.
func (github) run(ctx context.Context, r *protocol.ForgeRun) (string, error) {
	root := r.GetRoot()
	n := strconv.FormatInt(r.GetNumber(), 10)
	switch r.GetOp() {
	case protocol.ForgeOpCheckout:
		return printed(gh(ctx, root, nil, "pr", "checkout", n))
	case protocol.ForgeOpFetchPR:
		return ghFetchPR(ctx, root, r.GetNumber())
	case protocol.ForgeOpComment:
		return printed(gh(ctx, root, []byte(r.GetBody()), r.GetKind(), "comment", n, "--body-file", "-"))
	case protocol.ForgeOpReview:
		flag := map[string]string{"approve": "--approve", "request_changes": "--request-changes", "comment": "--comment"}[r.GetReview()]
		return printed(gh(ctx, root, []byte(r.GetBody()), "pr", "review", n, flag, "--body-file", "-"))
	case protocol.ForgeOpRerun:
		args := []string{"run", "rerun", n}
		if r.GetFailed() {
			args = append(args, "--failed")
		}
		return printed(gh(ctx, root, nil, args...))
	case protocol.ForgeOpCancel:
		return printed(gh(ctx, root, nil, "run", "cancel", n))
	case protocol.ForgeOpFailedLogs:
		out, err := gh(ctx, root, nil, "run", "view", n, "--log-failed")
		if err != nil {
			return "", err
		}
		if len(out) > maxLog {
			out = out[len(out)-maxLog:]
			if i := strings.IndexByte(string(out), '\n'); i >= 0 {
				out = out[i+1:]
			}
		}
		return string(out), nil
	}
	return "", fmt.Errorf("unknown forge operation %q", r.GetOp())
}

// printed is what gh printed, trimmed.
func printed(out []byte, err error) (string, error) {
	return strings.TrimSpace(string(out)), err
}

func (github) openPR(ctx context.Context, root, branch, base, title, body string, draft bool) (string, error) {
	if _, err := ghPath(); err != nil {
		return "", err // before the push, which would be left without its pull request
	}
	if pushed, err := push(ctx, root); err != nil {
		return pushed, err
	}
	args := []string{"pr", "create", "--head", branch}
	if base != "" {
		args = append(args, "--base", base)
	}
	if draft {
		args = append(args, "--draft")
	}
	var stdin []byte
	if title = strings.TrimSpace(title); title != "" {
		args = append(args, "--title", title, "--body-file", "-")
		stdin = []byte(body)
	} else {
		args = append(args, "--fill")
	}
	return printed(gh(ctx, root, stdin, args...))
}

// fetchPR fetches a pull request's head from the repository it is of, and
// replies with its commit, for a worktree to start at.
func ghFetchPR(ctx context.Context, root string, n int64) (string, error) {
	out, err := gh(ctx, root, nil, "pr", "view", strconv.FormatInt(n, 10), "--json", "headRefOid,url")
	if err != nil {
		return "", err
	}
	var pr struct {
		HeadRefOid string `json:"headRefOid"`
		URL        string `json:"url"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.HeadRefOid == "" {
		return "", fmt.Errorf("cannot read pull request #%d's head", n)
	}
	if _, err := gitstat.Git(ctx, root, "cat-file", "-e", pr.HeadRefOid+"^{commit}"); err == nil {
		return pr.HeadRefOid, nil // fetched already
	}
	_, path := gitstat.RepoPath(pr.URL) // owner/name/pull/n
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot read pull request #%d's repository", n)
	}
	repo := parts[0] + "/" + parts[1]
	if _, err := gitstat.Git(ctx, root, "fetch", "--no-tags", fetchURL(root, repo), fmt.Sprintf("refs/pull/%d/head", n)); err != nil {
		return "", err
	}
	return pr.HeadRefOid, nil
}

// fetchURL is where the GitHub repository repo, owner/name, is fetched
// from: the URL of a remote's that names it, fetching or pushing, as one
// that fetches from elsewhere has no pull requests; else GitHub's.
func fetchURL(root, repo string) string {
	for _, u := range gitstat.RemoteURLs(root) {
		if strings.EqualFold(gitHubRepo(u.URL), repo) {
			return u.URL
		}
	}
	return "https://github.com/" + repo + ".git"
}
