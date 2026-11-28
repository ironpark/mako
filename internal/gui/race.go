package gui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// raceEntry represents one contestant session inside a Multi-Agent A/B Race.
type raceEntry struct {
	Target    string
	SessionID string
	NodeID    string
	Branch    string
	Worktree  *protocol.Worktree
}

// raceGroup tracks a set of parallel sessions launched with the same prompt in
// isolated git worktrees so the user can compare their diffs and merge the winner.
type raceGroup struct {
	ID        string
	Prompt    string
	Project   string
	NodeID    string
	CreatedAt time.Time
	WinnerID  string
	Entries   []raceEntry
}

type raceState struct {
	Open    *ggui.StateValue[bool]
	Groups  *ggui.StateValue[[]raceGroup]
	Active  *ggui.StateValue[string]
	Changes *ggui.StateValue[map[string]*protocol.GitChanges] // sessionID -> changes since base
	// Busy is the race whose winner is being checked, committed or merged,
	// "" for none: no other winner is picked until it is done.
	Busy *ggui.StateValue[string]
	seq  int
}

func newRaceState() *raceState {
	return &raceState{
		Open:    ggui.State(false),
		Groups:  ggui.State([]raceGroup(nil)),
		Active:  ggui.State(""),
		Changes: ggui.State(map[string]*protocol.GitChanges{}),
		Busy:    ggui.State(""),
	}
}

// raceTargetSet is who races: primary first, then the extras, each once.
// A shell is no contestant, and a shell or no agent at all as the primary
// is no race: nil.
func raceTargetSet(primary string, extras []string) []string {
	primary = strings.TrimSpace(primary)
	if primary == "" || isShellTarget(primary) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || isShellTarget(t) || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	add(primary)
	for _, e := range extras {
		add(e)
	}
	return out
}

// partitionRaceEntries splits entries into the chosen winner and all losing entries.
func partitionRaceEntries(entries []raceEntry, winnerSessionID string) (raceEntry, []raceEntry, bool) {
	var winner raceEntry
	var losers []raceEntry
	found := false
	for _, e := range entries {
		if e.SessionID == winnerSessionID {
			winner = e
			found = true
		} else {
			losers = append(losers, e)
		}
	}
	return winner, losers, found
}

// removeRaceGroup is list without race id.
func removeRaceGroup(list []raceGroup, id string) []raceGroup {
	return slices.DeleteFunc(slices.Clone(list), func(g raceGroup) bool { return g.ID == id })
}

// nonRaceBranch is a run of what a race's branch does not take of an agent
// target: anything but a lowercase letter, a digit or _. What is left is
// valid in a git ref and a folder name on every file system, told apart
// even where case is not.
var nonRaceBranch = regexp.MustCompile(`[^a-z0-9_]+`)

// raceHintLen is the most of an agent target a race's branch keeps.
const raceHintLen = 32

// raceHint is target as a race's branch names it: lowercase, each run of
// what nonRaceBranch matches one -, and "agent" when nothing is left.
func raceHint(target string) string {
	hint := strings.Trim(nonRaceBranch.ReplaceAllString(strings.ToLower(target), "-"), "-")
	if len(hint) > raceHintLen {
		hint = strings.TrimRight(hint[:raceHintLen], "-")
	}
	return cmp.Or(hint, "agent")
}

// raceSuffixAlphabet and raceSuffixLen make the random part of a race's
// branches: 36^4 races started in the same second do not meet.
const (
	raceSuffixAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	raceSuffixLen      = 4
)

// raceSuffix is a random ID a race's branches share.
func raceSuffix() string {
	b := make([]byte, raceSuffixLen)
	for i := range b {
		b[i] = raceSuffixAlphabet[rand.IntN(len(raceSuffixAlphabet))]
	}
	return string(b)
}

// raceBranchName is the branch contestant idx, running target, works on in
// the race started at stamp with the random suffix: race/<agent>-<stamp>-<suffix>-<n>.
func raceBranchName(target, stamp, suffix string, idx int) string {
	return fmt.Sprintf("race/%s-%s-%s-%d", raceHint(target), stamp, suffix, idx+1)
}

// raceCommitSubjectLen is the most of a race's prompt a commit's subject
// keeps.
const raceCommitSubjectLen = 60

