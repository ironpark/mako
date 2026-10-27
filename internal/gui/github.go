package gui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/github"
	"github.com/ironpark/mako/internal/notify"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's GitHub tab is the project's repository on GitHub, as
// the GitHub CLI of the node it is on sees it: its pull requests, issues
// and Actions runs, each opening in the tab in full, to read, comment on,
// review, check out, run again, or hand to an agent. The Git tab shows the
// branch's pull request and runs, and the runs of the branch are watched
// while the window is open: one that finishes says so.

// ghList is what the GitHub tab lists.
type ghList struct {
	Root        fileRoot
	Kind, State string
	Items       []*protocol.GitHubItem
	Err         string
	Loading     bool
}

// ghOpen is the pull request, issue or run the tab shows in full; none
// when Kind is "".
type ghOpen struct {
	Root    fileRoot
	Kind    string
	Number  int64
	Detail  *protocol.GitHubDetail
	Err     string
	Loading bool
}

// ghBranch is the branch checked out's pull request, and the runs of its
// newest commit.
type ghBranch struct {
	Root fileRoot
	PR   *protocol.GitHubItem
	Runs []*protocol.GitHubItem
	Err  string
}

// runsState sums runs up: pending while any is not done, else failure if
// any failed, else success; "" for none.
func runsState(runs []*protocol.GitHubItem) string {
	if len(runs) == 0 {
		return ""
	}
	out := "success"
	for _, r := range runs {
		switch {
		case r.GetState() != "completed":
			return "pending"
		case github.Failed(r.GetChecks()):
			out = "failure"
		}
	}
	return out
}

// ghState is the GitHub tab's state, and the watch on the branch's runs.
type ghState struct {
	Kind   *ggui.StateValue[string] // what is listed: protocol.GitHubPR, GitHubIssue or GitHubActions
	Filter *ggui.StateValue[string] // open, closed or all
	List   *ggui.StateValue[ghList]
	Open   *ggui.StateValue[ghOpen]
	Branch *ggui.StateValue[ghBranch]
	Busy   *ggui.StateValue[string]
	Note   *ggui.StateValue[gitNote]
	// Comment is what is written on the item open, to comment or review
	// with.
	Comment *ggui.StateValue[string]

	// The new pull request dialog.
	PROpen                  *ggui.StateValue[bool]
	PRTitle, PRBody, PRBase *ggui.StateValue[string]
	PRDraft                 *ggui.StateValue[bool]

	// UI goroutine only: the state each watched commit's runs were last
	// seen in, by project and commit, and when the branch was last read.
	seen          map[string]string
	branchRead    time.Time
	listing       bool // a list is being read
	branchReading bool // and the branch
}

func newGHState() ghState {
	return ghState{
		Kind:    ggui.State(protocol.GitHubPR),
		Filter:  ggui.State("open"),
		List:    ggui.State(ghList{}).WithEqual(deepEqual),
		Open:    ggui.State(ghOpen{}).WithEqual(deepEqual),
		Branch:  ggui.State(ghBranch{}).WithEqual(deepEqual),
		Busy:    ggui.State(""),
		Note:    ggui.State(gitNote{}),
		Comment: ggui.State(""),
		PROpen:  ggui.State(false),
		PRTitle: ggui.State(""),
		PRBody:  ggui.State(""),
		PRBase:  ggui.State(""),
		PRDraft: ggui.State(false),
		seen:    map[string]string{},
	}
}

// Reading -------------------------------------------------------------------------

// loadGHList lists what the tab's kind and filter say of the project's
// repository.
func (m *model) loadGHList(root fileRoot) {
	if root.Path == "" || m.GH.listing {
		return
	}
	kind, state := ggui.Peek(m.GH.Kind), ggui.Peek(m.GH.Filter)
	v := ggui.Peek(m.GH.List)
	if v.Root != root || v.Kind != kind || v.State != state {
		v = ghList{Root: root, Kind: kind, State: state}
	}
	// Loading only shows with nothing listed yet: a list read again stays
	// as it is until the next comes.
	v.Loading = len(v.Items) == 0
	m.GH.List.Set(v)
	m.GH.listing = true
	req := &protocol.GitHubList{NodeId: m.remoteID(root.Node), Root: root.Path, Kind: kind, State: state, Max: 50}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitHubItems, error) {
		return c.GitHubList(ctx, req)
	}, func(items *protocol.GitHubItems, err error) {
		m.GH.listing = false
		cur := ggui.Peek(m.GH.List)
		if cur.Root != root || cur.Kind != kind || cur.State != state {
			return // another list is asked for now
		}
		next := ghList{Root: root, Kind: kind, State: state, Items: items.GetItems()}
		if err != nil {
			next.Items, next.Err = cur.Items, nodeErr(err)
		}
		m.GH.List.Set(next)
	})
}

// openGH shows a pull request, issue or run in full, reading it.
func (m *model) openGH(root fileRoot, kind string, number int64) {
	o := ggui.Peek(m.GH.Open)
	if o.Root != root || o.Kind != kind || o.Number != number {
		o = ghOpen{Root: root, Kind: kind, Number: number}
		m.GH.Comment.Set("")
	}
	o.Loading = o.Detail == nil
	m.GH.Open.Set(o)
	m.DetailsOpen.Set(true)
	m.DetailTab.Set(githubTab)
	m.readGH()
}

