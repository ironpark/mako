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
	"github.com/ironpark/mako/internal/notify"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's forge tab is the project's repository on its forge,
// GitHub, or a Forgejo or Gitea, as its node reaches it: its pull requests,
// issues and Actions runs, each opening in the tab in full, to read,
// comment on, review, check out, run again, or hand to an agent. The Git tab shows the
// branch's pull request and runs, and the runs of the branch are watched
// while the window is open: one that finishes says so.

// forgeList is what the forge tab lists.
type forgeList struct {
	Root        fileRoot
	Forge       string   // which answered: github, forgejo or gitea
	Ops         []string // what it can do
	Kind, State string
	Items       []*protocol.ForgeItem
	Err         string
	Loading     bool
}

// forgeOpen is the pull request, issue or run the tab shows in full; none
// when Kind is "".
type forgeOpen struct {
	Root    fileRoot
	Kind    string
	Number  int64
	Detail  *protocol.ForgeDetail
	Err     string
	Loading bool
}

// forgeBranch is the branch checked out's pull request, and the runs of its
// newest commit.
type forgeBranch struct {
	Root  fileRoot
	Forge string
	PR    *protocol.ForgeItem
	Runs  []*protocol.ForgeItem
	Err   string
}

// forgeLabel names a forge as its users do.
func forgeLabel(forge string) string {
	switch forge {
	case "forgejo":
		return "Forgejo"
	case "gitea":
		return "Gitea"
	}
	return "GitHub"
}

// forgeIcon is a forge's icon.
func forgeIcon(forge string) string {
	if forge == "github" || forge == "" {
		return "github"
	}
	return "git-merge"
}

// forgeOf is the forge of the selected project, once known.
func (m *model) forgeOf() string {
	root := m.filesRoot()
	if l := m.Forge.List.Get(); l.Root == root && l.Forge != "" {
		return l.Forge
	}
	if b := m.Forge.Branch.Get(); b.Root == root {
		return b.Forge
	}
	return ""
}

// runsState sums runs up: pending while any is not done, else failure if
// any failed, else success; "" for none.
func runsState(runs []*protocol.ForgeItem) string {
	if len(runs) == 0 {
		return ""
	}
	out := "success"
	for _, r := range runs {
		switch {
		case r.GetState() != "completed":
			return "pending"
		case protocol.Failed(r.GetChecks()):
			out = "failure"
		}
	}
	return out
}

// forgeState is the forge tab's state, and the watch on the branch's runs.
type forgeState struct {
	Kind   *ggui.StateValue[string] // what is listed: protocol.ForgePR, ForgeIssue or ForgeRuns
	Filter *ggui.StateValue[string] // open, closed or all
	List   *ggui.StateValue[forgeList]
	Open   *ggui.StateValue[forgeOpen]
	Branch *ggui.StateValue[forgeBranch]
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

func newForgeState() forgeState {
	return forgeState{
		Kind:    ggui.State(protocol.ForgePR),
		Filter:  ggui.State("open"),
		List:    ggui.State(forgeList{}).WithEqual(deepEqual),
		Open:    ggui.State(forgeOpen{}).WithEqual(deepEqual),
		Branch:  ggui.State(forgeBranch{}).WithEqual(deepEqual),
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

// loadForgeList lists what the tab's kind and filter say of the project's
// repository.
func (m *model) loadForgeList(root fileRoot) {
	if root.Path == "" || m.Forge.listing {
		return
	}
	kind, state := ggui.Peek(m.Forge.Kind), ggui.Peek(m.Forge.Filter)
	v := ggui.Peek(m.Forge.List)
	if v.Root != root || v.Kind != kind || v.State != state {
		v = forgeList{Root: root, Kind: kind, State: state}
	}
	// Loading only shows with nothing listed yet: a list read again stays
	// as it is until the next comes.
	v.Loading = len(v.Items) == 0
	m.Forge.List.Set(v)
	m.Forge.listing = true
	req := &protocol.ForgeList{NodeId: m.remoteID(root.Node), Root: root.Path, Kind: kind, State: state, Max: 50}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.ForgeItems, error) {
		return c.ForgeList(ctx, req)
	}, func(items *protocol.ForgeItems, err error) {
		m.Forge.listing = false
		cur := ggui.Peek(m.Forge.List)
		if cur.Root != root || cur.Kind != kind || cur.State != state {
			return // another list is asked for now
		}
		next := forgeList{Root: root, Forge: items.GetForge(), Ops: items.GetOps(), Kind: kind, State: state, Items: items.GetItems()}
		if err != nil {
			next.Items, next.Err = cur.Items, nodeErr(err)
		}
		m.Forge.List.Set(next)
	})
}

// openForge shows a pull request, issue or run in full, reading it.
func (m *model) openForge(root fileRoot, kind string, number int64) {
	o := ggui.Peek(m.Forge.Open)
	if o.Root != root || o.Kind != kind || o.Number != number {
		o = forgeOpen{Root: root, Kind: kind, Number: number}
		m.Forge.Comment.Set("")
	}
	o.Loading = o.Detail == nil
	m.Forge.Open.Set(o)
	m.DetailsOpen.Set(true)
	m.DetailTab.Set(forgeTab)
	m.readForge()
}

// readForge reads the item open again.
func (m *model) readForge() {
	o := ggui.Peek(m.Forge.Open)
	if o.Kind == "" {
		return
	}
	req := &protocol.ForgeView{NodeId: m.remoteID(o.Root.Node), Root: o.Root.Path, Kind: o.Kind, Number: o.Number}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.ForgeDetail, error) {
		return c.ForgeView(ctx, req)
	}, func(d *protocol.ForgeDetail, err error) {
		cur := ggui.Peek(m.Forge.Open)
		if cur.Root != o.Root || cur.Kind != o.Kind || cur.Number != o.Number {
			return
		}
		next := forgeOpen{Root: o.Root, Kind: o.Kind, Number: o.Number, Detail: d}
		if err != nil {
			next.Detail, next.Err = cur.Detail, nodeErr(err)
		}
		m.Forge.Open.Set(next)
	})
}