// raceCommitMessage is what the winner's uncommitted work is committed as
// before it is merged: its agent, and the prompt's first line.
func raceCommitMessage(target, prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > raceCommitSubjectLen {
		line = strings.TrimSpace(string([]rune(line)[:raceCommitSubjectLen])) + "..."
	}
	if line == "" {
		return "Race winner " + target
	}
	return "Race winner " + target + ": " + line
}

// startRace launches every contestant in its own isolated git worktree with the
// same prompt and registers a raceGroup for side-by-side comparison.
func (m *model) startRace(tab string, f newSession) {
	targets := raceTargetSet(f.Agent, f.RaceTargets)
	if len(targets) < 2 {
		m.startSession(tab, f)
		return
	}
	if tab != "" {
		l := m.launchOf(tab)
		l.Err.Set("")
		l.Starting.Set(true)
	}

	m.Races.seq++
	raceID := "race-" + strconv.Itoa(m.Races.seq)
	prompt := strings.TrimSpace(f.Prompt)
	nodeID := m.remoteID(f.NodeID)
	now := time.Now()
	group := raceGroup{
		ID:        raceID,
		Prompt:    prompt,
		Project:   f.Project,
		NodeID:    f.NodeID,
		CreatedAt: now,
	}
	m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
		return append([]raceGroup{group}, list...)
	})
	m.Races.Active.Set(raceID)

	stamp, suffix := now.Format("20060102-150405"), raceSuffix()
	remaining := len(targets)
	started := 0
	for idx, tgt := range targets {
		agentName, profile := agentspec.SplitTarget(tgt)
		req := &protocol.OpenSession{
			NodeId:   nodeID,
			Agent:    agentName,
			Profile:  profile,
			Project:  f.Project,
			Name:     Tf("Race · %s", targetLabel(tgt)),
			Prompt:   prompt,
			Worktree: &protocol.Worktree{Branch: raceBranchName(tgt, stamp, suffix, idx), Base: f.Base, Start: f.Start},
			Cols:     120,
			Rows:     36,
		}
		fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
			return c.Open(ctx, req)
		}, func(info *protocol.SessionInfo, err error) {
			remaining--
			if err != nil {
				m.toaster.Push(ui.Toast(Tf("%s did not start", targetLabel(tgt)), nodeErr(err)).Destructive())
			} else {
				started++
				if _, exists := m.session(info.GetId()); !exists {
					m.Sessions.Set(append(slices.Clone(ggui.Peek(m.Sessions)), info))
				}
				entry := raceEntry{
					Target:    tgt,
					SessionID: info.GetId(),
					NodeID:    info.GetNodeId(),
					Branch:    cmp.Or(info.GetWorktree().GetBranch(), info.GetBranch()),
					Worktree:  info.GetWorktree(),
				}
				m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
					next := slices.Clone(list)
					for i := range next {
						if next[i].ID == raceID {
							next[i].Entries = append(slices.Clone(next[i].Entries), entry)
							break
						}
					}
					return next
				})
				if started == 1 {
					// The first to start takes the tab's place, in whichever
					// window the tab is.
					m.showStarted(tab, info)
				}
			}
			if remaining > 0 {
				return
			}
			if started > 0 {
				m.toaster.Push(ui.Toast(Tf("Started %d-agent comparison race", started), T("Open Compare Races to inspect diffs side by side")))
				return
			}
			// Nothing started: the race goes, and the tab, opened again if
			// it was closed meanwhile, says why.
			m.Races.Groups.Update(func(list []raceGroup) []raceGroup { return removeRaceGroup(list, raceID) })
			if ggui.Peek(m.Races.Active) == raceID {
				next := ""
				if left := ggui.Peek(m.Races.Groups); len(left) > 0 {
					next = left[0].ID
				}
				m.Races.Active.Set(next)
			}
			if tab == "" || m.launches[tab] == nil {
				tab, _ = m.newLaunch(f)
			}
			l := m.launchOf(tab)
			l.Starting.Set(false)
			l.Err.Set(T("Could not start any race session"))
		})
	}
}

func (m *model) openRaceDialog() {
	groups := ggui.Peek(m.Races.Groups)
	if len(groups) > 0 && ggui.Peek(m.Races.Active) == "" {
		m.Races.Active.Set(groups[0].ID)
	}
	m.Races.Open.Set(true)
	m.refreshActiveRaceDiffs()
}