// readGH reads the item open again.
func (m *model) readGH() {
	o := ggui.Peek(m.GH.Open)
	if o.Kind == "" {
		return
	}
	req := &protocol.GitHubView{NodeId: m.remoteID(o.Root.Node), Root: o.Root.Path, Kind: o.Kind, Number: o.Number}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitHubDetail, error) {
		return c.GitHubView(ctx, req)
	}, func(d *protocol.GitHubDetail, err error) {
		cur := ggui.Peek(m.GH.Open)
		if cur.Root != o.Root || cur.Kind != o.Kind || cur.Number != o.Number {
			return
		}
		next := ghOpen{Root: o.Root, Kind: o.Kind, Number: o.Number, Detail: d}
		if err != nil {
			next.Detail, next.Err = cur.Detail, nodeErr(err)
		}
		m.GH.Open.Set(next)
	})
}

// diffFrom is where the diffs of the pull request open are read from: its
// change at its head.
func (o ghOpen) diffFrom() diffTab {
	it := o.Detail.GetItem()
	return diffTab{Root: o.Root, Repo: o.Root.Path, Commit: it.GetHeadOid(), Pull: it.GetNumber()}
}

// closeGH goes back from the item open to the list.
func (m *model) closeGH() {
	m.GH.Open.Set(ghOpen{})
	m.loadGHList(ggui.Peek(m.GH.List).Root)
}

// refreshGH reads what the tab shows again.
func (m *model) refreshGH(root fileRoot) {
	switch o := ggui.Peek(m.GH.Open); {
	case o.Kind != "" && o.Root == root:
		m.readGH()
		return
	case o.Kind != "":
		m.GH.Open.Set(ghOpen{}) // another project's
	}
	m.loadGHList(root)
}

// ghRun acts on GitHub, saying what it did, then reads what the tab shows
// again. done runs with what gh printed once it succeeds.
func (m *model) ghRun(root fileRoot, req *protocol.GitHubRun, label string, done func(string)) {
	if root.Path == "" || ggui.Peek(m.GH.Busy) != "" {
		return
	}
	req.NodeId, req.Root = m.remoteID(root.Node), root.Path
	m.GH.Busy.Set(label)
	m.GH.Note.Set(gitNote{})
	fetch(m, gitOpTimeout, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitHubRun(ctx, req)
	}, func(r *protocol.GitResult, err error) {
		m.GH.Busy.Set("")
		if err != nil {
			m.GH.Note.Set(gitNote{Text: nodeErr(err), Err: true})
			return
		}
		if done != nil {
			done(r.GetOutput())
		} else {
			m.GH.Note.Set(gitNote{Text: lastLines(r.GetOutput(), 4)})
		}
		m.refreshGH(root)
		m.loadGHBranch(root)
	})
}

// Watching the branch ---------------------------------------------------------

// The branch's runs are read this often while one is under way, and
// otherwise every ghIdle; a project gh cannot read is asked again after
// ghBackoff.
const (
	ghTick    = 15 * time.Second
	ghIdle    = time.Minute
	ghBackoff = 5 * time.Minute
)

// watchGH follows the selected project's branch on GitHub while the window
// is open.
func watchGH(m *model) {
	root := ggui.Derived(m.filesRoot)
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		if r.Path == "" {
			return nil
		}
		m.loadGHBranch(r)
		return m.every(ghTick, func() {
			b := ggui.Peek(m.GH.Branch)
			wait := ghIdle
			switch {
			case b.Root != r:
				wait = 0
			case b.Err != "":
				wait = ghBackoff
			case runsState(b.Runs) == "pending" || b.PR.GetChecks() == "pending":
				wait = 0
			}
			if time.Since(m.GH.branchRead) >= wait {
				m.loadGHBranch(r)
			}
		})
	})
}

// loadGHBranch reads the branch's pull request and its newest commit's
// runs, telling the desktop of runs that finished since the last read.
func (m *model) loadGHBranch(root fileRoot) {
	if root.Path == "" || m.GH.branchReading {
		return
	}
	m.GH.branchRead, m.GH.branchReading = time.Now(), true
	node := m.remoteID(root.Node)
	type read struct {
		pr   *protocol.GitHubItems
		runs *protocol.GitHubItems
	}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (read, error) {
		var r read
		var prErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.pr, prErr = c.GitHubList(ctx, &protocol.GitHubList{NodeId: node, Root: root.Path, Kind: protocol.GitHubPR, Current: true})
		}()
		runs, err := c.GitHubList(ctx, &protocol.GitHubList{NodeId: node, Root: root.Path, Kind: protocol.GitHubActions, Current: true, Max: 20})
		<-done
		r.runs = runs
		return r, cmp.Or(prErr, err)
	}, func(r read, err error) {
		m.GH.branchReading = false
		if ggui.Untrack(m.filesRoot) != root {
			return
		}
		b := ghBranch{Root: root}
		if err != nil {
			b.Err = nodeErr(err)
			m.GH.Branch.Set(b)
			return
		}
		if items := r.pr.GetItems(); len(items) > 0 {
			b.PR = items[0]
		}
		// The newest commit's runs: those of the commit the newest run is
		// of.
		if runs := r.runs.GetItems(); len(runs) > 0 {
			sha := runs[0].GetHeadOid()
			for _, run := range runs {
				if run.GetHeadOid() == sha {
					b.Runs = append(b.Runs, run)
				}
			}
		}
		m.GH.Branch.Set(b)
		m.noteRuns(b)
	})
}

