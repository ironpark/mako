package forge

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// What GitHub gives: pull requests and issues as the GraphQL API names
// their fields, as gh's --json names them, and runs as the REST API does.

type user struct {
	Login string `json:"login"`
}

type label struct {
	Name string `json:"name"`
}

// nodes is a GraphQL connection: a page of what it connects to.
type nodes[T any] struct {
	Nodes    []T `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

// check is a pull request's check: a check run, of Actions or another app,
// or a commit status, which has a state in place of a status and a
// conclusion.
type check struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
	URL        string `json:"url"`
	Workflow   string `json:"workflowName"`
}

// checkNode is a check as the GraphQL API gives it, its workflow's name
// within.
type checkNode struct {
	check
	CheckSuite *struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

type comment struct {
	Author    user      `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type review struct {
	Author      user      `json:"author"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type file struct {
	Path       string `json:"path"`
	Additions  int64  `json:"additions"`
	Deletions  int64  `json:"deletions"`
	ChangeType string `json:"changeType"`
}

// item is a pull request or an issue, whichever fields it has.
type item struct {
	Number      int64        `json:"number"`
	Title       string       `json:"title"`
	Author      user         `json:"author"`
	State       string       `json:"state"`
	URL         string       `json:"url"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	IsDraft     bool         `json:"isDraft"`
	HeadRefName string       `json:"headRefName"`
	BaseRefName string       `json:"baseRefName"`
	HeadRefOid  string       `json:"headRefOid"`
	Labels      nodes[label] `json:"labels"`
	Rollup      nodes[struct {
		Commit struct {
			StatusCheckRollup *struct {
				Contexts nodes[checkNode] `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	}] `json:"statusCheckRollup"`
	IsCrossRepository   bool `json:"isCrossRepository"`
	HeadRepositoryOwner user `json:"headRepositoryOwner"`

	Body     string         `json:"body"`
	Comments nodes[comment] `json:"comments"`
	Reviews  nodes[review]  `json:"reviews"`
	Files    nodes[file]    `json:"files"`
}

// checks are a pull request's checks on its head.
func (it item) checks() []check {
	var out []check
	for _, n := range it.Rollup.Nodes {
		if r := n.Commit.StatusCheckRollup; r != nil {
			for _, c := range r.Contexts.Nodes {
				if c.CheckSuite != nil && c.CheckSuite.WorkflowRun != nil {
					c.Workflow = c.CheckSuite.WorkflowRun.Workflow.Name
				}
				out = append(out, c.check)
			}
		}
	}
	return out
}

// The fields each kind is read with, as gh asks for them: a list's, and
// the more a view adds.
const (
	issueFields = `number title state url updatedAt author { login } labels(first: 100) { nodes { name } }`
	prFields    = issueFields + ` isDraft headRefName baseRefName headRefOid isCrossRepository headRepositoryOwner { login }
		statusCheckRollup: commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
			__typename
			... on StatusContext { context state targetUrl }
			... on CheckRun { name status conclusion detailsUrl checkSuite { workflowRun { workflow { name } } } }
		} } } } } }`
	commentFields = `body comments(last: 100) { nodes { author { login } body createdAt } }`
	prViewFields  = prFields + ` ` + commentFields + `
		reviews(last: 100) { nodes { author { login } body state submittedAt } }
		files(first: 100) { nodes { path additions deletions changeType } pageInfo { hasNextPage endCursor } }`
	fileFields = `files(first: 100, after: $after) { nodes { path additions deletions changeType } pageInfo { hasNextPage endCursor } }`
)

// maxPages bounds what is read a page at a time: a list's items, a pull
// request's files, as GitHub lists up to 3000 of those.
const maxPages = 30

func (g *github) list(ctx context.Context, r *protocol.ForgeList) (*protocol.ForgeItems, error) {
	kind := r.GetKind()
	limit := int(min(cmp.Or(r.GetMax(), 50), 200))
	branch := r.GetBranch()
	if r.GetCurrent() {
		if kind == protocol.ForgePR {
			return g.currentPR(ctx, r.GetRoot())
		}
		if branch = gitstat.Branch(r.GetRoot()); branch == "" {
			return &protocol.ForgeItems{}, nil // not a repository
		}
	}
	if strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("%q is not a branch", branch)
	}
	if kind == protocol.ForgeRuns {
		return g.listRuns(ctx, branch, limit)
	}
	states, err := listStates(kind, cmp.Or(r.GetState(), "open"))
	if err != nil {
		return nil, err
	}
	conn, state, fields, headVar, headArg := "issues", "IssueState", issueFields, "", ""
	vars := map[string]any{"owner": g.owner, "repo": g.repo, "states": states}
	if kind == protocol.ForgePR {
		conn, state, fields = "pullRequests", "PullRequestState", prFields
		if branch != "" {
			headVar, headArg, vars["head"] = ", $head: String!", ", headRefName: $head", branch
		}
	}
	query := `query($owner: String!, $repo: String!, $states: [` + state + `!], $first: Int!, $after: String` + headVar + `) {
		repository(owner: $owner, name: $repo) {
			list: ` + conn + `(first: $first, after: $after, states: $states` + headArg + `, orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes { ` + fields + ` }
				pageInfo { hasNextPage endCursor }
			}
		}
	}`
	reply := &protocol.ForgeItems{}
	var after any
	for page := 0; len(reply.Items) < limit && page < maxPages; page++ {
		vars["first"], vars["after"] = min(limit-len(reply.Items), 100), after
		var data struct {
			Repository struct {
				List nodes[item] `json:"list"`
			} `json:"repository"`
		}
		if err := g.graphql(ctx, query, vars, &data); err != nil {
			return nil, err
		}
		for _, it := range data.Repository.List.Nodes {
			reply.Items = append(reply.Items, it.proto(kind))
		}
		if pi := data.Repository.List.PageInfo; !pi.HasNextPage {
			break
		} else {
			after = pi.EndCursor
		}
	}
	return reply, nil
}

// listStates are the states a list of state asks for, as gh's --state
// says them: closed pull requests are merged ones too.
func listStates(kind, state string) ([]string, error) {
	switch {
	case state == "open":
		return []string{"OPEN"}, nil
	case state == "closed" && kind == protocol.ForgePR:
		return []string{"CLOSED", "MERGED"}, nil
	case state == "closed":
		return []string{"CLOSED"}, nil
	case state == "merged" && kind == protocol.ForgePR:
		return []string{"MERGED"}, nil
	case state == "all" && kind == protocol.ForgePR:
		return []string{"OPEN", "CLOSED", "MERGED"}, nil
	case state == "all":
		return []string{"OPEN", "CLOSED"}, nil
	}
	return nil, fmt.Errorf("%q is not a state to list", state)
}

func (g *github) view(ctx context.Context, r *protocol.ForgeView) (*protocol.ForgeDetail, error) {
	kind, n := r.GetKind(), r.GetNumber()
	if kind == protocol.ForgeRuns {
		return g.viewRun(ctx, n)
	}
	if kind == protocol.ForgePR && n == 0 {
		pr, err := g.branchPR(ctx, r.GetRoot())
		if err != nil {
			return nil, err
		}
		if pr == nil {
			return &protocol.ForgeDetail{None: true}, nil
		}
		n = pr.Number
	}

	// A pull request's comments on lines are only in the REST API's.
	var lines []*protocol.ForgeComment
	var linesErr error
	done := make(chan struct{})
	if kind == protocol.ForgePR {
		go func() { defer close(done); lines, linesErr = g.lineComments(ctx, n) }()
	} else {
		close(done)
	}
	it, err := g.item(ctx, kind, n)
	<-done
	if err != nil {
		return nil, err
	}
	d := &protocol.ForgeDetail{Item: it.proto(kind), Body: it.Body}
	for _, c := range it.Comments.Nodes {
		d.Comments = append(d.Comments, &protocol.ForgeComment{Author: c.Author.Login, Body: c.Body, Time: protocol.Time(c.CreatedAt)})
	}
	for _, rv := range it.Reviews.Nodes {
		if rv.Body == "" && rv.State == "COMMENTED" {
			continue // only the line comments it carried, listed on their own
		}
		d.Comments = append(d.Comments, &protocol.ForgeComment{Author: rv.Author.Login, Body: rv.Body, Time: protocol.Time(rv.SubmittedAt), Review: rv.State})
	}
	if linesErr == nil {
		d.Comments = append(d.Comments, lines...)
	}
	slices.SortStableFunc(d.Comments, func(a, b *protocol.ForgeComment) int {
		return protocol.TimeOf(a.GetTime()).Compare(protocol.TimeOf(b.GetTime()))
	})
	for _, f := range it.Files.Nodes {
		d.Files = append(d.Files, &protocol.GitFile{Path: f.Path, Status: changeStatus(f.ChangeType), Added: f.Additions, Removed: f.Deletions})
	}
	for _, c := range it.checks() {
		d.Checks = append(d.Checks, c.proto())
	}
	return d, nil
}

// item reads pull request or issue n in full: a pull request's files all,
// a page at a time.
func (g *github) item(ctx context.Context, kind string, n int64) (item, error) {
	field, fields := "issue", issueFields+" "+commentFields
	if kind == protocol.ForgePR {
		field, fields = "pullRequest", prViewFields
	}
	var data struct {
		Repository struct {
			Item *item `json:"item"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": g.owner, "repo": g.repo, "n": n}
	err := g.graphql(ctx, `query($owner: String!, $repo: String!, $n: Int!) {
		repository(owner: $owner, name: $repo) { item: `+field+`(number: $n) { `+fields+` } }
	}`, vars, &data)
	if err != nil {
		return item{}, err
	}
	it := data.Repository.Item
	if it == nil {
		return item{}, fmt.Errorf("no %s numbered %d", kind, n)
	}
	for page := 1; it.Files.PageInfo.HasNextPage && page < maxPages; page++ {
		vars["after"] = it.Files.PageInfo.EndCursor
		var more struct {
			Repository struct {
				Item struct {
					Files nodes[file] `json:"files"`
				} `json:"item"`
			} `json:"repository"`
		}
		if err := g.graphql(ctx, `query($owner: String!, $repo: String!, $n: Int!, $after: String) {
			repository(owner: $owner, name: $repo) { item: pullRequest(number: $n) { `+fileFields+` } }
		}`, vars, &more); err != nil {
			return item{}, err
		}
		it.Files.Nodes = append(it.Files.Nodes, more.Repository.Item.Files.Nodes...)
		it.Files.PageInfo = more.Repository.Item.Files.PageInfo
	}
	return *it, nil
}

