package forge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// What gh gives, as its --json names it.

type user struct {
	Login string `json:"login"`
}

type label struct {
	Name string `json:"name"`
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

// item is a pull request, an issue or a run, whichever fields it has.
type item struct {
	Number      int64     `json:"number"`
	DatabaseID  int64     `json:"databaseId"`
	Title       string    `json:"title"`
	DisplayName string    `json:"displayTitle"`
	Author      user      `json:"author"`
	State       string    `json:"state"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
	IsDraft     bool      `json:"isDraft"`
	HeadRefName string    `json:"headRefName"`
	HeadBranch  string    `json:"headBranch"`
	BaseRefName string    `json:"baseRefName"`
	HeadRefOid  string    `json:"headRefOid"`
	HeadSha     string    `json:"headSha"`
	Rollup      []check   `json:"statusCheckRollup"`
	Labels      []label   `json:"labels"`
	Workflow    string    `json:"workflowName"`
	Event       string    `json:"event"`

	Body     string    `json:"body"`
	Comments []comment `json:"comments"`
	Reviews  []review  `json:"reviews"`
	Files    []file    `json:"files"`
	Jobs     []check   `json:"jobs"`
}

// The fields each kind is read with: a list's, and the more a view adds.
var (
	listFields = map[string]string{
		protocol.ForgePR:    "number,title,author,state,url,updatedAt,isDraft,headRefName,baseRefName,headRefOid,statusCheckRollup,labels",
		protocol.ForgeIssue: "number,title,author,state,url,updatedAt,labels",
		protocol.ForgeRuns:  "databaseId,displayTitle,status,conclusion,url,updatedAt,headBranch,headSha,workflowName,event",
	}
	viewFields = map[string]string{
		protocol.ForgePR:    ",body,comments,reviews,files",
		protocol.ForgeIssue: ",body,comments",
		protocol.ForgeRuns:  ",jobs",
	}
)

func (github) list(ctx context.Context, r *protocol.ForgeList) (*protocol.ForgeItems, error) {
	kind := r.GetKind()
	limit := cmp.Or(r.GetMax(), 50)
	args := []string{kind, "list", "--limit", strconv.FormatInt(min(limit, 200), 10), "--json", listFields[kind]}
	branch := r.GetBranch()
	if r.GetCurrent() {
		if kind == protocol.ForgePR {
			return currentPR(ctx, r.GetRoot())
		}
		if branch = gitstat.Branch(r.GetRoot()); branch == "" {
			return &protocol.ForgeItems{}, nil // not a repository
		}
	}
	if strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("%q is not a branch", branch)
	}
	switch {
	case branch == "":
	case kind == protocol.ForgeRuns:
		args = append(args, "--branch", branch)
	case kind == protocol.ForgePR:
		args = append(args, "--head", branch)
	}
	if kind != protocol.ForgeRuns {
		state := cmp.Or(r.GetState(), "open")
		if !slices.Contains([]string{"open", "closed", "merged", "all"}, state) {
			return nil, fmt.Errorf("%q is not a state to list", state)
		}
		args = append(args, "--state", state)
	}
	out, err := gh(ctx, r.GetRoot(), nil, args...)
	if err != nil {
		return nil, err
	}
	var items []item
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("cannot read what gh listed: %w", err)
	}
	reply := &protocol.ForgeItems{}
	for _, it := range items {
		reply.Items = append(reply.Items, it.proto(kind))
	}
	return reply, nil
}

func (github) view(ctx context.Context, r *protocol.ForgeView) (*protocol.ForgeDetail, error) {
	kind := r.GetKind()
	args := []string{kind, "view"}
	if r.GetNumber() > 0 {
		args = append(args, strconv.FormatInt(r.GetNumber(), 10))
	} else {
		args = append(args, currentBranch(r.GetRoot())...)
	}
	args = append(args, "--json", listFields[kind]+viewFields[kind])

	// A pull request's comments on lines are only in the API's.
	var lines []*protocol.ForgeComment
	var linesErr error
	done := make(chan struct{})
	if kind == protocol.ForgePR && r.GetNumber() > 0 {
		go func() { defer close(done); lines, linesErr = lineComments(ctx, r.GetRoot(), r.GetNumber()) }()
	} else {
		close(done)
	}
	out, err := gh(ctx, r.GetRoot(), nil, args...)
	<-done
	if err != nil {
		if r.GetNumber() == 0 && noPR(err) {
			return &protocol.ForgeDetail{None: true}, nil
		}
		return nil, err
	}
	var it item
	if err := json.Unmarshal(out, &it); err != nil {
		return nil, fmt.Errorf("cannot read what gh gave: %w", err)
	}
	if kind == protocol.ForgePR && r.GetNumber() == 0 {
		// The branch's own, whose line comments are read now its number is
		// known.
		lines, linesErr = lineComments(ctx, r.GetRoot(), it.Number)
	}
	d := &protocol.ForgeDetail{Item: it.proto(kind), Body: it.Body}
	for _, c := range it.Comments {
		d.Comments = append(d.Comments, &protocol.ForgeComment{Author: c.Author.Login, Body: c.Body, Time: protocol.Time(c.CreatedAt)})
	}
	for _, rv := range it.Reviews {
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
	for _, f := range it.Files {
		d.Files = append(d.Files, &protocol.GitFile{Path: f.Path, Status: changeStatus(f.ChangeType), Added: f.Additions, Removed: f.Deletions})
	}
	for _, c := range append(it.Jobs, it.Rollup...) { // a run has jobs, a pull request checks
		d.Checks = append(d.Checks, c.proto())
	}
	return d, nil
}

// currentPR lists the pull request of the branch checked out, as gh finds
// it: by the branch's upstream, and not one of a fork's that has a branch
// of the same name.
func currentPR(ctx context.Context, root string) (*protocol.ForgeItems, error) {
	args := append([]string{"pr", "view"}, currentBranch(root)...)
	out, err := gh(ctx, root, nil, append(args, "--json", listFields[protocol.ForgePR])...)
	if err != nil {
		if noPR(err) {
			return &protocol.ForgeItems{}, nil
		}
		return nil, err
	}
	var it item
	if err := json.Unmarshal(out, &it); err != nil {
		return nil, fmt.Errorf("cannot read what gh gave: %w", err)
	}
	return &protocol.ForgeItems{Items: []*protocol.ForgeItem{it.proto(protocol.ForgePR)}}, nil
}

// currentBranch is what gh pr view is told of the branch checked out:
// nothing, as it finds the branch's pull request itself, unless the
// repository is named to it, when it asks for the branch by name.
func currentBranch(root string) []string {
	if repoOf(root) == "" {
		return nil
	}
	if branch := gitstat.Branch(root); branch != "" {
		return []string{branch}
	}
	return nil
}

// noPR reports gh's saying the branch has no pull request.
func noPR(err error) bool {
	return strings.Contains(err.Error(), "no pull requests found") || strings.Contains(err.Error(), "no open pull requests")
}

// lineComments are a pull request's review comments on lines of its
// change, as many as one page of the API's holds.
func lineComments(ctx context.Context, root string, n int64) ([]*protocol.ForgeComment, error) {
	out, err := gh(ctx, root, nil, "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments?per_page=100", n))
	if err != nil {
		return nil, err
	}
	var list []struct {
		User      user      `json:"user"`
		Body      string    `json:"body"`
		Path      string    `json:"path"`
		Line      int64     `json:"line"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	var cs []*protocol.ForgeComment
	for _, c := range list {
		cs = append(cs, &protocol.ForgeComment{Author: c.User.Login, Body: c.Body, Time: protocol.Time(c.CreatedAt), Path: c.Path, Line: c.Line})
	}
	return cs, nil
}

// proto is an item as a list shows it.
func (it item) proto(kind string) *protocol.ForgeItem {
	p := &protocol.ForgeItem{
		Kind: kind, Number: it.Number, Title: it.Title, Author: it.Author.Login, State: it.State, Url: it.URL,
		Updated: protocol.Time(it.UpdatedAt), Draft: it.IsDraft, Head: it.HeadRefName, Base: it.BaseRefName, HeadOid: it.HeadRefOid,
		Checks: rollup(it.Rollup),
	}
	for _, l := range it.Labels {
		p.Labels = append(p.Labels, l.Name)
	}
	if kind == protocol.ForgeRuns {
		p.Number, p.Title, p.State, p.Checks = it.DatabaseID, it.DisplayName, it.Status, it.Conclusion
		p.Head, p.HeadOid, p.Workflow, p.Event = it.HeadBranch, it.HeadSha, it.Workflow, it.Event
	}
	return p
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