// noteRuns tells the desktop when the runs of a commit it saw under way
// have finished.
func (m *model) noteRuns(b ghBranch) {
	if len(b.Runs) == 0 {
		return
	}
	run := b.Runs[0]
	key := b.Root.Node + "\x00" + b.Root.Path + "\x00" + run.GetHeadOid()
	state, was := runsState(b.Runs), m.GH.seen[key]
	m.GH.seen[key] = state
	if was != "pending" || state == "pending" || !ggui.Peek(m.Prefs).Notify {
		return
	}
	title := Tf("Checks passed on %s", run.GetHead())
	if state == "failure" {
		title = Tf("Checks failed on %s", run.GetHead())
	}
	var names []string
	for _, r := range b.Runs {
		names = append(names, r.GetWorkflow())
	}
	go notify.Post(title, projectName(b.Root.Path)+" · "+strings.Join(names, ", "), githubTarget(b.Root, run.GetNumber()))
}

// githubTarget names a run, for reveal to open when its notification is
// clicked.
func githubTarget(root fileRoot, run int64) string {
	return fmt.Sprintf("github:%d:%s\x00%s", run, root.Node, root.Path)
}

// revealGH opens the run a notification named.
func (m *model) revealGH(target string) {
	num, rest, _ := strings.Cut(target, ":")
	node, path, _ := strings.Cut(rest, "\x00")
	var n int64
	fmt.Sscan(num, &n)
	m.Page.Set("workspace")
	m.openGH(fileRoot{Node: node, Path: path}, protocol.GitHubActions, n)
}

// Handing work to agents ----------------------------------------------------

// sendToSession puts text in the message box of the session a message
// would go to, or of the session shown, showing its chat.
func (m *model) sendToSession(text string) {
	id := m.chatTarget()
	if id == "" {
		if sel := ggui.Peek(m.Selected); isSessionTab(sel) {
			id = sel
		} else if isSessionTab(m.lastSession) {
			id = m.lastSession
		}
	}
	if id == "" {
		m.GH.Note.Set(gitNote{Text: T("Open a session to send this to."), Err: true})
		return
	}
	m.askAgent(id, text)
}

// startOn opens the new session dialog in the project, its message box to
// hold prompt.
func (m *model) startOn(root fileRoot, prompt string) {
	m.openNew(root.Node, root.Path)
	m.Form.Update(func(f newSession) newSession { f.Prompt = prompt; return f })
}

// issuePrompt asks an agent to resolve an issue.
func issuePrompt(it *protocol.GitHubItem, body string) string {
	return strings.TrimSpace(fmt.Sprintf("Resolve GitHub issue #%d: %s\n%s\n\n%s", it.GetNumber(), it.GetTitle(), it.GetUrl(), body))
}

// reviewPrompt asks an agent to address a pull request's review: its
// reviews' bodies and its comments on lines.
func reviewPrompt(d *protocol.GitHubDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Address the review of pull request #%d: %s\n%s\n", d.GetItem().GetNumber(), d.GetItem().GetTitle(), d.GetItem().GetUrl())
	for _, c := range d.GetComments() {
		if c.GetPath() == "" && c.GetReview() == "" {
			continue // the conversation, not the review
		}
		where := cmp.Or(commentWhere(c), c.GetReview())
		fmt.Fprintf(&b, "\n- %s (%s): %s", where, c.GetAuthor(), strings.TrimSpace(c.GetBody()))
	}
	return b.String()
}

// commentWhere is the file and line a comment is on, "" for one that is
// on none.
func commentWhere(c *protocol.GitHubComment) string {
	switch {
	case c.GetPath() == "":
		return ""
	case c.GetLine() > 0:
		return fmt.Sprintf("%s:%d", c.GetPath(), c.GetLine())
	}
	return c.GetPath()
}

// sendFailedLogs reads the logs of a run's failed steps and hands them to
// a session, to fix what failed.
func (m *model) sendFailedLogs(root fileRoot, run *protocol.GitHubItem) {
	m.ghRun(root, &protocol.GitHubRun{Op: protocol.GitHubOpFailedLogs, Number: run.GetNumber()}, T("Reading the logs…"), func(out string) {
		const most = 400 // lines, the end of the log, where the failure is
		lines := strings.Split(strings.TrimSpace(out), "\n")
		lines = lines[max(len(lines)-most, 0):]
		m.sendToSession(fmt.Sprintf("The %s run on %s failed: %s\nFix what failed. The failed steps' logs end:\n```\n%s\n```",
			run.GetWorkflow(), run.GetHead(), run.GetUrl(), strings.Join(lines, "\n")))
	})
}

// newPRSession starts a session on a pull request's head, in a worktree of
// its own that merges into the pull request's base.
func (m *model) newPRSession(root fileRoot, pr *protocol.GitHubItem) {
	m.ghRun(root, &protocol.GitHubRun{Op: protocol.GitHubOpFetchPR, Number: pr.GetNumber()}, T("Fetching the pull request…"), func(head string) {
		m.openNew(root.Node, root.Path)
		m.Form.Update(func(f newSession) newSession {
			f.Worktree, f.Branch, f.Base, f.Start = true, fmt.Sprintf("pr-%d", pr.GetNumber()), pr.GetBase(), strings.TrimSpace(head)
			return f
		})
	})
}