// diffFrom is where the diffs of the pull request open are read from: its
// change at its head.
func (o forgeOpen) diffFrom() diffTab {
	it := o.Detail.GetItem()
	return diffTab{Root: o.Root, Repo: o.Root.Path, Commit: it.GetHeadOid(), Pull: it.GetNumber()}
}

// closeForge goes back from the item open to the list.
func (m *model) closeForge() {
	m.Forge.Open.Set(forgeOpen{})
	m.loadForgeList(ggui.Peek(m.Forge.List).Root)
}

// refreshForge reads what the tab shows again.
func (m *model) refreshForge(root fileRoot) {
	switch o := ggui.Peek(m.Forge.Open); {
	case o.Kind != "" && o.Root == root:
		m.readForge()
		return
	case o.Kind != "":
		m.Forge.Open.Set(forgeOpen{}) // another project's
	}
	m.loadForgeList(root)
}

// forgeRun acts on the forge, saying what it did, then reads what the tab
// shows again. done runs with what the forge said once it succeeds.
func (m *model) forgeRun(root fileRoot, req *protocol.ForgeRun, label string, done func(string)) {
	if root.Path == "" || ggui.Peek(m.Forge.Busy) != "" {
		return
	}
	req.NodeId, req.Root = m.remoteID(root.Node), root.Path
	m.Forge.Busy.Set(label)
	m.Forge.Note.Set(gitNote{})
	fetch(m, gitOpTimeout, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.ForgeRun(ctx, req)
	}, func(r *protocol.GitResult, err error) {
		m.Forge.Busy.Set("")
		if err != nil {
			m.Forge.Note.Set(gitNote{Text: nodeErr(err), Err: true})
			return
		}
		if done != nil {
			done(r.GetOutput())
		} else {
			m.Forge.Note.Set(gitNote{Text: lastLines(r.GetOutput(), 4)})
		}
		m.refreshForge(root)
		m.loadForgeBranch(root)
	})
}

// Watching the branch ---------------------------------------------------------

// The branch's runs are read this often while one is under way, and
// otherwise every forgeIdle; a project gh cannot read is asked again after
// forgeBackoff.
const (
	forgeTick    = 15 * time.Second
	forgeIdle    = time.Minute
	forgeBackoff = 5 * time.Minute
)

// watchForge follows the selected project's branch on its forge while the window
// is open.
func watchForge(m *model) {
	root := ggui.Derived(m.filesRoot)
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		if r.Path == "" {
			return nil
		}
		m.loadForgeBranch(r)
		return m.every(forgeTick, func() {
			b := ggui.Peek(m.Forge.Branch)
			wait := forgeIdle
			switch {
			case b.Root != r:
				wait = 0
			case b.Err != "":
				wait = forgeBackoff
			case runsState(b.Runs) == "pending" || b.PR.GetChecks() == "pending":
				wait = 0
			}
			if time.Since(m.Forge.branchRead) >= wait {
				m.loadForgeBranch(r)
			}
		})
	})
}