func (m *model) refreshActiveRaceDiffs() {
	activeID := ggui.Peek(m.Races.Active)
	var group *raceGroup
	for _, g := range ggui.Peek(m.Races.Groups) {
		if g.ID == activeID {
			cp := g
			group = &cp
			break
		}
	}
	if group == nil {
		return
	}
	for _, entry := range group.Entries {
		e := entry
		s, ok := m.session(e.SessionID)
		if !ok {
			continue
		}
		wt := cmp.Or(s.GetWorktree(), e.Worktree)
		req := &protocol.GitStatus{
			NodeId: m.remoteID(s.GetNodeId()),
			Path:   s.GetProject(),
			Base:   wt.GetBase(),
		}
		fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitChanges, error) {
			return c.GitStatus(ctx, req)
		}, func(ch *protocol.GitChanges, err error) {
			if err != nil || ch == nil {
				return
			}
			m.Races.Changes.Update(func(cur map[string]*protocol.GitChanges) map[string]*protocol.GitChanges {
				next := maps.Clone(cur)
				next[e.SessionID] = ch
				return next
			})
		})
	}
}

// raceWorktree is the worktree e runs in: as its session now says, or as
// it said when it started.
func (m *model) raceWorktree(e raceEntry) *protocol.Worktree {
	if s, ok := m.session(e.SessionID); ok && s.GetWorktree() != nil {
		return s.GetWorktree()
	}
	return e.Worktree
}

// uncommittedFiles counts what the work tree at path, on node, has not
// committed, untracked files included, and hands done the count.
func (m *model) uncommittedFiles(node, path string, done func(int, error)) {
	req := &protocol.GitStatus{NodeId: m.remoteID(node), Path: path}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitChanges, error) {
		return c.GitStatus(ctx, req)
	}, func(ch *protocol.GitChanges, err error) {
		done(len(ch.GetFiles()), err)
	})
}

// raceWinnerBody says what merging winner does: what it commits first,
// where it merges, and what of the others it deletes.
func raceWinnerBody(winner raceEntry, wt *protocol.Worktree, prompt string, dirty int) string {
	branch := cmp.Or(wt.GetBranch(), winner.Branch)
	var parts []string
	if dirty > 0 {
		parts = append(parts, Tf("Its worktree has %d uncommitted file(s). Mako commits them on %s first, as \"%s\".", dirty, branch, raceCommitMessage(winner.Target, prompt)))
	}
	parts = append(parts,
		Tf("Then %s is merged into %s.", branch, cmp.Or(wt.GetBase(), "HEAD")),
		T("The other race sessions are stopped, and their worktrees and branches are deleted with any work left in them."))
	return strings.Join(parts, " ")
}

// askPickRaceWinner asks before merging the winner. It first reads what
// the winner's worktree has not committed, so the question says what is
// committed for it; an agent still at work is not picked at all, as its
// files are still changing.
func (m *model) askPickRaceWinner(group raceGroup, winnerSessionID string) {
	st := m.Races
	if ggui.Peek(st.Busy) != "" {
		return
	}
	winner, losers, ok := partitionRaceEntries(group.Entries, winnerSessionID)
	if !ok {
		return
	}
	wt := m.raceWorktree(winner)
	if wt == nil {
		m.toaster.Push(ui.Toast(T("Could not merge winner"), T("Winning session has no worktree")).Destructive())
		return
	}
	if s, ok := m.session(winner.SessionID); ok && ggui.Untrack(func() dotState { return m.dotOf(s) }) == dotWorking {
		m.toaster.Push(ui.Toast(Tf("%s is still working", targetLabel(winner.Target)),
			T("Wait for it to finish, or stop it, before you pick it as the winner.")).Destructive())
		return
	}
	st.Busy.Set(group.ID)
	m.uncommittedFiles(winner.NodeID, wt.GetPath(), func(dirty int, err error) {
		st.Busy.Set("")
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not read the winner's changes"), nodeErr(err)).Destructive())
			return
		}
		m.askConfirm(confirmation{
			Title:  Tf("Merge %s and clean up %d other worktree(s)?", targetLabel(winner.Target), len(losers)),
			Body:   raceWinnerBody(winner, wt, group.Prompt, dirty),
			Action: T("Merge winner"),
			Run: func() {
				m.applyRaceWinner(group, winner, losers, dirty > 0)
			},
		})
	})
}