// currentPR lists the pull request of the branch checked out, as branchPR
// finds it.
func (g *github) currentPR(ctx context.Context, root string) (*protocol.ForgeItems, error) {
	pr, err := g.branchPR(ctx, root)
	if err != nil || pr == nil {
		return &protocol.ForgeItems{}, err
	}
	return &protocol.ForgeItems{Items: []*protocol.ForgeItem{pr.proto(protocol.ForgePR)}}, nil
}

// branchPR is the pull request of the branch checked out at root, found as
// gh pr view finds it: the one its upstream names, refs/pull/n/head, as gh
// pr checkout sets of a fork's; else of the branch its upstream is, by
// name, from the repository the upstream's remote is of, or this one, an
// open one before others. It is nil for none.
func (g *github) branchPR(ctx context.Context, root string) (*item, error) {
	branch := gitstat.Branch(root)
	if branch == "" {
		return nil, nil
	}
	head, owner := branch, ""
	merge := gitstat.Config(root, "branch", branch, "merge")
	if n, ok := pullRef(merge); ok {
		it, err := g.item(ctx, protocol.ForgePR, n)
		return &it, err
	}
	if b, ok := strings.CutPrefix(merge, "refs/heads/"); ok {
		head = b
	}
	if remote := gitstat.Config(root, "branch", branch, "remote"); remote != "" && remote != "." {
		u := remote
		if !strings.Contains(remote, ":") { // a remote's name, not a URL
			u = gitstat.Config(root, "remote", remote, "url")
		}
		if o, _, ok := ownerRepo(gitHubRepo(u)); ok {
			owner = o
		}
	}
	var data struct {
		Repository struct {
			PRs nodes[item] `json:"pullRequests"`
		} `json:"repository"`
	}
	err := g.graphql(ctx, `query($owner: String!, $repo: String!, $head: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(headRefName: $head, first: 30, orderBy: {field: CREATED_AT, direction: DESC}) { nodes { `+prFields+` } }
		}
	}`, map[string]any{"owner": g.owner, "repo": g.repo, "head": head}, &data)
	if err != nil {
		return nil, err
	}
	prs := data.Repository.PRs.Nodes
	slices.SortStableFunc(prs, func(a, b item) int {
		return cmp.Compare(b2i(a.State != "OPEN"), b2i(b.State != "OPEN"))
	})
	for _, pr := range prs {
		if pr.HeadRefName != head {
			continue
		}
		if owner != "" && !strings.EqualFold(owner, g.owner) {
			if strings.EqualFold(pr.HeadRepositoryOwner.Login, owner) {
				return &pr, nil
			}
		} else if !pr.IsCrossRepository {
			return &pr, nil
		}
	}
	return nil, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pullRef is the number of a pull request's head ref, refs/pull/n/head.
func pullRef(ref string) (int64, bool) {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0, false
	}
	num, ok := strings.CutSuffix(rest, "/head")
	n, err := strconv.ParseInt(num, 10, 64)
	return n, ok && err == nil && n > 0
}

