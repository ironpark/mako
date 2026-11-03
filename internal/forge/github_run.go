package forge

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// run acts through the API, and replies as gh printed it to a pipe: a
// comment's address, a run's failed logs, nothing for the rest.
func (g *github) run(ctx context.Context, r *protocol.ForgeRun) (string, error) {
	root, n := r.GetRoot(), r.GetNumber()
	switch r.GetOp() {
	case protocol.ForgeOpCheckout:
		return g.checkout(ctx, root, n)
	case protocol.ForgeOpFetchPR:
		pr, err := g.fetchHead(ctx, root, n)
		return pr.Head.SHA, err
	case protocol.ForgeOpComment:
		var c struct {
			URL string `json:"html_url"`
		}
		err := g.rest(ctx, http.MethodPost, g.path(fmt.Sprintf("/issues/%d/comments", n)), map[string]string{"body": r.GetBody()}, &c)
		return c.URL, err
	case protocol.ForgeOpReview:
		review := map[string]string{"event": map[string]string{"approve": "APPROVE", "request_changes": "REQUEST_CHANGES", "comment": "COMMENT"}[r.GetReview()]}
		if body := r.GetBody(); body != "" {
			review["body"] = body
		}
		return "", g.rest(ctx, http.MethodPost, g.path(fmt.Sprintf("/pulls/%d/reviews", n)), review, nil)
	case protocol.ForgeOpRerun:
		path := fmt.Sprintf("/actions/runs/%d/rerun", n)
		if r.GetFailed() {
			path = fmt.Sprintf("/actions/runs/%d/rerun-failed-jobs", n)
		}
		return "", g.rest(ctx, http.MethodPost, g.path(path), nil, nil)
	case protocol.ForgeOpCancel:
		return "", g.rest(ctx, http.MethodPost, g.path(fmt.Sprintf("/actions/runs/%d/cancel", n)), nil, nil)
	case protocol.ForgeOpFailedLogs:
		return g.failedLogs(ctx, n)
	}
	return "", fmt.Errorf("unknown forge operation %q", r.GetOp())
}