// raceLoser is a losing contestant with the worktree it ran in, read
// before its session stops and leaves the list.
type raceLoser struct {
	raceEntry
	wt *protocol.Worktree
}

// applyRaceWinner merges the winner's branch, committing first what its
// worktree has not when commit says the user agreed to it, then stops the
// losers and deletes their worktrees. Work that appeared in the winner's
// worktree after the user was asked is not committed unasked: the merge
// stops, and says so.
func (m *model) applyRaceWinner(group raceGroup, winner raceEntry, losers []raceEntry, commit bool) {
	st := m.Races
	if ggui.Peek(st.Busy) != "" {
		return
	}
	wt := m.raceWorktree(winner)
	if wt == nil {
		m.toaster.Push(ui.Toast(T("Could not merge winner"), T("Winning session has no worktree")).Destructive())
		return
	}
	cleanup := make([]raceLoser, len(losers))
	for i, l := range losers {
		cleanup[i] = raceLoser{l, m.raceWorktree(l)}
	}
	node := m.remoteID(winner.NodeID)
	st.Busy.Set(group.ID)
	fail := func(title string, err error) {
		st.Busy.Set("")
		m.toaster.Push(ui.Toast(title, nodeErr(err)).Destructive())
	}
	merge := func() {
		req := &protocol.GitRun{NodeId: node, Root: wt.GetRepo(), Op: protocol.GitOpMerge, Worktree: wt}
		fetch(m, 2*time.Minute, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
			return c.GitRun(ctx, req)
		}, func(_ *protocol.GitResult, err error) {
			if err != nil {
				fail(T("Could not merge winner"), err)
				return
			}
			st.Groups.Update(func(list []raceGroup) []raceGroup {
				next := slices.Clone(list)
				for i := range next {
					if next[i].ID == group.ID {
						next[i].WinnerID = winner.SessionID
						break
					}
				}
				return next
			})
			st.Busy.Set("")
			m.toaster.Push(ui.Toast(Tf("Merged winner %s", targetLabel(winner.Target)), cmp.Or(wt.GetBranch(), winner.Branch)))
			m.cleanUpRaceLosers(cleanup)
		})
	}
	m.uncommittedFiles(winner.NodeID, wt.GetPath(), func(dirty int, err error) {
		switch {
		case err != nil:
			fail(T("Could not read the winner's changes"), err)
		case dirty == 0:
			merge()
		case !commit:
			st.Busy.Set("")
			m.toaster.Push(ui.Toast(T("The winner's worktree changed"),
				Tf("It has %d uncommitted file(s) now. Pick the winner again to commit and merge them.", dirty)).Destructive())
		default:
			req := &protocol.GitRun{NodeId: node, Root: wt.GetPath(), Op: protocol.GitOpCommit, Message: raceCommitMessage(winner.Target, group.Prompt)}
			fetch(m, 2*time.Minute, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
				return c.GitRun(ctx, req)
			}, func(_ *protocol.GitResult, err error) {
				if err != nil {
					fail(T("Could not commit the winner's changes"), err)
					return
				}
				merge()
			})
		}
	})
}

// cleanUpRaceLosers stops the losing sessions and, once they have stopped,
// deletes their worktrees and branches with whatever is left in them, which
// the user agreed to when they picked the winner. A session that did not
// stop keeps its worktree, and every worktree not deleted is said.
func (m *model) cleanUpRaceLosers(losers []raceLoser) {
	if len(losers) == 0 {
		return
	}
	ids := make([]string, len(losers))
	for i, l := range losers {
		ids[i] = l.SessionID
	}
	m.killAll(ids, func() {
		var failed []string
		removed, left := 0, 0
		report := func() {
			if len(failed) > 0 {
				m.toaster.Push(ui.Toast(Tf("Could not clean up %d race worktree(s)", len(failed)), strings.Join(failed, "\n")).Destructive())
			}
			if removed > 0 {
				m.toaster.Push(ui.Toast(Tf("Removed %d other race worktree(s)", removed), T("Their sessions stopped, and their branches were deleted.")))
			}
		}
		for _, l := range losers {
			label := targetLabel(l.Target)
			if _, alive := m.session(l.SessionID); alive {
				failed = append(failed, Tf("%s: its session did not stop, so its worktree was kept", label))
				continue
			}
			if l.wt == nil {
				continue
			}
			left++
			req := &protocol.GitRun{NodeId: m.remoteID(l.NodeID), Root: l.wt.GetRepo(), Op: protocol.GitOpRemoveWT, Worktree: l.wt, Force: true}
			fetch(m, 2*time.Minute, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
				return c.GitRun(ctx, req)
			}, func(_ *protocol.GitResult, err error) {
				if err != nil {
					failed = append(failed, label+": "+nodeErr(err))
				} else {
					removed++
				}
				if left--; left == 0 {
					report()
				}
			})
		}
		if left == 0 {
			report()
		}
	})
}