// loadForgeBranch reads the branch's pull request and its newest commit's
// runs, telling the desktop of runs that finished since the last read.
func (m *model) loadForgeBranch(root fileRoot) {
	if root.Path == "" || m.Forge.branchReading {
		return
	}
	m.Forge.branchRead, m.Forge.branchReading = time.Now(), true
	node := m.remoteID(root.Node)
	type read struct {
		pr   *protocol.ForgeItems
		runs *protocol.ForgeItems
	}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (read, error) {
		var r read
		var prErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.pr, prErr = c.ForgeList(ctx, &protocol.ForgeList{NodeId: node, Root: root.Path, Kind: protocol.ForgePR, Current: true})
		}()
		runs, err := c.ForgeList(ctx, &protocol.ForgeList{NodeId: node, Root: root.Path, Kind: protocol.ForgeRuns, Current: true, Max: 20})
		<-done
		r.runs = runs
		return r, cmp.Or(prErr, err)
	}, func(r read, err error) {
		m.Forge.branchReading = false
		if ggui.Untrack(m.filesRoot) != root {
			return
		}
		b := forgeBranch{Root: root, Forge: cmp.Or(r.runs.GetForge(), r.pr.GetForge())}
		if err != nil {
			b.Err = nodeErr(err)
			m.Forge.Branch.Set(b)
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
		m.Forge.Branch.Set(b)
		m.noteRuns(b)
	})
}

// noteRuns tells the desktop when the runs of a commit it saw under way
// have finished.
func (m *model) noteRuns(b forgeBranch) {
	if len(b.Runs) == 0 {
		return
	}
	run := b.Runs[0]
	key := b.Root.Node + "\x00" + b.Root.Path + "\x00" + run.GetHeadOid()
	state, was := runsState(b.Runs), m.Forge.seen[key]
	m.Forge.seen[key] = state
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
	go notify.Post(title, projectName(b.Root.Path)+" · "+strings.Join(names, ", "), forgeTarget(b.Root, run.GetNumber()))
}

// forgeTarget names a run, for reveal to open when its notification is
// clicked.
func forgeTarget(root fileRoot, run int64) string {
	return fmt.Sprintf("forge:%d:%s\x00%s", run, root.Node, root.Path)
}