// startOnIssue starts a session on an issue, its body read first when it
// is not at hand.
func (m *model) startOnIssue(root fileRoot, it *protocol.GitHubItem, d *protocol.GitHubDetail) {
	if d != nil {
		m.startOn(root, issuePrompt(it, d.GetBody()))
		return
	}
	req := &protocol.GitHubView{NodeId: m.remoteID(root.Node), Root: root.Path, Kind: protocol.GitHubIssue, Number: it.GetNumber()}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitHubDetail, error) {
		return c.GitHubView(ctx, req)
	}, func(d *protocol.GitHubDetail, _ error) { m.startOn(root, issuePrompt(it, d.GetBody())) })
}

// ghOps are the busy labels of the operations on an item a button or menu
// runs as it is.
var ghOps = map[string]string{
	protocol.GitHubOpCheckout: "Checking out…",
	protocol.GitHubOpRerun:    "Starting the run again…",
	protocol.GitHubOpCancel:   "Cancelling…",
}

// ghOp runs an operation of ghOps on item n; failed runs only a run's
// failed jobs again.
func (m *model) ghOp(root fileRoot, op string, n int64, failed bool) func() {
	return func() { m.ghRun(root, &protocol.GitHubRun{Op: op, Number: n, Failed: failed}, T(ghOps[op]), nil) }
}

// askPR opens the new pull request dialog for the branch checked out.
func (m *model) askPR() {
	m.GH.PRTitle.Set("")
	m.GH.PRBody.Set("")
	m.GH.PRBase.Set("")
	m.GH.PRDraft.Set(false)
	m.GH.PROpen.Set(true)
}

// submitPR pushes the branch and opens its pull request.
func (m *model) submitPR() {
	root := ggui.Untrack(m.filesRoot)
	req := &protocol.GitHubRun{Op: protocol.GitHubOpCreatePR, Title: strings.TrimSpace(ggui.Peek(m.GH.PRTitle)),
		Body: ggui.Peek(m.GH.PRBody), Base: strings.TrimSpace(ggui.Peek(m.GH.PRBase)), Draft: ggui.Peek(m.GH.PRDraft)}
	m.GH.PROpen.Set(false)
	m.ghRun(root, req, T("Opening the pull request…"), func(out string) {
		m.GH.Note.Set(gitNote{Text: lastLines(out, 2)})
		m.GH.Kind.Set(protocol.GitHubPR)
		// gh prints the new pull request's address last, …/pull/n.
		var n int64
		_, num, _ := strings.Cut(lastLines(out, 1), "/pull/")
		fmt.Sscan(num, &n)
		m.openGH(root, protocol.GitHubPR, n)
	})
}

// View ----------------------------------------------------------------------

func githubPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		m.GH.Kind.Get()
		m.GH.Filter.Get()
		if r.Path == "" || !m.DetailsOpen.Get() || m.DetailTab.Get() != githubTab {
			return nil // nothing shows it
		}
		m.refreshGH(r)
		return m.every(30*time.Second, func() {
			if m.window == nil || ggui.Peek(m.window.Focused()) {
				m.refreshGH(r) // not while the window is in the background
			}
		})
	})
	open := ggui.Map(m.GH.Open, func(o ghOpen) bool { return o.Kind != "" })
	list, detail := ghListView(m, root), ghDetailView(m)
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to see its repository on GitHub.")).Size(13))).Pad(16)
		}
		return ggui.Column(
			ggui.Expanded(ggui.If(open, func() ggui.Widget { return detail }).Else(func() ggui.Widget { return list })),
		).Stretch()
	})
}

func ghListView(m *model, root ggui.Readable[fileRoot]) ggui.Widget {
	kinds := ui.ToggleGroup[string](m.GH.Kind).Options([]string{protocol.GitHubPR, protocol.GitHubIssue, protocol.GitHubActions}).
		Format(func(k string) string {
			return map[string]string{protocol.GitHubPR: T("Pull requests"), protocol.GitHubIssue: T("Issues"), protocol.GitHubActions: T("Actions")}[k]
		}).Name(T("What to list"))
	header := panelHeader(
		icon("github").Size(15),
		ggui.Expanded(ggui.Scroll(kinds).Horizontal().Bar(color.Transparent)),
		iconButton("refresh-cw", T("Refresh"), func() { m.loadGHList(ggui.Peek(root)) }),
	)
	filter := ui.Select(m.GH.Filter).Options([]string{"open", "closed", "all"}).Format(func(s string) string {
		return map[string]string{"open": T("Open"), "closed": T("Closed"), "all": T("All")}[s]
	}).Name(T("Which to list"))
	tools := ggui.View(m.GH.Kind, func(k string) ggui.Widget {
		if k == protocol.GitHubActions {
			return ggui.Box()
		}
		row := []ggui.Widget{ggui.Box(filter).Width(120), ggui.Spacer()}
		if k == protocol.GitHubPR {
			row = append(row, ui.Button(T("New pull request…"), m.askPR).Ghost())
		}
		return ggui.Column(ggui.Box(ggui.Row(row...).Gap(8).Align(ggui.AlignCenter)).Pad(6, 8, 6, 12), hline()).Stretch()
	})
	body := ggui.View(m.GH.List, func(v ghList) ggui.Widget {
		switch {
		case v.Loading && len(v.Items) == 0:
			return ggui.Center(ui.Spinner())
		case v.Err != "" && len(v.Items) == 0:
			return paneNote(v.Err)
		case len(v.Items) == 0:
			return paneNote(map[string]string{protocol.GitHubPR: T("No pull requests."), protocol.GitHubIssue: T("No issues."), protocol.GitHubActions: T("No runs.")}[v.Kind])
		}
		rows := make([]ggui.Widget, 0, len(v.Items)+1)
		if v.Err != "" {
			rows = append(rows, paneNote(v.Err))
		}
		for _, it := range v.Items {
			rows = append(rows, ghRow(m, v.Root, it))
		}
		return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
	})
	return ggui.Column(header, hline(), statusLine(m.GH.Busy, m.GH.Note), tools, ggui.Expanded(body)).Stretch()
}