// pull is a pull request as the REST API gives it.
type pull struct {
	URL  string `json:"html_url"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
			Owner    user   `json:"owner"`
		} `json:"repo"` // nil when the fork is gone
	} `json:"head"`
	Base struct {
		Repo struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	} `json:"base"`
	MaintainerCanModify bool `json:"maintainer_can_modify"`
}

// cross reports whether the pull request is from another repository's
// branch, a fork's.
func (g *github) cross(pr pull) bool {
	return pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, g.full())
}

// fetchHead fetches pull request n's head, refs/pull/n/head, from this
// repository, unless it is here already, and returns the pull request.
func (g *github) fetchHead(ctx context.Context, root string, n int64) (pull, error) {
	var pr pull
	if err := g.rest(ctx, http.MethodGet, g.path(fmt.Sprintf("/pulls/%d", n)), nil, &pr); err != nil {
		return pr, err
	}
	if pr.Head.SHA == "" {
		return pr, fmt.Errorf("cannot read pull request #%d's head", n)
	}
	if _, err := gitstat.Git(ctx, root, "cat-file", "-e", pr.Head.SHA+"^{commit}"); err == nil {
		return pr, nil
	}
	_, fetch := remoteOf(root, g.full())
	_, err := gitstat.Git(ctx, root, "fetch", "--no-tags", fetch, fmt.Sprintf("refs/pull/%d/head", n))
	return pr, err
}

// checkout checks out a pull request's branch, as gh pr checkout does. One
// of this repository's, with a remote here, is fetched from it and
// tracked. A fork's is fetched as refs/pull/n/head onto a branch of its
// name, its owner's before it when it is the default branch's, which pulls
// from there.
func (g *github) checkout(ctx context.Context, root string, n int64) (string, error) {
	var pr pull
	if err := g.rest(ctx, http.MethodGet, g.path(fmt.Sprintf("/pulls/%d", n)), nil, &pr); err != nil {
		return "", err
	}
	branch := pr.Head.Ref
	if strings.HasPrefix(branch, "-") || branch == "" {
		return "", fmt.Errorf("pull request #%d's branch %q cannot be checked out", n, branch)
	}
	remote, fetch := remoteOf(root, g.full())
	if remote != "" && !g.cross(pr) {
		tracking := remote + "/" + branch
		if _, err := gitstat.Git(ctx, root, "fetch", "--no-tags", remote, "+refs/heads/"+branch+":refs/remotes/"+tracking); err != nil {
			return "", err
		}
		if _, err := gitstat.Git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
			return gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpTrack, Branch: tracking})
		}
		return checkoutAt(ctx, root, branch, "refs/remotes/"+tracking)
	}
	if g.cross(pr) && branch == pr.Base.Repo.DefaultBranch && pr.Head.Repo != nil {
		branch = pr.Head.Repo.Owner.Login + "/" + branch
	}
	ref := fmt.Sprintf("refs/pull/%d/head", n)
	if _, err := gitstat.Git(ctx, root, "fetch", "--no-tags", fetch, ref); err != nil {
		return "", err
	}
	out, err := checkoutAt(ctx, root, branch, pr.Head.SHA)
	if err != nil {
		return out, err
	}
	upstream := remote
	if upstream == "" {
		upstream = fetch
	}
	if _, err := gitstat.Git(ctx, root, "config", "branch."+branch+".remote", upstream); err != nil {
		return out, err
	}
	_, err = gitstat.Git(ctx, root, "config", "branch."+branch+".merge", ref)
	return out, err
}

// checkoutAt checks out branch at commit: made there when it is not here,
// else brought up to it.
func checkoutAt(ctx context.Context, root, branch, commit string) (string, error) {
	if _, err := gitstat.Git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpBranch, Branch: branch, Commit: commit})
	}
	if out, err := gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCheckout, Branch: branch}); err != nil {
		return out, err
	}
	return gitstat.Git(ctx, root, "merge", "--ff-only", commit)
}

// failedLogs are the logs of run n's failed steps, as gh run view
// --log-failed prints them: each line after its job's name and its step's.
// A job's log is read whole, and its lines told to their steps by the
// time each began; a failed job's whose steps it cannot tell is given
// whole. Only the end, maxLog of it, is kept.
func (g *github) failedLogs(ctx context.Context, n int64) (string, error) {
	jobs, err := g.jobs(ctx, n)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, j := range jobs {
		if !protocol.Failed(j.Conclusion) {
			continue
		}
		log, err := githubCall(ctx, http.MethodGet, githubAPI+g.path(fmt.Sprintf("/actions/jobs/%d/logs", j.ID)), "", nil)
		if err != nil {
			return "", fmt.Errorf("cannot read the log of %s: %w", j.Name, err)
		}
		writeFailedSteps(&b, j, strings.TrimPrefix(string(log), "\ufeff"))
	}
	out := b.String()
	if len(out) > maxLog {
		out = out[len(out)-maxLog:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out, nil
}

// writeFailedSteps writes the lines of job j's log of its failed steps,
// or all of it when no line is told to one.
func writeFailedSteps(b *strings.Builder, j job, log string) {
	steps := slices.Clone(j.Steps)
	slices.SortFunc(steps, func(a, b step) int { return a.StartedAt.Compare(b.StartedAt) })
	var cur *step
	wrote := false
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if stamp, _, ok := strings.Cut(line, " "); ok {
			if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				t = t.Truncate(time.Second)
				cur = nil
				for i := range steps {
					if !steps[i].StartedAt.IsZero() && !steps[i].StartedAt.After(t) {
						cur = &steps[i]
					}
				}
			}
		}
		if cur != nil && protocol.Failed(cur.Conclusion) {
			fmt.Fprintf(b, "%s\t%s\t%s\n", j.Name, cur.Name, line)
			wrote = true
		}
	}
	if !wrote {
		for line := range strings.Lines(log) {
			fmt.Fprintf(b, "%s\t\t%s", j.Name, line)
		}
		if !strings.HasSuffix(log, "\n") && log != "" {
			b.WriteByte('\n')
		}
	}
}

func (g *github) openPR(ctx context.Context, root, branch, base, title, body string, draft bool) (string, error) {
	if _, err := githubToken(ctx); err != nil {
		return "", err // before the push, which would be left without its pull request
	}
	if base == "" {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.rest(ctx, http.MethodGet, g.path(""), nil, &repo); err != nil {
			return "", err
		}
		base = repo.DefaultBranch
	}
	if strings.TrimSpace(title) == "" {
		remote, _ := remoteOf(root, g.full())
		if remote == "" {
			remote = "origin"
		}
		title, body = fill(ctx, root, branch, remote+"/"+base)
	}
	if pushed, err := push(ctx, root); err != nil {
		return pushed, err
	}
	// The branch as GitHub names it: owner:branch when it was pushed to
	// another repository, a fork, than this one.
	head := branch
	if merge := gitstat.Config(root, "branch", branch, "merge"); strings.HasPrefix(merge, "refs/heads/") {
		head = strings.TrimPrefix(merge, "refs/heads/")
	}
	if remote := gitstat.Config(root, "branch", branch, "remote"); remote != "" {
		u := remote
		if !strings.Contains(remote, ":") {
			u = gitstat.Config(root, "remote", remote, "url")
		}
		if owner, _, ok := ownerRepo(gitHubRepo(u)); ok && !strings.EqualFold(owner, g.owner) {
			head = owner + ":" + head
		}
	}
	var pr pull
	err := g.rest(ctx, http.MethodPost, g.path("/pulls"), map[string]any{"head": head, "base": base, "title": title, "body": body, "draft": draft}, &pr)
	return pr.URL, err
}

// fill is a pull request's title and body from the commits of branch
// since base, as gh's --fill makes them: one commit's message, or the
// branch's name over their subjects.
func fill(ctx context.Context, root, branch, base string) (title, body string) {
	log, err := gitstat.Git(ctx, root, "log", "--format=%s%x1f%b%x1e", "--reverse", base+"..HEAD")
	if err != nil {
		log, _ = gitstat.Git(ctx, root, "log", "-1", "--format=%s%x1f%b%x1e")
	}
	var subjects []string
	for c := range strings.SplitSeq(strings.TrimSpace(log), "\x1e") {
		subject, b, _ := strings.Cut(strings.TrimSpace(c), "\x1f")
		if subject == "" {
			continue
		}
		subjects = append(subjects, subject)
		title, body = subject, strings.TrimSpace(b)
	}
	if len(subjects) > 1 {
		title, body = branch, "- "+strings.Join(subjects, "\n- ")
	}
	return title, body
}