// revealForge opens the run a notification named.
func (m *model) revealForge(target string) {
	num, rest, _ := strings.Cut(target, ":")
	node, path, _ := strings.Cut(rest, "\x00")
	var n int64
	fmt.Sscan(num, &n)
	m.Page.Set("workspace")
	m.openForge(fileRoot{Node: node, Path: path}, protocol.ForgeRuns, n)
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
		m.Forge.Note.Set(gitNote{Text: T("Open a session to send this to."), Err: true})
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
func issuePrompt(it *protocol.ForgeItem, body string) string {
	return strings.TrimSpace(fmt.Sprintf("Resolve issue #%d: %s\n%s\n\n%s", it.GetNumber(), it.GetTitle(), it.GetUrl(), body))
}

// reviewPrompt asks an agent to address a pull request's review: its
// reviews' bodies and its comments on lines.
func reviewPrompt(d *protocol.ForgeDetail) string {
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
func commentWhere(c *protocol.ForgeComment) string {
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
func (m *model) sendFailedLogs(root fileRoot, run *protocol.ForgeItem) {
	m.forgeRun(root, &protocol.ForgeRun{Op: protocol.ForgeOpFailedLogs, Number: run.GetNumber()}, T("Reading the logs…"), func(out string) {
		const most = 400 // lines, the end of the log, where the failure is
		lines := strings.Split(strings.TrimSpace(out), "\n")
		lines = lines[max(len(lines)-most, 0):]
		m.sendToSession(fmt.Sprintf("The %s run on %s failed: %s\nFix what failed. The failed steps' logs end:\n```\n%s\n```",
			run.GetWorkflow(), run.GetHead(), run.GetUrl(), strings.Join(lines, "\n")))
	})
}

// newPRSession starts a session on a pull request's head, in a worktree of
// its own that merges into the pull request's base.
func (m *model) newPRSession(root fileRoot, pr *protocol.ForgeItem) {
	m.forgeRun(root, &protocol.ForgeRun{Op: protocol.ForgeOpFetchPR, Number: pr.GetNumber()}, T("Fetching the pull request…"), func(head string) {
		m.openNew(root.Node, root.Path)
		m.Form.Update(func(f newSession) newSession {
			f.Worktree, f.Branch, f.Base, f.Start = true, fmt.Sprintf("pr-%d", pr.GetNumber()), pr.GetBase(), strings.TrimSpace(head)
			return f
		})
	})
}

// startOnIssue starts a session on an issue, its body read first when it
// is not at hand.
func (m *model) startOnIssue(root fileRoot, it *protocol.ForgeItem, d *protocol.ForgeDetail) {
	if d != nil {
		m.startOn(root, issuePrompt(it, d.GetBody()))
		return
	}
	req := &protocol.ForgeView{NodeId: m.remoteID(root.Node), Root: root.Path, Kind: protocol.ForgeIssue, Number: it.GetNumber()}
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.ForgeDetail, error) {
		return c.ForgeView(ctx, req)
	}, func(d *protocol.ForgeDetail, _ error) { m.startOn(root, issuePrompt(it, d.GetBody())) })
}

// forgeOps are the busy labels of the operations on an item a button or menu
// runs as it is.
var forgeOps = map[string]string{
	protocol.ForgeOpCheckout: "Checking out…",
	protocol.ForgeOpRerun:    "Starting the run again…",
	protocol.ForgeOpCancel:   "Cancelling…",
}

// forgeOp runs an operation of forgeOps on item n; failed runs only a run's
// failed jobs again.
func (m *model) forgeOp(root fileRoot, op string, n int64, failed bool) func() {
	return func() { m.forgeRun(root, &protocol.ForgeRun{Op: op, Number: n, Failed: failed}, T(forgeOps[op]), nil) }
}

// askPR opens the new pull request dialog for the branch checked out.
func (m *model) askPR() {
	m.Forge.PRTitle.Set("")
	m.Forge.PRBody.Set("")
	m.Forge.PRBase.Set("")
	m.Forge.PRDraft.Set(false)
	m.Forge.PROpen.Set(true)
}

// submitPR pushes the branch and opens its pull request.
func (m *model) submitPR() {
	root := ggui.Untrack(m.filesRoot)
	req := &protocol.ForgeRun{Op: protocol.ForgeOpCreatePR, Title: strings.TrimSpace(ggui.Peek(m.Forge.PRTitle)),
		Body: ggui.Peek(m.Forge.PRBody), Base: strings.TrimSpace(ggui.Peek(m.Forge.PRBase)), Draft: ggui.Peek(m.Forge.PRDraft)}
	m.Forge.PROpen.Set(false)
	m.forgeRun(root, req, T("Opening the pull request…"), func(out string) {
		m.Forge.Note.Set(gitNote{Text: lastLines(out, 2)})
		m.Forge.Kind.Set(protocol.ForgePR)
		// The forge answers with the new pull request's address,
		// …/pull/n or …/pulls/n, last.
		var n int64
		addr := lastLines(out, 1)
		fmt.Sscan(addr[strings.LastIndexByte(addr, '/')+1:], &n)
		m.openForge(root, protocol.ForgePR, n)
	})
}

// View ----------------------------------------------------------------------

func forgePanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		m.Forge.Kind.Get()
		m.Forge.Filter.Get()
		if r.Path == "" || !m.DetailsOpen.Get() || m.DetailTab.Get() != forgeTab {
			return nil // nothing shows it
		}
		m.refreshForge(r)
		return m.every(30*time.Second, func() {
			if m.window == nil || ggui.Peek(m.window.Focused()) {
				m.refreshForge(r) // not while the window is in the background
			}
		})
	})
	open := ggui.Map(m.Forge.Open, func(o forgeOpen) bool { return o.Kind != "" })
	list, detail := forgeListView(m, root), forgeDetailView(m)
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to see its repository's pull requests, issues and runs.")).Size(13))).Pad(16)
		}
		return ggui.Column(
			ggui.Expanded(ggui.If(open, func() ggui.Widget { return detail }).Else(func() ggui.Widget { return list })),
		).Stretch()
	})
}