// ghIcon is an item's kind and state as an icon in its colour.
func ghIcon(it *protocol.GitHubItem) ggui.Widget {
	switch it.GetKind() {
	case protocol.GitHubIssue:
		if it.GetState() == "OPEN" {
			return iconColored("circle-dot", tokDiffAdded).Size(15)
		}
		return iconColored("circle-check", tokAccentBright).Size(15)
	case protocol.GitHubActions:
		return checkIcon(it.GetState(), it.GetChecks())
	}
	switch {
	case it.GetState() == "MERGED":
		return iconColored("git-merge", tokAccentBright).Size(15)
	case it.GetState() == "CLOSED":
		return iconColored("git-pull-request", tokDotDown).Size(15)
	case it.GetDraft():
		return iconColored("git-pull-request", tokExtraMuted).Size(15)
	}
	return iconColored("git-pull-request", tokDiffAdded).Size(15)
}

// checkIcon is a check's, or a run's, state.
func checkIcon(status, conclusion string) ggui.Widget {
	switch {
	case status == "queued" || status == "waiting" || status == "pending" || status == "requested":
		return iconColored("clock", tokDotWaiting).Size(14)
	case status != "completed" && status != "":
		return iconColored("loader-circle", tokDotWaiting).Size(14)
	case github.Failed(conclusion):
		return iconColored("circle-x", tokDotDown).Size(14)
	case conclusion == "success":
		return iconColored("circle-check", tokDiffAdded).Size(14)
	}
	return iconColored("circle-slash", tokExtraMuted).Size(14) // skipped, neutral
}

// rollupIcon is a pull request's checks summed up.
func rollupIcon(checks string) ggui.Widget {
	switch checks {
	case "success":
		return checkIcon("completed", "success")
	case "failure":
		return checkIcon("completed", "failure")
	case "pending":
		return checkIcon("in_progress", "")
	}
	return ggui.Box()
}

// ghMeta is an item's line under its title.
func ghMeta(it *protocol.GitHubItem) string {
	var parts []string
	if it.GetKind() == protocol.GitHubActions {
		parts = append(parts, it.GetWorkflow(), it.GetHead(), it.GetEvent())
	} else {
		parts = append(parts, fmt.Sprintf("#%d", it.GetNumber()), it.GetAuthor())
	}
	return metaLine(it, parts...)
}