// launchCompareMenu lets the user pick additional agents/profiles in the Launch
// tab to run the same prompt side by side in isolated worktrees. It reads
// only what the tab picks besides its prompt, so typing does not rebuild it.
func launchCompareMenu(l *launchState, where ggui.Readable[launchWhere], targets, picked ggui.Readable[[]string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		w, all, chosen := where.Get(), targets.Get(), picked.Get()
		if isShellTarget(w.Agent) {
			return widget.Box()
		}
		candidates := slices.DeleteFunc(slices.Clone(all), func(t string) bool { return t == w.Agent })
		if len(candidates) == 0 {
			return widget.Box()
		}
		items := make([]ggui.Widget, 0, len(candidates)+2)
		if len(chosen) > 0 {
			items = append(items,
				ui.MenuItem(T("Clear comparison"), func() {
					l.Form.Update(func(cur newSession) newSession { cur.RaceTargets = nil; return cur })
				}),
				ui.MenuDivider(),
			)
		}
		for _, c := range candidates {
			items = append(items, ui.MenuItem(checked(targetLabel(c), slices.Contains(chosen, c)), func() {
				l.Form.Update(func(cur newSession) newSession {
					cur.RaceTargets = toggled(cur.RaceTargets, c)
					if len(cur.RaceTargets) > 0 {
						// A race runs each agent in a worktree of its own,
						// leading no team.
						cur.Worktree, cur.Team = true, ""
					}
					return cur
				})
			}))
		}
		label := T("Compare")
		n := len(raceTargetSet(w.Agent, chosen))
		if n >= 2 {
			label = Tf("Compare (%d)", n)
		}
		return dropMenu("columns-2", label, n >= 2, 240, T("Compare agents side by side"), items...)
	})
}

// raceFooterButton appears in the sidebar footer when at least one race exists.
func raceFooterButton(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		groups := m.Races.Groups.Get()
		if len(groups) == 0 {
			return widget.Box()
		}
		tip := Tf("Compare agent races (%d)", len(groups))
		btn := ui.ButtonOf(icon("columns-2").Color(skin.AccentBright), m.openRaceDialog).Ghost().Pad(6).Radius(6).Name(T("Compare agent races"))
		return ui.Tooltip(btn, tip)
	})
}

