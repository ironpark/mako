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
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// A Forgejo or Gitea is read and acted on through its API, at https on the
// host a remote names, with the credentials git keeps for that host, as
// git credential fill gives them: the token a credential helper or manager
// was given, whatever the remote's own protocol. Without one, what is
// public is read.

// gitea is a repository on a Forgejo or Gitea.
type gitea struct {
	flavor      string // forgejo or gitea
	host        string
	owner, repo string
	url         string // the remote's, which fetches its pull requests
}

func (g *gitea) name() string { return g.flavor }

// ops leaves out what the API cannot do: run a workflow again, cancel it,
// or give its logs.
func (g *gitea) ops() []string {
	return []string{protocol.ForgeOpCheckout, protocol.ForgeOpFetchPR, protocol.ForgeOpComment, protocol.ForgeOpReview}
}

var hostClient = &http.Client{Timeout: 30 * time.Second}

// How long a host's credentials are kept, and what it is: a host that is
// no Forgejo or Gitea, as one reached over SSH alone, is asked again less
// often.
const (
	tokenTTL  = 10 * time.Minute
	flavorTTL = 10 * time.Minute
	notTTL    = time.Hour
)

type kept struct {
	value string
	until time.Time
}

// keptMap keeps values by key until they expire.
type keptMap struct {
	mu sync.Mutex // held while a value is got, so requests at once get it once
	m  map[string]kept
}

var flavors, tokens = &keptMap{m: map[string]kept{}}, &keptMap{m: map[string]kept{}}

// get is the value kept by key, or what get gives, kept as long as ttl
// says of it.
func (k *keptMap) get(key string, get func() string, ttl func(string) time.Duration) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[key]; ok && time.Now().Before(v.until) {
		return v.value
	}
	v := get()
	k.m[key] = kept{v, time.Now().Add(ttl(v))}
	return v
}

// version is what a version endpoint answers.
type version struct {
	Version string `json:"version"`
}

// giteaAt is what host serves, forgejo or gitea, "" for neither.
func giteaAt(ctx context.Context, host string) string {
	return flavors.get(host, func() string {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var v, fj version
		if err := getJSON(ctx, "https://"+host+"/api/v1/version", "", &v); err != nil || v.Version == "" {
			return ""
		}
		if getJSON(ctx, "https://"+host+"/api/forgejo/v1/version", "", &fj) == nil && fj.Version != "" {
			return "forgejo"
		}
		return "gitea"
	}, func(f string) time.Duration {
		if f == "" {
			return notTTL
		}
		return flavorTTL
	})
}

// token is the password git keeps for host, which for a forge is a token,
// "" for none. git asks no one: a credential it does not have is none.
func token(ctx context.Context, host string) string {
	return tokens.get(host, func() string {
		cmd := exec.CommandContext(ctx, "git", "credential", "fill")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		for line := range strings.Lines(string(out)) {
			if p, ok := strings.CutPrefix(strings.TrimSpace(line), "password="); ok {
				return p
			}
		}
		return ""
	}, func(string) time.Duration { return tokenTTL })
}

// getJSON reads a JSON document into v.
func getJSON(ctx context.Context, u, tok string, v any) error {
	body, err := call(ctx, http.MethodGet, u, tok, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// call makes a request of an API, its body JSON, and returns the reply's.
func call(ctx context.Context, method, u, tok string, body any) ([]byte, error) {
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
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "token "+tok)
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
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(out, &e)
		return nil, fmt.Errorf("%s answered %s: %s", req.URL.Host, resp.Status, cmp.Or(e.Message, strings.TrimSpace(string(out))))
	}
	return out, nil
}