// metaLine joins what is said of an item, and when it was last updated,
// leaving out what is empty.
func metaLine(it *protocol.GitHubItem, parts ...string) string {
	if t := protocol.TimeOf(it.GetUpdated()); !t.IsZero() {
		parts = append(parts, ago(t))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// ghRow is an item in the list, which opens it, with a menu of what to do
// with it.
func ghRow(m *model, root fileRoot, it *protocol.GitHubItem) ggui.Widget {
	title := []ggui.Widget{ggui.Expanded(ggui.Text(it.GetTitle()).Size(12.5).NoWrap().Ellipsis())}
	if it.GetKind() == protocol.GitHubPR {
		title = append(title, rollupIcon(it.GetChecks()))
	}
	text := ggui.Column(
		ggui.Row(title...).Gap(6).Align(ggui.AlignCenter),
		ggui.Text(ghMeta(it)).Size(11).Color(tokExtraMuted).NoWrap().Ellipsis(),
	).Gap(2).Stretch()
	row := ggui.Row(ghIcon(it), ggui.Expanded(text)).Gap(8).Align(ggui.AlignStart)
	tap := ui.ButtonOf(row, func() { m.openGH(root, it.GetKind(), it.GetNumber()) }).Ghost().Pad(6, 10, 6, 12).Radius(0).Name(it.GetTitle())
	return ui.ContextMenu(tap, ghItemMenu(m, root, it, nil)...).Name(Tf("%s actions", it.GetTitle()))
}

// ghItemMenu is what can be done with an item, in its row's menu or its
// view's; d is the item in full, when read.
func ghItemMenu(m *model, root fileRoot, it *protocol.GitHubItem, d *protocol.GitHubDetail) []ggui.Widget {
	n := it.GetNumber()
	var items []ggui.Widget
	switch it.GetKind() {
	case protocol.GitHubPR:
		items = append(items,
			ui.MenuItem(T("Check out"), m.ghOp(root, protocol.GitHubOpCheckout, n, false)),
			ui.MenuItem(T("New session in a worktree…"), func() { m.newPRSession(root, it) }),
		)
		if d != nil {
			items = append(items, ui.MenuItem(T("Send review to session"), func() { m.sendToSession(reviewPrompt(d)) }))
		}
	case protocol.GitHubIssue:
		items = append(items, ui.MenuItem(T("Start a session on it…"), func() { m.startOnIssue(root, it, d) }))
	case protocol.GitHubActions:
		if it.GetState() == "completed" {
			items = append(items, ui.MenuItem(T("Run again"), m.ghOp(root, protocol.GitHubOpRerun, n, false)))
			if github.Failed(it.GetChecks()) {
				items = append(items,
					ui.MenuItem(T("Run failed jobs again"), m.ghOp(root, protocol.GitHubOpRerun, n, true)),
					ui.MenuItem(T("Send failed logs to session"), func() { m.sendFailedLogs(root, it) }),
				)
			}
		} else {
			items = append(items, ui.MenuItem(T("Cancel run"), m.ghOp(root, protocol.GitHubOpCancel, n, false)))
		}
	}
	return append(items,
		ui.MenuDivider(),
		ui.MenuItem(T("Open on GitHub"), func() { _ = runtime.OpenURL(it.GetUrl()) }),
		ui.MenuItem(T("Copy link"), func() { ggui.CurrentClipboard().Write(it.GetUrl()) }),
	)
}

func ghDetailView(m *model) ggui.Widget {
	head := ggui.View(m.GH.Open, func(o ghOpen) ggui.Widget {
		label := fmt.Sprintf("#%d", o.Number)
		switch {
		case o.Kind == protocol.GitHubActions:
			label = cmp.Or(o.Detail.GetItem().GetWorkflow(), T("Run"))
		case o.Number == 0 && o.Detail.GetItem() != nil:
			label = fmt.Sprintf("#%d", o.Detail.GetItem().GetNumber())
		}
		children := []ggui.Widget{
			iconButton("arrow-left", T("Back"), m.closeGH),
			ggui.Expanded(ggui.Text(label).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		}
		if it := o.Detail.GetItem(); it != nil {
			children = append(children,
				iconButton("external-link", T("Open on GitHub"), func() { _ = runtime.OpenURL(it.GetUrl()) }),
				iconMenu(ui.MenuOf(icon("ellipsis"), ghItemMenu(m, o.Root, it, o.Detail)...).Width(240).Name(T("GitHub actions")), 6),
			)
		}
		return panelHeader(append(children, iconButton("refresh-cw", T("Refresh"), m.readGH))...)
	})
	body := ggui.View(m.GH.Open, func(o ghOpen) ggui.Widget {
		d := o.Detail
		switch {
		case o.Loading && d == nil:
			return ggui.Center(ui.Spinner())
		case d == nil && o.Err != "":
			return paneNote(o.Err)
		case d.GetNone():
			return ggui.Column(paneNote(T("This branch has no pull request.")),
				ggui.Box(ui.Button(T("New pull request…"), m.askPR)).Pad(0, 12)).Stretch()
		case d == nil:
			return ggui.Box()
		}
		return ghDetailBody(m, o)
	})
	return ggui.Column(head, hline(), statusLine(m.GH.Busy, m.GH.Note), ggui.Expanded(body)).Stretch()
}

// sectionTitle heads a part of an item's view.
func sectionTitle(title string, count int) ggui.Widget {
	label := []ggui.Widget{ggui.Text(title).Size(11.5).Weight(ggui.WeightSemibold).Color(tokMuted).NoWrap()}
	if count > 0 {
		label = append(label, ggui.Text(lineCount(count)).Size(11).Color(tokExtraMuted))
	}
	return ggui.Box(ggui.Row(label...).Gap(6)).Pad(12, 12, 4, 12)
}

// ghStateLabel names an item's state as people say it.
func ghStateLabel(it *protocol.GitHubItem) string {
	switch it.GetKind() {
	case protocol.GitHubActions:
		if it.GetState() != "completed" {
			return map[string]string{"queued": T("Queued"), "in_progress": T("Running")}[it.GetState()]
		}
		return map[string]string{"success": T("Succeeded"), "failure": T("Failed"), "cancelled": T("Cancelled"), "skipped": T("Skipped")}[it.GetChecks()]
	}
	switch {
	case it.GetDraft() && it.GetState() == "OPEN":
		return T("Draft")
	case it.GetState() == "OPEN":
		return T("Open")
	case it.GetState() == "MERGED":
		return T("Merged")
	}
	return T("Closed")
}

func ghDetailBody(m *model, o ghOpen) ggui.Widget {
	d := o.Detail
	it := d.GetItem()
	meta := []string{ghStateLabel(it)}
	switch it.GetKind() {
	case protocol.GitHubPR:
		meta = append(meta, it.GetAuthor(), it.GetHead()+" → "+it.GetBase())
	case protocol.GitHubIssue:
		meta = append(meta, it.GetAuthor())
	default:
		meta = append(meta, it.GetHead(), it.GetEvent())
	}
	top := []ggui.Widget{
		ggui.Row(ghIcon(it), ggui.Expanded(ggui.Text(it.GetTitle()).Size(14).Weight(ggui.WeightSemibold).Selectable())).Gap(8).Align(ggui.AlignStart),
		ggui.Text(metaLine(it, meta...)).Size(11.5).Color(tokExtraMuted).Selectable(),
	}
	if len(it.GetLabels()) > 0 {
		var badges []ggui.Widget
		for _, l := range it.GetLabels() {
			badges = append(badges, ui.Badge(l).Outline())
		}
		top = append(top, ggui.Wrap(badges...).Gap(4))
	}
	if acts := ghActions(m, o.Root, d); acts != nil {
		top = append(top, acts)
	}
	rows := []ggui.Widget{ggui.Box(ggui.Column(top...).Gap(8).Stretch()).Pad(12, 12, 10, 12)}
	if strings.TrimSpace(d.GetBody()) != "" {
		rows = append(rows, hline(), ggui.Box(markdownText(m, d.GetBody())).Pad(12))
	}
	if len(d.GetChecks()) > 0 {
		title := T("Checks")
		if it.GetKind() == protocol.GitHubActions {
			title = T("Jobs")
		}
		rows = append(rows, hline(), sectionTitle(title, len(d.GetChecks())))
		for _, c := range d.GetChecks() {
			name := c.GetName()
			if c.GetWorkflow() != "" && it.GetKind() != protocol.GitHubActions {
				name = c.GetWorkflow() + " / " + name
			}
			row := ggui.Row(checkIcon(c.GetStatus(), c.GetConclusion()), ggui.Expanded(ggui.Text(name).Size(12).NoWrap().Ellipsis())).Gap(7).Align(ggui.AlignCenter)
			rows = append(rows, ui.ButtonOf(row, func() { _ = runtime.OpenURL(c.GetUrl()) }).Ghost().Pad(4, 12).Radius(0).Name(name))
		}
	}
	if len(d.GetFiles()) > 0 {
		rows = append(rows, hline(), sectionTitle(T("Files"), len(d.GetFiles())))
		from := o.diffFrom()
		for _, f := range d.GetFiles() {
			row := ggui.Row(statusIcon(f.GetStatus()), ggui.Expanded(ggui.Text(f.GetPath()).Size(12).NoWrap().Ellipsis()), fileCounts(f)).Gap(7)
			rows = append(rows, ui.ButtonOf(row, func() { m.openDiffFrom(from, f, true) }).Ghost().Pad(4, 12).Radius(0).Name(f.GetPath()))
		}
	}
	if it.GetKind() != protocol.GitHubActions {
		rows = append(rows, hline(), sectionTitle(T("Conversation"), len(d.GetComments())))
		for _, c := range d.GetComments() {
			rows = append(rows, ghComment(m, c))
		}
		rows = append(rows, ghComposer(m, o.Root, it))
	}
	return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
}

// ghActions are the buttons an item's view leads with: what is most often
// done with it.
func ghActions(m *model, root fileRoot, d *protocol.GitHubDetail) ggui.Widget {
	it := d.GetItem()
	n := it.GetNumber()
	var btns []ggui.Widget
	switch it.GetKind() {
	case protocol.GitHubPR:
		if slices.ContainsFunc(d.GetComments(), func(c *protocol.GitHubComment) bool { return c.GetPath() != "" || c.GetReview() == "CHANGES_REQUESTED" }) {
			btns = append(btns, ui.Button(T("Send review to session"), func() { m.sendToSession(reviewPrompt(d)) }).Outline())
		}
		btns = append(btns, ui.Button(T("Check out"), m.ghOp(root, protocol.GitHubOpCheckout, n, false)).Ghost())
	case protocol.GitHubIssue:
		btns = append(btns, ui.Button(T("Start a session on it…"), func() { m.startOnIssue(root, it, d) }).Outline())
	case protocol.GitHubActions:
		switch {
		case it.GetState() != "completed":
			btns = append(btns, ui.Button(T("Cancel run"), m.ghOp(root, protocol.GitHubOpCancel, n, false)).Ghost())
		case github.Failed(it.GetChecks()):
			btns = append(btns,
				ui.Button(T("Send failed logs to session"), func() { m.sendFailedLogs(root, it) }).Outline(),
				ui.Button(T("Run failed jobs again"), m.ghOp(root, protocol.GitHubOpRerun, n, true)).Ghost())
		default:
			btns = append(btns, ui.Button(T("Run again"), m.ghOp(root, protocol.GitHubOpRerun, n, false)).Ghost())
		}
	}
	if len(btns) == 0 {
		return nil
	}
	return ggui.Wrap(btns...).Gap(6)
}

// ghComment is a comment, a review, or a review's comment on a line.
func ghComment(m *model, c *protocol.GitHubComment) ggui.Widget {
	head := []string{c.GetAuthor()}
	switch c.GetReview() {
	case "APPROVED":
		head = append(head, T("approved"))
	case "CHANGES_REQUESTED":
		head = append(head, T("requested changes"))
	case "COMMENTED":
		head = append(head, T("reviewed"))
	}
	if where := commentWhere(c); where != "" {
		head = append(head, where)
	}
	if t := protocol.TimeOf(c.GetTime()); !t.IsZero() {
		head = append(head, ago(t))
	}
	rows := []ggui.Widget{ggui.Row(
		iconColored("message-square", tokExtraMuted).Size(13),
		ggui.Expanded(ggui.Text(strings.Join(head, " · ")).Size(11.5).Color(tokMuted).NoWrap().Ellipsis()),
	).Gap(6).Align(ggui.AlignCenter)}
	if strings.TrimSpace(c.GetBody()) != "" {
		rows = append(rows, markdownText(m, c.GetBody()))
	}
	return ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).Pad(8, 12)
}

// ghComposer writes a comment on a pull request or an issue, or a review
// of a pull request.
func ghComposer(m *model, root fileRoot, it *protocol.GitHubItem) ggui.Widget {
	n := it.GetNumber()
	send := func(op, review, label string) func() {
		return func() {
			body := ggui.Peek(m.GH.Comment)
			m.ghRun(root, &protocol.GitHubRun{Op: op, Number: n, Kind: it.GetKind(), Review: review, Body: body}, label, func(out string) {
				m.GH.Comment.Set("")
				m.GH.Note.Set(gitNote{Text: lastLines(out, 2)})
			})
		}
	}
	field := ui.TextField(m.GH.Comment).Multiline().Lines(3).Placeholder(T("Leave a comment")).Name(T("Comment")).
		OnSubmit(func(string) { send(protocol.GitHubOpComment, "", T("Commenting…"))() })
	btns := []ggui.Widget{ggui.Spacer()}
	if it.GetKind() == protocol.GitHubPR {
		btns = append(btns, borderless(ui.MenuOf(ggui.Row(ggui.Text(T("Review")).Size(13), icon("chevron-down").Size(12)).Gap(4),
			ui.MenuItem(T("Approve"), send(protocol.GitHubOpReview, "approve", T("Reviewing…"))),
			ui.MenuItem(T("Request changes"), send(protocol.GitHubOpReview, "request_changes", T("Reviewing…"))),
			ui.MenuItem(T("Comment as a review"), send(protocol.GitHubOpReview, "comment", T("Reviewing…"))),
		).Name(T("Review"))))
	}
	btns = append(btns, ui.Button(T("Comment"), send(protocol.GitHubOpComment, "", T("Commenting…"))))
	return ggui.Box(ggui.Column(field, ggui.Row(btns...).Gap(8).Align(ggui.AlignCenter)).Gap(8).Stretch()).Pad(8, 12, 16, 12)
}

// ghBranchLine is the Git tab's line for the branch on GitHub: its pull
// request, or a button to open one, and its newest commit's runs.
func ghBranchLine(m *model) ggui.Widget {
	return ggui.View(m.GH.Branch, func(b ghBranch) ggui.Widget {
		if b.Root.Path == "" || b.Err != "" || ggui.Untrack(m.filesRoot) != b.Root {
			return ggui.Box()
		}
		var parts []ggui.Widget
		if pr := b.PR; pr != nil {
			label := ggui.Row(ghIcon(pr), ggui.Box(ggui.Text(fmt.Sprintf("#%d %s", pr.GetNumber(), pr.GetTitle())).Size(12).NoWrap().Ellipsis()).MaxWidth(200), rollupIcon(pr.GetChecks())).Gap(6).Align(ggui.AlignCenter)
			parts = append(parts, ui.ButtonOf(label, func() { m.openGH(b.Root, protocol.GitHubPR, pr.GetNumber()) }).Ghost().Pad(3, 6).Radius(6).Name(pr.GetTitle()))
		} else {
			parts = append(parts, ui.ButtonOf(ggui.Row(iconColored("git-pull-request", tokMuted).Size(14), muted(ggui.Text(T("Create pull request")).Size(12))).Gap(6),
				m.askPR).Ghost().Pad(3, 6).Radius(6).Name(T("Create pull request")))
		}
		parts = append(parts, ggui.Spacer())
		if len(b.Runs) > 0 {
			state := runsState(b.Runs)
			label := map[string]string{"pending": T("Running"), "failure": T("Failed"), "success": T("Passed")}[state]
			icon := rollupIcon(state)
			first := b.Runs[0]
			for _, r := range b.Runs {
				if r.GetState() != "completed" || github.Failed(r.GetChecks()) {
					first = r // the run to look at
					break
				}
			}
			parts = append(parts, ui.Tooltip(ui.ButtonOf(ggui.Row(icon, muted(ggui.Text(label).Size(12))).Gap(5),
				func() { m.openGH(b.Root, protocol.GitHubActions, first.GetNumber()) }).Ghost().Pad(3, 6).Radius(6).Name(T("Actions")),
				Tf("%d runs of %s", len(b.Runs), shortHash(first.GetHeadOid()))))
		}
		return ggui.Column(ggui.Box(ggui.Row(parts...).Gap(4).Align(ggui.AlignCenter)).Pad(2, 6, 2, 6), hline()).Stretch()
	})
}

// ghDialogs is the new pull request dialog.
func ghDialogs(m *model) ggui.Widget {
	return ui.Dialog(m.GH.PROpen, ggui.Column(
		ui.Field(T("Title"), ui.TextField(m.GH.PRTitle).Placeholder(T("From the commits, when left empty")).Name(T("Title"))),
		ui.Field(T("Description"), ui.TextField(m.GH.PRBody).Multiline().Lines(5).Name(T("Description"))),
		ui.Field(T("Base branch"), ui.TextField(m.GH.PRBase).Placeholder(T("The repository's default branch")).Name(T("Base branch"))),
		ui.Checkbox(m.GH.PRDraft, T("Open as a draft")),
		muted(ggui.Text(T("The branch is pushed first, its upstream set if it has none.")).Size(12)),
		ggui.Row(
			ui.Button(T("Cancel"), func() { m.GH.PROpen.Set(false) }).Outline(),
			ui.Button(T("Create pull request"), m.submitPR),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(14).Stretch()).Title(T("New pull request")).Width(480)
}