// lineComments are a pull request's review comments on lines of its
// change, as many as one page of the API's holds.
func (g *github) lineComments(ctx context.Context, n int64) ([]*protocol.ForgeComment, error) {
	var list []struct {
		User      user      `json:"user"`
		Body      string    `json:"body"`
		Path      string    `json:"path"`
		Line      int64     `json:"line"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := g.rest(ctx, http.MethodGet, g.path(fmt.Sprintf("/pulls/%d/comments?per_page=100", n)), nil, &list); err != nil {
		return nil, err
	}
	var cs []*protocol.ForgeComment
	for _, c := range list {
		cs = append(cs, &protocol.ForgeComment{Author: c.User.Login, Body: c.Body, Time: protocol.Time(c.CreatedAt), Path: c.Path, Line: c.Line})
	}
	return cs, nil
}

// proto is a pull request or an issue as a list shows it.
func (it item) proto(kind string) *protocol.ForgeItem {
	checks := it.checks()
	p := &protocol.ForgeItem{
		Kind: kind, Number: it.Number, Title: it.Title, Author: it.Author.Login, State: it.State, Url: it.URL,
		Updated: protocol.Time(it.UpdatedAt), Draft: it.IsDraft, Head: it.HeadRefName, Base: it.BaseRefName, HeadOid: it.HeadRefOid,
		Checks: rollup(checks),
	}
	for _, l := range it.Labels.Nodes {
		p.Labels = append(p.Labels, l.Name)
	}
	return p
}

// Runs.

// run is a workflow run of Actions.
type run struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"` // its workflow's
	Title      string    `json:"display_title"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"html_url"`
	UpdatedAt  time.Time `json:"updated_at"`
	HeadBranch string    `json:"head_branch"`
	HeadSha    string    `json:"head_sha"`
	Event      string    `json:"event"`
}

// job is a job of a run, with its steps.
type job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
	Steps      []step `json:"steps"`
}