func raceDialog(m *model) ggui.Widget {
	st := m.Races
	body := ggui.Reactive(func() ggui.Widget {
		if !st.Open.Get() {
			return widget.Box()
		}
		groups := st.Groups.Get()
		if len(groups) == 0 {
			return muted(widget.Text(T("No comparison races started yet. Pick Compare in a New session tab to run two or more agents side by side.")).Size(13))
		}
		activeID := st.Active.Get()
		var cur *raceGroup
		for i := range groups {
			if groups[i].ID == activeID {
				cur = &groups[i]
				break
			}
		}
		if cur == nil {
			cur = &groups[0]
		}
		changes := st.Changes.Get()
		sessions := m.Sessions.Get()
		busy := st.Busy.Get() != ""

		topRow := []ggui.Widget{
			widget.Expanded(widget.Text(firstLine(cur.Prompt)).Size(13.5).Weight(ggui.WeightSemibold).NoWrap().Ellipsis()),
			ui.Button(T("Refresh"), m.refreshActiveRaceDiffs).Ghost().Pad(4, 10).Radius(6),
		}
		if len(groups) > 1 {
			ids := make([]string, len(groups))
			labels := map[string]string{}
			for i, g := range groups {
				ids[i] = g.ID
				labels[g.ID] = fmt.Sprintf("%s (%d)", firstLine(g.Prompt), len(g.Entries))
			}
			topRow = append([]ggui.Widget{
				widget.Box(ui.Select(st.Active).Options(ids).Format(func(id string) string { return labels[id] }).Name(T("Race"))).Width(200),
			}, topRow...)
		}

		cards := make([]ggui.Widget, 0, len(cur.Entries))
		for _, entry := range cur.Entries {
			e := entry
			s, hasSess := sessionIn(sessions, e.SessionID)
			ch := changes[e.SessionID]
			isWinner := cur.WinnerID != "" && cur.WinnerID == e.SessionID

			var added, removed int64
			for _, f := range ch.GetFiles() {
				added += f.GetAdded()
				removed += f.GetRemoved()
			}

			statusText := T("Starting")
			statusCol := skin.Muted
			if hasSess {
				statusText = statusLabel(s)
				switch m.dotOf(s) {
				case dotWorking:
					statusCol = skin.DotRunning
				case dotWaiting:
					statusCol = skin.DotWaiting
				case dotDone:
					statusCol = skin.AccentBright
				case dotError:
					statusCol = skin.DotDown
				}
			}

			agentBase, _ := agentspec.SplitTarget(e.Target)
			cardRows := []ggui.Widget{
				widget.Row(
					agentGlyph(agentBase, skin.Muted).Size(15),
					widget.Expanded(widget.Text(targetLabel(e.Target)).Size(13.5).Weight(ggui.WeightSemibold).NoWrap().Ellipsis()),
					dot(statusCol),
					muted(widget.Text(statusText).Size(11.5).NoWrap()),
				).Gap(6).Align(widget.AlignCenter),
				muted(widget.Text(cmp.Or(e.Branch, e.SessionID)).Size(11.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
			}
			if ch != nil {
				cardRows = append(cardRows, widget.Text(Tf("%d files changed · +%d -%d", len(ch.GetFiles()), added, removed)).Size(12).Weight(ggui.WeightMedium))
				for _, f := range ch.GetFiles()[:min(len(ch.GetFiles()), 6)] {
					file := f
					cardRows = append(cardRows, ui.ButtonOf(widget.Row(
						widget.Expanded(widget.Text(file.GetPath()).Size(11.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
						widget.Text(fmt.Sprintf("+%d -%d", file.GetAdded(), file.GetRemoved())).Size(11).Color(skin.Muted).NoWrap(),
					).Gap(6).Align(widget.AlignCenter), func() {
						if hasSess {
							st.Open.Set(false)
							wt := cmp.Or(s.GetWorktree(), e.Worktree)
							m.openDiffTab(fileRoot{Node: s.GetNodeId(), Path: s.GetProject()}, s.GetProject(), "", wt.GetBase(), file, false)
						}
					}).Ghost().Pad(2, 4).Radius(4))
				}
				if len(ch.GetFiles()) > 6 {
					cardRows = append(cardRows, muted(widget.Text(Tf("and %d more", len(ch.GetFiles())-6)).Size(11.5)))
				}
			} else {
				cardRows = append(cardRows, muted(widget.Text(T("No diff summary loaded yet.")).Size(12)))
			}

			var footer []ggui.Widget
			footer = append(footer, ui.Button(T("Open session"), func() {
				st.Open.Set(false)
				m.selectSession(e.SessionID)
			}).Outline().Pad(4, 10).Radius(6))
			if isWinner {
				footer = append(footer, widget.Box(widget.Text(T("Winner")).Size(11.5).Weight(ggui.WeightSemibold).Color(skin.DotRunning).NoWrap()).
					Fill(skin.DotRunning.Alpha(0.15)).Radius(6).Pad(4, 8))
			} else if cur.WinnerID == "" {
				grp := *cur
				footer = append(footer, ui.Button(T("Pick winner & merge"), func() {
					m.askPickRaceWinner(grp, e.SessionID)
				}).Pad(4, 10).Radius(6).Disabled(busy))
			}
			cardRows = append(cardRows, widget.Row(footer...).Gap(8).Align(widget.AlignCenter))

			borderCol := skin.Border
			if isWinner {
				borderCol = skin.DotRunning
			}
			cards = append(cards, widget.Expanded(widget.Box(widget.Column(cardRows...).Gap(8).Stretch()).
				Fill(skin.Surface1).Border(1, borderCol).Radius(10).Pad(12, 14)))
		}

		return widget.Column(
			widget.Row(topRow...).Gap(8).Align(widget.AlignCenter),
			widget.Row(cards...).Gap(12).Stretch(),
		).Gap(12).Stretch()
	})
	return keyedDialog(st.Open, body).Title(T("Compare agent races")).Width(760)
}