func forgeListView(m *model, root ggui.Readable[fileRoot]) ggui.Widget {
	// One line: what is listed, as flat chips; which of it, as a menu; and
	// making a pull request and reading the list again.
	name := ggui.Derived(m.forgeOf) // read here, the header is built again only as it changes
	header := ggui.Reactive(func() ggui.Widget {
		kind, filter := m.Forge.Kind.Get(), m.Forge.Filter.Get()
		row := []ggui.Widget{ggui.Box(icon(forgeIcon(name.Get())).Size(15)).Pad(0, 6, 0, 0)}
		for _, k := range []string{protocol.ForgePR, protocol.ForgeIssue, protocol.ForgeRuns} {
			row = append(row, kindChip(forgeKindLabel(k), k == kind, func() { m.Forge.Kind.Set(k) }))
		}
		row = append(row, ggui.Spacer())
		if kind != protocol.ForgeRuns {
			var items []ggui.Widget
			for _, f := range []string{"open", "closed", "all"} {
				label := forgeFilterLabel(f)
				if f == filter {
					label = "✓ " + label
				}
				items = append(items, ui.MenuItem(label, func() { m.Forge.Filter.Set(f) }))
			}
			face := ggui.Row(muted(ggui.Text(forgeFilterLabel(filter)).Size(12.5).NoWrap()), icon("chevron-down").Size(12).Color(tokExtraMuted)).Gap(3)
			row = append(row, borderless(ui.MenuOf(face, items...).Name(T("Which to list"))))
		}
		if kind == protocol.ForgePR {
			row = append(row, iconButton("plus", T("New pull request…"), m.askPR))
		}
		row = append(row, iconButton("refresh-cw", T("Refresh"), func() { m.loadForgeList(ggui.Peek(root)) }))
		return ggui.Box(ggui.Row(row...).Gap(2).Align(ggui.AlignCenter)).Height(tabBarHeight).Pad(0, 6, 0, 12)
	})
	body := ggui.View(m.Forge.List, func(v forgeList) ggui.Widget {
		switch {
		case v.Loading && len(v.Items) == 0:
			return ggui.Center(ui.Spinner())
		case v.Err != "" && len(v.Items) == 0:
			return paneNote(v.Err)
		case len(v.Items) == 0:
			return paneNote(map[string]string{protocol.ForgePR: T("No pull requests."), protocol.ForgeIssue: T("No issues."), protocol.ForgeRuns: T("No runs.")}[v.Kind])
		}
		rows := make([]ggui.Widget, 0, len(v.Items)+1)
		if v.Err != "" {
			rows = append(rows, paneNote(v.Err))
		}
		for _, it := range v.Items {
			rows = append(rows, forgeRow(m, v, it))
		}
		return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
	})
	return ggui.Column(header, hline(), statusLine(m.Forge.Busy, m.Forge.Note), ggui.Expanded(body)).Stretch()
}

// forgeKindLabel names what the forge tab lists.
func forgeKindLabel(kind string) string {
	return map[string]string{protocol.ForgePR: T("Pull requests"), protocol.ForgeIssue: T("Issues"), protocol.ForgeRuns: T("Actions")}[kind]
}

// forgeFilterLabel names which of it the tab lists.
func forgeFilterLabel(filter string) string {
	return map[string]string{"open": T("Open"), "closed": T("Closed"), "all": T("All")}[filter]
}

// kindChip is a flat choice in a panel's header, filled while chosen.
func kindChip(label string, on bool, onTap func()) ggui.Widget {
	text := ggui.Text(label).Size(12.5).NoWrap().Color(pick[color.Color](on, tokFg, tokMuted))
	face := ggui.Box(text).Pad(4, 8).Radius(6)
	if on {
		face = face.Fill(tokSurface2)
	}
	return ui.ButtonOf(face, onTap).Ghost().Pad(0).Radius(6).Name(label)
}