// api makes a request of the repository's API, at path under it.
func (g *gitea) api(ctx context.Context, method, path string, body, v any) error {
	out, err := call(ctx, method, g.apiURL(path), token(ctx, g.host), body)
	if err != nil || v == nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func (g *gitea) apiURL(path string) string {
	return "https://" + g.host + "/api/v1/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + path
}

// What the API gives.

type gPR struct {
	Number  int64     `json:"number"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	State   string    `json:"state"`
	Merged  bool      `json:"merged"`
	Draft   bool      `json:"draft"`
	HTMLURL string    `json:"html_url"`
	Updated time.Time `json:"updated_at"`
	User    user      `json:"user"`
	Labels  []label   `json:"labels"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	PullRequest *struct{} `json:"pull_request"` // an issue's, when it is a pull request
}

type gComment struct {
	User    user      `json:"user"`
	Body    string    `json:"body"`
	Created time.Time `json:"created_at"`
}

type gReview struct {
	ID        int64     `json:"id"`
	User      user      `json:"user"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Submitted time.Time `json:"submitted_at"`
	Comments  int64     `json:"comments_count"`
}

type gTask struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Title      string    `json:"display_title"`
	HeadBranch string    `json:"head_branch"`
	HeadSHA    string    `json:"head_sha"`
	Event      string    `json:"event"`
	Status     string    `json:"status"`
	URL        string    `json:"url"`
	Updated    time.Time `json:"updated_at"`
}

// proto is a pull request, or an issue, as a list shows it.
func (p gPR) proto(kind string) *protocol.ForgeItem {
	state := strings.ToUpper(p.State)
	if p.Merged {
		state = "MERGED"
	}
	it := &protocol.ForgeItem{
		Kind: kind, Number: p.Number, Title: p.Title, Author: p.User.Login, State: state, Url: p.HTMLURL,
		Updated: protocol.Time(p.Updated), Head: p.Head.Ref, Base: p.Base.Ref, HeadOid: p.Head.SHA,
		Draft: p.Draft, // titled as the server takes a draft's
	}
	for _, l := range p.Labels {
		it.Labels = append(it.Labels, l.Name)
	}
	return it
}

// proto is a run as a list shows it: its status split into GitHub's
// status and conclusion.
func (t gTask) proto() *protocol.ForgeItem {
	status, conclusion := "completed", t.Status
	switch t.Status {
	case "waiting", "blocked":
		status, conclusion = "queued", ""
	case "running":
		status, conclusion = "in_progress", ""
	}
	return &protocol.ForgeItem{
		Kind: protocol.ForgeRuns, Number: t.ID, Title: cmp.Or(t.Title, t.Name), State: status, Checks: conclusion,
		Url: t.URL, Updated: protocol.Time(t.Updated), Head: t.HeadBranch, HeadOid: t.HeadSHA, Workflow: t.Name, Event: t.Event,
	}
}

// listState is what a list asks for as the API's state.
func listState(s string) (string, error) {
	switch s = cmp.Or(s, "open"); s {
	case "open", "closed", "all":
		return s, nil
	}
	return "", fmt.Errorf("%q is not a state to list", s)
}

func (g *gitea) list(ctx context.Context, r *protocol.ForgeList) (*protocol.ForgeItems, error) {
	limit := strconv.FormatInt(min(cmp.Or(r.GetMax(), 50), 50), 10)
	branch := r.GetBranch()
	if r.GetCurrent() {
		if branch = gitstat.Branch(r.GetRoot()); branch == "" {
			return &protocol.ForgeItems{}, nil
		}
	}
	out := &protocol.ForgeItems{}
	switch r.GetKind() {
	case protocol.ForgeRuns:
		var tasks struct {
			Runs []gTask `json:"workflow_runs"`
		}
		if err := g.api(ctx, http.MethodGet, "/actions/tasks?limit="+limit, nil, &tasks); err != nil {
			return nil, err
		}
		for _, t := range tasks.Runs {
			if branch == "" || t.HeadBranch == branch {
				out.Items = append(out.Items, t.proto())
			}
		}
	case protocol.ForgePR:
		state, err := listState(r.GetState())
		if err != nil {
			return nil, err
		}
		if branch != "" {
			state = "all" // the branch's, whatever became of it
		}
		var prs []gPR
		if err := g.api(ctx, http.MethodGet, "/pulls?sort=recentupdate&state="+state+"&limit="+limit, nil, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			// The branch's own, not a fork's of the same name.
			if branch == "" || p.Head.Ref == branch && g.own(p) {
				out.Items = append(out.Items, p.proto(protocol.ForgePR))
			}
		}
		if branch != "" && len(out.Items) > 1 {
			out.Items = out.Items[:1]
		}
	case protocol.ForgeIssue:
		state, err := listState(r.GetState())
		if err != nil {
			return nil, err
		}
		var issues []gPR
		if err := g.api(ctx, http.MethodGet, "/issues?type=issues&state="+state+"&limit="+limit, nil, &issues); err != nil {
			return nil, err
		}
		for _, i := range issues {
			if i.PullRequest == nil {
				out.Items = append(out.Items, i.proto(protocol.ForgeIssue))
			}
		}
	}
	return out, nil
}

func (g *gitea) view(ctx context.Context, r *protocol.ForgeView) (*protocol.ForgeDetail, error) {
	n := r.GetNumber()
	switch r.GetKind() {
	case protocol.ForgeRuns:
		// The API has no run of its own to read, only the list of them.
		items, err := g.list(ctx, &protocol.ForgeList{Root: r.GetRoot(), Kind: protocol.ForgeRuns})
		if err != nil {
			return nil, err
		}
		for _, it := range items.GetItems() {
			if it.GetNumber() == n {
				return &protocol.ForgeDetail{Item: it}, nil
			}
		}
		return nil, fmt.Errorf("no run %d among the latest", n)
	case protocol.ForgeIssue:
		var issue gPR
		var comments []gComment
		err := all(
			func() error { return g.api(ctx, http.MethodGet, fmt.Sprintf("/issues/%d", n), nil, &issue) },
			func() error { return g.api(ctx, http.MethodGet, fmt.Sprintf("/issues/%d/comments", n), nil, &comments) },
		)
		if err != nil {
			return nil, err
		}
		d := &protocol.ForgeDetail{Item: issue.proto(protocol.ForgeIssue), Body: issue.Body}
		d.Comments = gComments(comments)
		return d, nil
	}
	if n == 0 {
		items, err := g.list(ctx, &protocol.ForgeList{Root: r.GetRoot(), Kind: protocol.ForgePR, Current: true})
		if err != nil {
			return nil, err
		}
		if len(items.GetItems()) == 0 {
			return &protocol.ForgeDetail{None: true}, nil
		}
		n = items.GetItems()[0].GetNumber()
	}
	var pr gPR
	var comments []gComment
	var reviews []gReview
	var files []struct {
		Name      string `json:"filename"`
		From      string `json:"previous_filename"`
		Status    string `json:"status"`
		Additions int64  `json:"additions"`
		Deletions int64  `json:"deletions"`
	}
	err := all(
		func() error { return g.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d", n), nil, &pr) },
		func() error { return g.api(ctx, http.MethodGet, fmt.Sprintf("/issues/%d/comments", n), nil, &comments) },
		func() error { return g.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews", n), nil, &reviews) },
		func() error {
			return g.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/files?limit=300", n), nil, &files)
		},
	)
	if err != nil {
		return nil, err
	}
	d := &protocol.ForgeDetail{Item: pr.proto(protocol.ForgePR), Body: pr.Body}
	d.Comments = gComments(comments)
	// The reviews, and their comments on lines, and the checks of the
	// head, read side by side.
	lines := make([][]*protocol.ForgeComment, len(reviews))
	var reads []func() error
	for i, rv := range reviews {
		if rv.State == "PENDING" || rv.State == "REQUEST_REVIEW" {
			continue // not given yet, or only asked for
		}
		if rv.Body != "" || rv.State != "COMMENT" {
			d.Comments = append(d.Comments, &protocol.ForgeComment{Author: rv.User.Login, Body: rv.Body, Time: protocol.Time(rv.Submitted), Review: reviewState(rv.State)})
		}
		if rv.Comments > 0 {
			reads = append(reads, func() error {
				var cs []struct {
					gComment
					Path     string `json:"path"`
					Position int64  `json:"position"`
				}
				if err := g.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews/%d/comments", n, rv.ID), nil, &cs); err != nil {
					return nil // the review shows without them
				}
				for _, c := range cs {
					lines[i] = append(lines[i], &protocol.ForgeComment{Author: c.User.Login, Body: c.Body, Time: protocol.Time(c.Created), Path: c.Path, Line: c.Position})
				}
				return nil
			})
		}
	}
	var status struct {
		Statuses []struct {
			Context   string `json:"context"`
			Status    string `json:"status"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if pr.Head.SHA != "" {
		reads = append(reads, func() error {
			_ = g.api(ctx, http.MethodGet, "/commits/"+url.PathEscape(pr.Head.SHA)+"/status", nil, &status)
			return nil
		})
	}
	_ = all(reads...)
	for _, l := range lines {
		d.Comments = append(d.Comments, l...)
	}
	slices.SortStableFunc(d.Comments, func(a, b *protocol.ForgeComment) int {
		return protocol.TimeOf(a.GetTime()).Compare(protocol.TimeOf(b.GetTime()))
	})
	var checks []check
	for _, s := range status.Statuses {
		c := check{Context: s.Context, TargetURL: s.TargetURL, State: strings.ToUpper(s.Status)}
		if c.State == "WARNING" {
			c.State = "SUCCESS" // a warning passes
		}
		checks = append(checks, c)
		d.Checks = append(d.Checks, c.proto())
	}
	d.Item.Checks = rollup(checks)
	for _, f := range files {
		d.Files = append(d.Files, &protocol.GitFile{Path: f.Name, From: f.From, Status: changeStatus(f.Status), Added: f.Additions, Removed: f.Deletions})
	}
	return d, nil
}

// reviewState is a review's state as GitHub names it.
func reviewState(s string) string {
	switch s {
	case "REQUEST_CHANGES":
		return "CHANGES_REQUESTED"
	case "COMMENT":
		return "COMMENTED"
	}
	return s
}

func gComments(cs []gComment) []*protocol.ForgeComment {
	var out []*protocol.ForgeComment
	for _, c := range cs {
		out = append(out, &protocol.ForgeComment{Author: c.User.Login, Body: c.Body, Time: protocol.Time(c.Created)})
	}
	return out
}

// all runs fns side by side, and returns the first error of theirs.
func all(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Go(func() { errs[i] = fn() })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (g *gitea) run(ctx context.Context, r *protocol.ForgeRun) (string, error) {
	n := r.GetNumber()
	switch r.GetOp() {
	case protocol.ForgeOpComment:
		var c struct {
			HTMLURL string `json:"html_url"`
		}
		err := g.api(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", n), map[string]string{"body": r.GetBody()}, &c)
		return c.HTMLURL, err
	case protocol.ForgeOpReview:
		event := map[string]string{"approve": "APPROVED", "request_changes": "REQUEST_CHANGES", "comment": "COMMENT"}[r.GetReview()]
		var rv struct {
			HTMLURL string `json:"html_url"`
		}
		err := g.api(ctx, http.MethodPost, fmt.Sprintf("/pulls/%d/reviews", n), map[string]string{"event": event, "body": r.GetBody()}, &rv)
		return rv.HTMLURL, err
	case protocol.ForgeOpCheckout:
		return g.checkout(ctx, r.GetRoot(), n)
	case protocol.ForgeOpFetchPR:
		pr, err := g.fetchHead(ctx, r.GetRoot(), n)
		return pr.Head.SHA, err
	}
	return "", fmt.Errorf("%s's API cannot do %s", g.flavor, r.GetOp())
}

func (g *gitea) openPR(ctx context.Context, root, branch, base, title, body string, draft bool) (string, error) {
	if base == "" {
		var repo struct {
			Default string `json:"default_branch"`
		}
		if err := g.api(ctx, http.MethodGet, "", nil, &repo); err != nil {
			return "", err
		}
		base = repo.Default
	}
	if strings.TrimSpace(title) == "" {
		// Filled from the branch's commits, as gh's --fill does: one
		// commit's message, or the branch's name over their subjects.
		log, err := gitstat.Git(ctx, root, "log", "--format=%s%x1f%b%x1e", "--reverse", "origin/"+base+"..HEAD")
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
	}
	if draft {
		title = "WIP: " + title
	}
	if pushed, err := push(ctx, root); err != nil {
		return pushed, err
	}
	var pr gPR
	err := g.api(ctx, http.MethodPost, "/pulls", map[string]string{"head": branch, "base": base, "title": title, "body": body}, &pr)
	return pr.HTMLURL, err
}

// own reports whether a pull request is from a branch of this repository,
// not a fork's.
func (g *gitea) own(p gPR) bool {
	return strings.EqualFold(p.Head.Repo.FullName, g.owner+"/"+g.repo)
}

// fetchHead fetches pull request n's head from the remote, and returns
// its commit and its branch.
func (g *gitea) fetchHead(ctx context.Context, root string, n int64) (gPR, error) {
	var pr gPR
	if err := g.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d", n), nil, &pr); err != nil {
		return pr, err
	}
	if _, err := gitstat.Git(ctx, root, "cat-file", "-e", pr.Head.SHA+"^{commit}"); err == nil {
		return pr, nil
	}
	_, err := gitstat.Git(ctx, root, "fetch", "--no-tags", g.url, fmt.Sprintf("refs/pull/%d/head", n))
	return pr, err
}

// checkout checks out a pull request's head on its own branch: the
// pull request's, when it is from this repository and no branch of the
// name is here, else pr-n; one already here is brought up to it.
func (g *gitea) checkout(ctx context.Context, root string, n int64) (string, error) {
	pr, err := g.fetchHead(ctx, root, n)
	if err != nil {
		return "", err
	}
	branch := fmt.Sprintf("pr-%d", n)
	if g.own(pr) && pr.Head.Ref != "" {
		branch = pr.Head.Ref
	}
	if _, err := gitstat.Git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpBranch, Branch: branch, Commit: pr.Head.SHA})
	}
	if out, err := gitstat.Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCheckout, Branch: branch}); err != nil {
		return out, err
	}
	return gitstat.Git(ctx, root, "merge", "--ff-only", pr.Head.SHA)
}

func (g *gitea) diff(ctx context.Context, _ string, n int64) (string, error) {
	out, err := call(ctx, http.MethodGet, g.apiURL(fmt.Sprintf("/pulls/%d.diff", n)), token(ctx, g.host), nil)
	return string(out), err
}
