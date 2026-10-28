// Package forge reads and acts on the forge that hosts a repository on
// this node, GitHub, or a Forgejo or Gitea, with the login the node's user
// already has there: the pull requests, issues and Actions runs, a pull
// request's change, and the comments, reviews and runs a front-end asks
// for. GitHub is reached through its CLI, gh; a Forgejo or Gitea through
// its API, with the credentials git keeps for its host.
package forge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// backend is one kind of forge.
type backend interface {
	name() string  // github, forgejo or gitea
	ops() []string // the operations it can do
	list(ctx context.Context, r *protocol.ForgeList) (*protocol.ForgeItems, error)
	view(ctx context.Context, r *protocol.ForgeView) (*protocol.ForgeDetail, error)
	// run acts as r says, one of ops, its kind, number and body checked
	// already; openPR is the operation that opens a pull request.
	run(ctx context.Context, r *protocol.ForgeRun) (string, error)
	openPR(ctx context.Context, root, branch, base, title, body string, draft bool) (string, error)
	diff(ctx context.Context, root string, n int64) (string, error) // a pull request's, unified
}

// of is the forge of the repository at root: the host its origin fetches
// from, else its first remote's, when that is GitHub or a Forgejo or
// Gitea; else GitHub, when a remote pushes there.
func of(ctx context.Context, root string) (backend, error) {
	if u := gitstat.FetchURL(root); u != "" {
		host, path := gitstat.RepoPath(u)
		owner, repo, ok := ownerRepo(path)
		switch {
		case !ok:
		case host == "github.com":
			return github{}, nil
		default:
			if flavor := giteaAt(ctx, host); flavor != "" {
				return &gitea{flavor: flavor, host: host, owner: owner, repo: repo, url: u}, nil
			}
		}
	}
	if repoOf(root) != "" {
		return github{}, nil
	}
	return nil, errors.New("this repository is not on a forge mako knows: GitHub, Forgejo or Gitea")
}

// ownerRepo splits a repository's path on its host, owner/name, as the
// forges have them.
func ownerRepo(path string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(path, "/")
	return owner, name, ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

// Name is the forge of the repository at root, as of answers: "" for
// none.
func Name(ctx context.Context, root string) string {
	b, err := of(ctx, root)
	if err != nil {
		return ""
	}
	return b.name()
}

// List lists a repository's pull requests, issues or runs, the most
// recently active first.
func List(ctx context.Context, r *protocol.ForgeList) (*protocol.ForgeItems, error) {
	if err := checkKind(r.GetKind()); err != nil {
		return nil, err
	}
	b, err := of(ctx, r.GetRoot())
	if err != nil {
		return nil, err
	}
	items, err := b.list(ctx, r)
	if items != nil {
		items.Forge, items.Ops = b.name(), b.ops()
	}
	return items, err
}

// View reads one pull request, issue or run, with its body, comments,
// reviews, files and checks. A pull request numbered 0 is the branch's.
func View(ctx context.Context, r *protocol.ForgeView) (*protocol.ForgeDetail, error) {
	if err := checkKind(r.GetKind()); err != nil {
		return nil, err
	}
	if r.GetNumber() < 0 || r.GetNumber() == 0 && r.GetKind() != protocol.ForgePR {
		return nil, fmt.Errorf("no %s numbered %d", r.GetKind(), r.GetNumber())
	}
	b, err := of(ctx, r.GetRoot())
	if err != nil {
		return nil, err
	}
	d, err := b.view(ctx, r)
	if d != nil {
		d.Forge, d.Ops = b.name(), b.ops()
	}
	return d, err
}

// checkKind refuses what is not a kind of item: pr, issue or run.
func checkKind(kind string) error {
	switch kind {
	case protocol.ForgePR, protocol.ForgeIssue, protocol.ForgeRuns:
		return nil
	}
	return fmt.Errorf("%q is not a pull request, an issue or a run", kind)
}

// maxLog caps the failed steps' logs a run_log replies with: their end,
// where a failure says why.
const maxLog = 256 << 10

// Run acts on the forge as r says, and returns what it said.
func Run(ctx context.Context, r *protocol.ForgeRun) (string, error) {
	b, err := of(ctx, r.GetRoot())
	if err != nil {
		return "", err
	}
	if r.GetOp() == protocol.ForgeOpCreatePR {
		branch := gitstat.Branch(r.GetRoot())
		if branch == "" {
			return "", errors.New("no branch is checked out to open a pull request of")
		}
		return openPR(ctx, b, r.GetRoot(), branch, r.GetBase(), r.GetTitle(), r.GetBody(), r.GetDraft())
	}
	if r.GetNumber() <= 0 {
		return "", errors.New("no pull request, issue or run named")
	}
	if !slices.Contains(b.ops(), r.GetOp()) {
		return "", fmt.Errorf("%s cannot do %q", b.name(), r.GetOp())
	}
	switch r.GetOp() {
	case protocol.ForgeOpComment:
		if k := r.GetKind(); k != protocol.ForgePR && k != protocol.ForgeIssue {
			return "", fmt.Errorf("%q is not a pull request or an issue", k)
		}
		if strings.TrimSpace(r.GetBody()) == "" {
			return "", errors.New("a comment needs a body")
		}
	case protocol.ForgeOpReview:
		switch r.GetReview() {
		case "approve":
		case "request_changes", "comment":
			if strings.TrimSpace(r.GetBody()) == "" {
				return "", errors.New("this review needs a body")
			}
		default:
			return "", fmt.Errorf("%q is not a review", r.GetReview())
		}
	}
	return b.run(ctx, r)
}

// WorktreePR pushes a session's branch and opens a pull request of it into
// the branch it started from, filled from its commits.
func WorktreePR(ctx context.Context, w *protocol.Worktree) (string, error) {
	b, err := of(ctx, w.GetPath())
	if err != nil {
		return "", err
	}
	return openPR(ctx, b, w.GetPath(), w.GetBranch(), w.GetBase(), "", "", false)
}

// openPR checks a pull request's branches, and has b open it.
func openPR(ctx context.Context, b backend, root, branch, base, title, body string, draft bool) (string, error) {
	if strings.HasPrefix(base, "-") || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("%q into %q is not a pull request of branches", branch, base)
	}
	return b.openPR(ctx, root, branch, base, title, body, draft)
}

// push pushes the branch checked out at root, setting its upstream if it
// has none.
func push(ctx context.Context, root string) (string, error) {
	return gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpPush})
}