// itemIcon is an item's kind and state as an icon in its colour.
func itemIcon(it *protocol.ForgeItem) ggui.Widget {
	switch it.GetKind() {
	case protocol.ForgeIssue:
		if it.GetState() == "OPEN" {
			return iconColored("circle-dot", tokDiffAdded).Size(15)
		}
		return iconColored("circle-check", tokAccentBright).Size(15)
	case protocol.ForgeRuns:
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
	case protocol.Failed(conclusion):
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

// forgeMeta is an item's line under its title.
func forgeMeta(it *protocol.ForgeItem) string {
	var parts []string
	if it.GetKind() == protocol.ForgeRuns {
		if it.GetWorkflow() != it.GetTitle() { // a scheduled run is titled by its workflow
			parts = append(parts, it.GetWorkflow())
		}
		parts = append(parts, it.GetHead(), it.GetEvent())
	} else {
		parts = append(parts, fmt.Sprintf("#%d", it.GetNumber()), it.GetAuthor())
	}
	return metaLine(it, parts...)
}

// metaLine joins what is said of an item, and when it was last updated,
// leaving out what is empty.
func metaLine(it *protocol.ForgeItem, parts ...string) string {
	if t := protocol.TimeOf(it.GetUpdated()); !t.IsZero() {
		parts = append(parts, ago(t))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// forgeRow is an item in the list, which opens it, with a menu of what to do
// with it.
func forgeRow(m *model, v forgeList, it *protocol.ForgeItem) ggui.Widget {
	root := v.Root
	title := []ggui.Widget{ggui.Expanded(ggui.Text(it.GetTitle()).Size(12.5).NoWrap().Ellipsis())}
	if it.GetKind() == protocol.ForgePR {
		title = append(title, rollupIcon(it.GetChecks()))
	}
	text := ggui.Column(
		ggui.Row(title...).Gap(6).Align(ggui.AlignCenter),
		ggui.Text(forgeMeta(it)).Size(11).Color(tokExtraMuted).NoWrap().Ellipsis(),
	).Gap(2).Stretch()
	row := ggui.Row(itemIcon(it), ggui.Expanded(text)).Gap(8).Align(ggui.AlignStart)
	tap := ui.ButtonOf(row, func() { m.openForge(root, it.GetKind(), it.GetNumber()) }).Ghost().Pad(6, 10, 6, 12).Radius(0).Name(it.GetTitle())
	return ui.ContextMenu(tap, forgeItemMenu(m, root, v.Forge, v.Ops, it, nil)...).Name(Tf("%s actions", it.GetTitle()))
}

// forgeItemMenu is what can be done with an item on its forge, in its row's
// menu or its view's; d is the item in full, when read.
func forgeItemMenu(m *model, root fileRoot, forgeName string, ops []string, it *protocol.ForgeItem, d *protocol.ForgeDetail) []ggui.Widget {
	n := it.GetNumber()
	var items []ggui.Widget
	switch it.GetKind() {
	case protocol.ForgePR:
		items = append(items,
			ui.MenuItem(T("Check out"), m.forgeOp(root, protocol.ForgeOpCheckout, n, false)),
			ui.MenuItem(T("New session in a worktree…"), func() { m.newPRSession(root, it) }),
		)
		if d != nil {
			items = append(items, ui.MenuItem(T("Send review to session"), func() { m.sendToSession(reviewPrompt(d)) }))
		}
	case protocol.ForgeIssue:
		items = append(items, ui.MenuItem(T("Start a session on it…"), func() { m.startOnIssue(root, it, d) }))
	case protocol.ForgeRuns:
		if !slices.Contains(ops, protocol.ForgeOpRerun) {
			break // the forge only shows its runs
		}
		if it.GetState() == "completed" {
			items = append(items, ui.MenuItem(T("Run again"), m.forgeOp(root, protocol.ForgeOpRerun, n, false)))
			if protocol.Failed(it.GetChecks()) {
				items = append(items,
					ui.MenuItem(T("Run failed jobs again"), m.forgeOp(root, protocol.ForgeOpRerun, n, true)),
					ui.MenuItem(T("Send failed logs to session"), func() { m.sendFailedLogs(root, it) }),
				)
			}
		} else {
			items = append(items, ui.MenuItem(T("Cancel run"), m.forgeOp(root, protocol.ForgeOpCancel, n, false)))
		}
	}
	if len(items) > 0 {
		items = append(items, ui.MenuDivider())
	}
	return append(items,
		ui.MenuItem(Tf("Open on %s", forgeLabel(forgeName)), func() { _ = runtime.OpenURL(it.GetUrl()) }),
		ui.MenuItem(T("Copy link"), func() { ggui.CurrentClipboard().Write(it.GetUrl()) }),
	)
}

func forgeDetailView(m *model) ggui.Widget {
	head := ggui.View(m.Forge.Open, func(o forgeOpen) ggui.Widget {
		label := fmt.Sprintf("#%d", o.Number)
		switch {
		case o.Kind == protocol.ForgeRuns:
			label = cmp.Or(o.Detail.GetItem().GetWorkflow(), T("Run"))
		case o.Number == 0 && o.Detail.GetItem() != nil:
			label = fmt.Sprintf("#%d", o.Detail.GetItem().GetNumber())
		}
		children := []ggui.Widget{
			iconButton("arrow-left", T("Back"), m.closeForge),
			ggui.Expanded(ggui.Text(label).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		}
		if it := o.Detail.GetItem(); it != nil {
			children = append(children,
				iconButton("external-link", Tf("Open on %s", forgeLabel(o.Detail.GetForge())), func() { _ = runtime.OpenURL(it.GetUrl()) }),
				iconMenu(ui.MenuOf(icon("ellipsis"), forgeItemMenu(m, o.Root, o.Detail.GetForge(), o.Detail.GetOps(), it, o.Detail)...).Width(240).Name(Tf("%s actions", label)), 6),
			)
		}
		return panelHeader(append(children, iconButton("refresh-cw", T("Refresh"), m.readForge))...)
	})
	body := ggui.View(m.Forge.Open, func(o forgeOpen) ggui.Widget {
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
		return forgeDetailBody(m, o)
	})
	return ggui.Column(head, hline(), statusLine(m.Forge.Busy, m.Forge.Note), ggui.Expanded(body)).Stretch()
}

// sectionTitle heads a part of an item's view.
func sectionTitle(title string, count int) ggui.Widget {
	label := []ggui.Widget{ggui.Text(title).Size(11.5).Weight(ggui.WeightSemibold).Color(tokMuted).NoWrap()}
	if count > 0 {
		label = append(label, ggui.Text(lineCount(count)).Size(11).Color(tokExtraMuted))
	}
	return ggui.Box(ggui.Row(label...).Gap(6)).Pad(12, 12, 4, 12)
}

// forgeStateLabel names an item's state as people say it.
func forgeStateLabel(it *protocol.ForgeItem) string {
	switch it.GetKind() {
	case protocol.ForgeRuns:
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

func forgeDetailBody(m *model, o forgeOpen) ggui.Widget {
	d := o.Detail
	it := d.GetItem()
	meta := []string{forgeStateLabel(it)}
	switch it.GetKind() {
	case protocol.ForgePR:
		meta = append(meta, it.GetAuthor(), it.GetHead()+" → "+it.GetBase())
	case protocol.ForgeIssue:
		meta = append(meta, it.GetAuthor())
	default:
		meta = append(meta, it.GetHead(), it.GetEvent())
	}
	top := []ggui.Widget{
		ggui.Row(itemIcon(it), ggui.Expanded(ggui.Text(it.GetTitle()).Size(14).Weight(ggui.WeightSemibold).Selectable())).Gap(8).Align(ggui.AlignStart),
		ggui.Text(metaLine(it, meta...)).Size(11.5).Color(tokExtraMuted).Selectable(),
	}
	if len(it.GetLabels()) > 0 {
		var badges []ggui.Widget
		for _, l := range it.GetLabels() {
			badges = append(badges, ui.Badge(l).Outline())
		}
		top = append(top, ggui.Wrap(badges...).Gap(4))
	}
	if acts := forgeActions(m, o.Root, d); acts != nil {
		top = append(top, acts)
	}
	rows := []ggui.Widget{ggui.Box(ggui.Column(top...).Gap(8).Stretch()).Pad(12, 12, 10, 12)}
	if strings.TrimSpace(d.GetBody()) != "" {
		rows = append(rows, hline(), ggui.Box(markdownText(m, d.GetBody())).Pad(12))
	}
	if len(d.GetChecks()) > 0 {
		title := T("Checks")
		if it.GetKind() == protocol.ForgeRuns {
			title = T("Jobs")
		}
		rows = append(rows, hline(), sectionTitle(title, len(d.GetChecks())))
		for _, c := range d.GetChecks() {
			name := c.GetName()
			if c.GetWorkflow() != "" && it.GetKind() != protocol.ForgeRuns {
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
	if it.GetKind() != protocol.ForgeRuns {
		rows = append(rows, hline(), sectionTitle(T("Conversation"), len(d.GetComments())))
		for _, c := range d.GetComments() {
			rows = append(rows, forgeComment(m, c))
		}
		rows = append(rows, forgeComposer(m, o.Root, it))
	}
	return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
}

// forgeActions are the buttons an item's view leads with: what is most often
// done with it.
func forgeActions(m *model, root fileRoot, d *protocol.ForgeDetail) ggui.Widget {
	it := d.GetItem()
	n := it.GetNumber()
	var btns []ggui.Widget
	switch it.GetKind() {
	case protocol.ForgePR:
		if slices.ContainsFunc(d.GetComments(), func(c *protocol.ForgeComment) bool { return c.GetPath() != "" || c.GetReview() == "CHANGES_REQUESTED" }) {
			btns = append(btns, ui.Button(T("Send review to session"), func() { m.sendToSession(reviewPrompt(d)) }).Outline())
		}
		btns = append(btns, ui.Button(T("Check out"), m.forgeOp(root, protocol.ForgeOpCheckout, n, false)).Ghost())
	case protocol.ForgeIssue:
		btns = append(btns, ui.Button(T("Start a session on it…"), func() { m.startOnIssue(root, it, d) }).Outline())
	case protocol.ForgeRuns:
		switch {
		case !slices.Contains(d.GetOps(), protocol.ForgeOpRerun):
		case it.GetState() != "completed":
			btns = append(btns, ui.Button(T("Cancel run"), m.forgeOp(root, protocol.ForgeOpCancel, n, false)).Ghost())
		case protocol.Failed(it.GetChecks()):
			btns = append(btns,
				ui.Button(T("Send failed logs to session"), func() { m.sendFailedLogs(root, it) }).Outline(),
				ui.Button(T("Run failed jobs again"), m.forgeOp(root, protocol.ForgeOpRerun, n, true)).Ghost())
		default:
			btns = append(btns, ui.Button(T("Run again"), m.forgeOp(root, protocol.ForgeOpRerun, n, false)).Ghost())
		}
	}
	if len(btns) == 0 {
		return nil
	}
	return ggui.Wrap(btns...).Gap(6)
}

// forgeComment is a comment, a review, or a review's comment on a line.
func forgeComment(m *model, c *protocol.ForgeComment) ggui.Widget {
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

// forgeComposer writes a comment on a pull request or an issue, or a review
// of a pull request.
func forgeComposer(m *model, root fileRoot, it *protocol.ForgeItem) ggui.Widget {
	n := it.GetNumber()
	send := func(op, review, label string) func() {
		return func() {
			body := ggui.Peek(m.Forge.Comment)
			m.forgeRun(root, &protocol.ForgeRun{Op: op, Number: n, Kind: it.GetKind(), Review: review, Body: body}, label, func(out string) {
				m.Forge.Comment.Set("")
				m.Forge.Note.Set(gitNote{Text: lastLines(out, 2)})
			})
		}
	}
	field := ui.TextField(m.Forge.Comment).Multiline().Lines(3).Placeholder(T("Leave a comment")).Name(T("Comment")).
		OnSubmit(func(string) { send(protocol.ForgeOpComment, "", T("Commenting…"))() })
	btns := []ggui.Widget{ggui.Spacer()}
	if it.GetKind() == protocol.ForgePR {
		btns = append(btns, borderless(ui.MenuOf(ggui.Row(ggui.Text(T("Review")).Size(13), icon("chevron-down").Size(12)).Gap(4),
			ui.MenuItem(T("Approve"), send(protocol.ForgeOpReview, "approve", T("Reviewing…"))),
			ui.MenuItem(T("Request changes"), send(protocol.ForgeOpReview, "request_changes", T("Reviewing…"))),
			ui.MenuItem(T("Comment as a review"), send(protocol.ForgeOpReview, "comment", T("Reviewing…"))),
		).Name(T("Review"))))
	}
	btns = append(btns, ui.Button(T("Comment"), send(protocol.ForgeOpComment, "", T("Commenting…"))))
	return ggui.Box(ggui.Column(field, ggui.Row(btns...).Gap(8).Align(ggui.AlignCenter)).Gap(8).Stretch()).Pad(8, 12, 16, 12)
}

// forgeBranchLine is the Git tab's line for the branch on its forge: its pull
// request, or a button to open one, and its newest commit's runs.
func forgeBranchLine(m *model) ggui.Widget {
	return ggui.View(m.Forge.Branch, func(b forgeBranch) ggui.Widget {
		if b.Root.Path == "" || b.Err != "" || ggui.Untrack(m.filesRoot) != b.Root {
			return ggui.Box()
		}
		var parts []ggui.Widget
		if pr := b.PR; pr != nil {
			label := ggui.Row(itemIcon(pr), ggui.Box(ggui.Text(fmt.Sprintf("#%d %s", pr.GetNumber(), pr.GetTitle())).Size(12).NoWrap().Ellipsis()).MaxWidth(200), rollupIcon(pr.GetChecks())).Gap(6).Align(ggui.AlignCenter)
			parts = append(parts, ui.ButtonOf(label, func() { m.openForge(b.Root, protocol.ForgePR, pr.GetNumber()) }).Ghost().Pad(3, 6).Radius(6).Name(pr.GetTitle()))
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
				if r.GetState() != "completed" || protocol.Failed(r.GetChecks()) {
					first = r // the run to look at
					break
				}
			}
			parts = append(parts, ui.Tooltip(ui.ButtonOf(ggui.Row(icon, muted(ggui.Text(label).Size(12))).Gap(5),
				func() { m.openForge(b.Root, protocol.ForgeRuns, first.GetNumber()) }).Ghost().Pad(3, 6).Radius(6).Name(T("Actions")),
				Tf("%d runs of %s", len(b.Runs), shortHash(first.GetHeadOid()))))
		}
		return ggui.Column(ggui.Box(ggui.Row(parts...).Gap(4).Align(ggui.AlignCenter)).Pad(2, 6, 2, 6), hline()).Stretch()
	})
}

// forgeDialogs is the new pull request dialog.
func forgeDialogs(m *model) ggui.Widget {
	return ui.Dialog(m.Forge.PROpen, ggui.Column(
		ui.Field(T("Title"), ui.TextField(m.Forge.PRTitle).Placeholder(T("From the commits, when left empty")).Name(T("Title"))),
		ui.Field(T("Description"), ui.TextField(m.Forge.PRBody).Multiline().Lines(5).Name(T("Description"))),
		ui.Field(T("Base branch"), ui.TextField(m.Forge.PRBase).Placeholder(T("The repository's default branch")).Name(T("Base branch"))),
		ui.Checkbox(m.Forge.PRDraft, T("Open as a draft")),
		muted(ggui.Text(T("The branch is pushed first, its upstream set if it has none.")).Size(12)),
		ggui.Row(
			ui.Button(T("Cancel"), func() { m.Forge.PROpen.Set(false) }).Outline(),
			ui.Button(T("Create pull request"), m.submitPR),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(14).Stretch()).Title(T("New pull request")).Width(480)
}