type step struct {
	Name        string    `json:"name"`
	Number      int64     `json:"number"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

func (r run) proto() *protocol.ForgeItem {
	return &protocol.ForgeItem{
		Kind: protocol.ForgeRuns, Number: r.ID, Title: r.Title, State: r.Status, Checks: r.Conclusion, Url: r.URL,
		Updated: protocol.Time(r.UpdatedAt), Head: r.HeadBranch, HeadOid: r.HeadSha, Workflow: r.Name, Event: r.Event,
	}
}

func (g *github) listRuns(ctx context.Context, branch string, limit int) (*protocol.ForgeItems, error) {
	reply := &protocol.ForgeItems{}
	for page := 1; len(reply.Items) < limit && page <= maxPages; page++ {
		path := fmt.Sprintf("/actions/runs?per_page=%d&page=%d", min(limit, 100), page)
		if branch != "" {
			path += "&branch=" + url.QueryEscape(branch)
		}
		var runs struct {
			Runs []run `json:"workflow_runs"`
		}
		if err := g.rest(ctx, http.MethodGet, g.path(path), nil, &runs); err != nil {
			return nil, err
		}
		for _, r := range runs.Runs[:min(len(runs.Runs), limit-len(reply.Items))] {
			reply.Items = append(reply.Items, r.proto())
		}
		if len(runs.Runs) < min(limit, 100) {
			break
		}
	}
	return reply, nil
}

// jobs are run n's jobs, of its latest attempt.
func (g *github) jobs(ctx context.Context, n int64) ([]job, error) {
	var jobs struct {
		Jobs []job `json:"jobs"`
	}
	err := g.rest(ctx, http.MethodGet, g.path(fmt.Sprintf("/actions/runs/%d/jobs?per_page=100", n)), nil, &jobs)
	return jobs.Jobs, err
}

func (g *github) viewRun(ctx context.Context, n int64) (*protocol.ForgeDetail, error) {
	var r run
	var jobs []job
	if err := all(
		func() error { return g.rest(ctx, http.MethodGet, g.path(fmt.Sprintf("/actions/runs/%d", n)), nil, &r) },
		func() (err error) { jobs, err = g.jobs(ctx, n); return err },
	); err != nil {
		return nil, err
	}
	d := &protocol.ForgeDetail{Item: r.proto()}
	for _, j := range jobs {
		d.Checks = append(d.Checks, check{Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL}.proto())
	}
	return d, nil
}

// state is a check's status and conclusion, a commit status's state read
// as both.
func (c check) state() (status, conclusion string) {
	switch c.State {
	case "":
		return strings.ToLower(c.Status), strings.ToLower(c.Conclusion)
	case "PENDING", "EXPECTED":
		return "in_progress", ""
	}
	return "completed", strings.ToLower(c.State)
}

// proto is a check, or a job, as a front-end shows it.
func (c check) proto() *protocol.ForgeCheck {
	status, conclusion := c.state()
	return &protocol.ForgeCheck{
		Name: cmp.Or(c.Name, c.Context), Workflow: c.Workflow, Status: status, Conclusion: conclusion,
		Url: cmp.Or(c.DetailsURL, c.TargetURL, c.URL),
	}
}

// rollup is a pull request's checks summed up: failure if any failed,
// else pending if any is not done, else success; "" with none.
func rollup(checks []check) string {
	if len(checks) == 0 {
		return ""
	}
	out := "success"
	for _, c := range checks {
		switch status, conclusion := c.state(); {
		case protocol.Failed(conclusion):
			return "failure"
		case status != "completed":
			out = "pending"
		}
	}
	return out
}

// changeStatus is a pull request file's change type as a GitFile's status:
// GitHub's, ADDED, or Gitea's, added.
func changeStatus(t string) string {
	switch strings.ToUpper(t) {
	case "ADDED":
		return protocol.GitAdded
	case "DELETED":
		return protocol.GitDeleted
	case "RENAMED":
		return protocol.GitRenamed
	}
	return protocol.GitModified
}
