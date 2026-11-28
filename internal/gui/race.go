package gui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

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
	seq     int
}

func newRaceState() *raceState {
	return &raceState{
		Open:    ggui.State(false),
		Groups:  ggui.State([]raceGroup(nil)),
		Active:  ggui.State(""),
		Changes: ggui.State(map[string]*protocol.GitChanges{}),
	}
}

// raceTargetSet deduplicates primary + extra targets while preserving order.
func raceTargetSet(primary string, extras []string) []string {
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

// startRace launches every contestant in its own isolated git worktree with the
// same prompt and registers a raceGroup for side-by-side comparison.
func (m *model) startRace(tab string, f newSession) {
	targets := raceTargetSet(f.Agent, f.RaceTargets)
	if len(targets) < 2 {
		m.startSession(tab, f)
		return
	}
	l := m.launchOf(tab)
	l.Err.Set("")
	l.Starting.Set(true)

	m.Races.seq++
	raceID := "race-" + strconv.Itoa(m.Races.seq)
	prompt := strings.TrimSpace(f.Prompt)
	nodeID := m.remoteID(f.NodeID)
	group := raceGroup{
		ID:        raceID,
		Prompt:    prompt,
		Project:   f.Project,
		NodeID:    f.NodeID,
		CreatedAt: time.Now(),
	}
	m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
		return append([]raceGroup{group}, list...)
	})
	m.Races.Active.Set(raceID)

	stamp := time.Now().Format("150405")
	remaining := len(targets)
	started := 0
	for idx, target := range targets {
		tgt := target
		agentName, profile := agentspec.SplitTarget(tgt)
		branchHint := strings.ReplaceAll(strings.ReplaceAll(tgt, ":", "-"), " ", "-")
		branch := fmt.Sprintf("race/%s-%s-%d", branchHint, stamp, idx+1)
		req := &protocol.OpenSession{
			NodeId:   nodeID,
			Agent:    agentName,
			Profile:  profile,
			Project:  f.Project,
			Name:     Tf("Race · %s", targetLabel(tgt)),
			Prompt:   prompt,
			Worktree: &protocol.Worktree{Branch: branch, Base: f.Base, Start: f.Start},
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
				if started == 1 && tab != "" {
					m.closeLaunch(tab)
					m.selectSession(info.GetId())
				}
			}
			if remaining == 0 {
				if started == 0 && l != nil {
					l.Starting.Set(false)
					l.Err.Set(T("Could not start any race session"))
				} else if started > 0 {
					m.toaster.Push(ui.Toast(Tf("Started %d-agent comparison race", started), T("Open Compare Races to inspect diffs side by side")))
				}
			}
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

func (m *model) askPickRaceWinner(group raceGroup, winnerSessionID string) {
	winner, losers, ok := partitionRaceEntries(group.Entries, winnerSessionID)
	if !ok {
		return
	}
	m.askConfirm(confirmation{
		Title:  Tf("Merge %s and clean up %d other worktree(s)?", targetLabel(winner.Target), len(losers)),
		Body:   T("Merges the winning branch into its base branch, then stops the other race sessions and attempts to remove their worktrees."),
		Action: T("Merge winner"),
		Safe:   true,
		Run: func() {
			m.applyRaceWinner(group.ID, winner, losers)
		},
	})
}

func (m *model) applyRaceWinner(raceID string, winner raceEntry, losers []raceEntry) {
	wt := winner.Worktree
	if s, ok := m.session(winner.SessionID); ok && s.GetWorktree() != nil {
		wt = s.GetWorktree()
	}
	if wt == nil {
		m.toaster.Push(ui.Toast(T("Could not merge winner"), T("Winning session has no worktree")).Destructive())
		return
	}
	req := &protocol.GitRun{
		NodeId:   m.remoteID(winner.NodeID),
		Root:     wt.GetRepo(),
		Op:       protocol.GitOpMerge,
		Worktree: wt,
	}
	fetch(m, 25*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(_ *protocol.GitResult, err error) {
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not merge winner"), nodeErr(err)).Destructive())
			return
		}
		m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
			next := slices.Clone(list)
			for i := range next {
				if next[i].ID == raceID {
					next[i].WinnerID = winner.SessionID
					break
				}
			}
			return next
		})
		var loserIDs []string
		for _, loser := range losers {
			loserIDs = append(loserIDs, loser.SessionID)
			lwt := loser.Worktree
			if s, ok := m.session(loser.SessionID); ok && s.GetWorktree() != nil {
				lwt = s.GetWorktree()
			}
			if lwt != nil {
				rmReq := &protocol.GitRun{
					NodeId:   m.remoteID(loser.NodeID),
					Root:     lwt.GetRepo(),
					Op:       protocol.GitOpRemoveWT,
					Worktree: lwt,
				}
				fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
					return c.GitRun(ctx, rmReq)
				}, func(_ *protocol.GitResult, _ error) {})
			}
		}
		if len(loserIDs) > 0 {
			m.killAll(loserIDs, nil)
		}
		m.toaster.Push(ui.Toast(Tf("Merged winner %s", targetLabel(winner.Target)), winner.Branch))
	})
}

// launchCompareMenu lets the user pick additional agents/profiles in the Launch
// tab to run the same prompt side by side in isolated worktrees.
func launchCompareMenu(m *model, l *launchState, where ggui.Readable[launchWhere]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		f := l.Form.Get()
		if isShellTarget(f.Agent) {
			return widget.Box()
		}
		all := sessionAgentTargets(nodeTargets(m.Nodes.Get(), where.Get().NodeID, m.Agents.Get()))
		var candidates []string
		for _, t := range all {
			if t != f.Agent {
				candidates = append(candidates, t)
			}
		}
		if len(candidates) == 0 {
			return widget.Box()
		}
		items := make([]ggui.Widget, 0, len(candidates)+2)
		if len(f.RaceTargets) > 0 {
			items = append(items,
				ui.MenuItem(T("Clear comparison"), func() {
					l.Form.Update(func(cur newSession) newSession { cur.RaceTargets = nil; return cur })
				}),
				ui.MenuDivider(),
			)
		}
		for _, cand := range candidates {
			c := cand
			on := slices.Contains(f.RaceTargets, c)
			items = append(items, ui.MenuItem(checked(targetLabel(c), on), func() {
				l.Form.Update(func(cur newSession) newSession {
					cur.RaceTargets = toggled(cur.RaceTargets, c)
					if len(cur.RaceTargets) > 0 {
						cur.Worktree = true
					}
					return cur
				})
			}))
		}
		label := T("Compare")
		if len(f.RaceTargets) > 0 {
			label = Tf("Compare (%d)", len(f.RaceTargets)+1)
		}
		return dropMenu("columns-2", label, len(f.RaceTargets) > 0, 240, T("Compare agents side by side"), items...)
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
				}).Pad(4, 10).Radius(6))
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
