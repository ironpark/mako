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
	// Removed is set once the race's winner was merged and this loser's
	// session stopped, and its worktree and branch were deleted.
	Removed bool
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
	// Starting counts the contestants whose start has not answered yet:
	// no winner is picked while one may still join.
	Starting int
}

type raceState struct {
	Open *ggui.StateValue[bool]
	// Owner is the window the dialog shows in: "" for the main window,
	// or a Space window's spaceID.
	Owner   *ggui.StateValue[string]
	Groups  *ggui.StateValue[[]raceGroup]
	Active  *ggui.StateValue[string]
	Changes *ggui.StateValue[map[string]*protocol.GitChanges] // sessionID -> changes since base
	// Errs is, by session, why its changes could not be read last.
	Errs *ggui.StateValue[map[string]string]
	// Busy is the race whose winner is being checked, committed or merged,
	// or whose losers are being cleaned up, "" for none: no other winner
	// is picked until it is done.
	Busy *ggui.StateValue[string]
	seq  int
}

func newRaceState() *raceState {
	return &raceState{
		Open:    ggui.State(false),
		Owner:   ggui.State(""),
		Groups:  ggui.State([]raceGroup(nil)),
		Active:  ggui.State(""),
		Changes: ggui.State(map[string]*protocol.GitChanges{}),
		Errs:    ggui.State(map[string]string{}),
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

// raceStartTimeout is how long a contestant's start is waited for: the
// daemon gives its worktree a minute to be made, and the agent then has
// to start in it.
const raceStartTimeout = 90 * time.Second

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
		Starting:  len(targets),
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
		fetch(m, raceStartTimeout, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
			return c.Open(ctx, req)
		}, func(info *protocol.SessionInfo, err error) {
			remaining--
			var entry *raceEntry
			if err != nil {
				m.toaster.Push(ui.Toast(Tf("%s did not start", targetLabel(tgt)), nodeErr(err)).Destructive())
			} else {
				started++
				if _, exists := m.session(info.GetId()); !exists {
					m.Sessions.Set(append(slices.Clone(ggui.Peek(m.Sessions)), info))
				}
				entry = &raceEntry{
					Target:    tgt,
					SessionID: info.GetId(),
					NodeID:    info.GetNodeId(),
					Branch:    cmp.Or(info.GetWorktree().GetBranch(), info.GetBranch()),
					Worktree:  info.GetWorktree(),
				}
			}
			m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
				next := slices.Clone(list)
				for i := range next {
					if next[i].ID == raceID {
						next[i].Starting--
						if entry != nil {
							next[i].Entries = append(slices.Clone(next[i].Entries), *entry)
						}
						break
					}
				}
				return next
			})
			if entry != nil && started == 1 {
				// The first to start takes the tab's place, in whichever
				// window the tab is.
				m.showStarted(tab, info)
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
			m.forgetRace(raceID)
			if tab == "" || m.launches[tab] == nil {
				tab, _ = m.newLaunch(f)
			}
			l := m.launchOf(tab)
			l.Starting.Set(false)
			l.Err.Set(T("Could not start any race session"))
		})
	}
}

// openRaceDialog opens the race dialog in the main window.
func (m *model) openRaceDialog() { m.openRaceDialogIn("") }

// openRaceDialogIn opens the race dialog in the window owner names: ""
// for the main window, or a Space window's spaceID.
func (m *model) openRaceDialogIn(owner string) {
	groups := ggui.Peek(m.Races.Groups)
	if len(groups) > 0 && ggui.Peek(m.Races.Active) == "" {
		m.Races.Active.Set(groups[0].ID)
	}
	m.Races.Owner.Set(owner)
	// raceDialog reads the diffs as it opens.
	m.Races.Open.Set(true)
}

// activeRace is the race the dialog shows: the one picked, or the newest.
func activeRace(groups []raceGroup, activeID string) (raceGroup, bool) {
	for _, g := range groups {
		if g.ID == activeID {
			return g, true
		}
	}
	if len(groups) > 0 {
		return groups[0], true
	}
	return raceGroup{}, false
}

// raceGroupByID is race id, if it is still listed.
func raceGroupByID(groups []raceGroup, id string) (raceGroup, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return raceGroup{}, false
}

// raceEntryIDs is the sessions of a race's contestants, in order.
func raceEntryIDs(entries []raceEntry) []string {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.SessionID
	}
	return ids
}

// forgetRace takes race id off the list, with what was read of its
// contestants' changes; the dialog then shows the newest race left.
func (m *model) forgetRace(id string) {
	st := m.Races
	group, ok := raceGroupByID(ggui.Peek(st.Groups), id)
	if !ok {
		return
	}
	ids := raceEntryIDs(group.Entries)
	st.Groups.Update(func(list []raceGroup) []raceGroup { return removeRaceGroup(list, id) })
	st.Changes.Update(func(cur map[string]*protocol.GitChanges) map[string]*protocol.GitChanges {
		next := maps.Clone(cur)
		maps.DeleteFunc(next, func(k string, _ *protocol.GitChanges) bool { return slices.Contains(ids, k) })
		return next
	})
	st.Errs.Update(func(cur map[string]string) map[string]string {
		next := maps.Clone(cur)
		maps.DeleteFunc(next, func(k, _ string) bool { return slices.Contains(ids, k) })
		return next
	})
	if ggui.Peek(st.Active) == id {
		next := ""
		if left := ggui.Peek(st.Groups); len(left) > 0 {
			next = left[0].ID
		}
		st.Active.Set(next)
	}
}

// dismissRace forgets race id once nothing is under way in it: its
// sessions, worktrees and branches stay as they are, and only the
// comparison goes.
func (m *model) dismissRace(id string) {
	group, ok := raceGroupByID(ggui.Peek(m.Races.Groups), id)
	if !ok || ggui.Peek(m.Races.Busy) == id || group.Starting > 0 {
		return
	}
	m.forgetRace(id)
}

func (m *model) refreshActiveRaceDiffs() {
	group, ok := activeRace(ggui.Peek(m.Races.Groups), ggui.Peek(m.Races.Active))
	if !ok {
		return
	}
	for _, e := range group.Entries {
		s, ok := m.session(e.SessionID)
		if !ok || e.Removed {
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
			m.Races.Errs.Update(func(cur map[string]string) map[string]string {
				next := maps.Clone(cur)
				if err != nil {
					next[e.SessionID] = nodeErr(err)
				} else {
					delete(next, e.SessionID)
				}
				return next
			})
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

// readUncommitted reads what the work tree at path, on node, has not
// committed, untracked files included, and hands it to done.
func (m *model) readUncommitted(node, path string, done func(*protocol.GitChanges, error)) {
	req := &protocol.GitStatus{NodeId: m.remoteID(node), Path: path}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitChanges, error) {
		return c.GitStatus(ctx, req)
	}, done)
}

// raceFiles is what the merge commits first of a winner's worktree, as ch
// reads it: a key that changes when any of its uncommitted files does, and
// how many there are. blocked says why nothing may be committed or merged
// at all: a merge, rebase or the like under way, or files in conflict.
func raceFiles(ch *protocol.GitChanges) (key string, n int, blocked string) {
	if op := ch.GetOperation(); op != "" {
		return "", 0, Tf("A %s is under way in its worktree. Finish or abort it in its session first.", op)
	}
	lines := make([]string, 0, len(ch.GetFiles()))
	for _, f := range ch.GetFiles() {
		if f.GetStatus() == protocol.GitConflicted {
			return "", 0, T("Its worktree has files in conflict. Resolve them in its session first.")
		}
		lines = append(lines, fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%t",
			f.GetStatus(), f.GetFrom(), f.GetPath(), f.GetAdded(), f.GetRemoved(), f.GetBinary()))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n"), len(lines), ""
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

// raceAsk is what the user agreed to when they picked a race's winner,
// checked again before anything is done: the race, its winner, every
// contestant it had, and the winner's uncommitted files.
type raceAsk struct {
	Group   string
	Winner  string
	Entries []string // the contestants' sessions, as raceEntryIDs has them
	Files   string   // raceFiles' key of the winner's uncommitted files
	Count   int      // how many they were
}

// raceChanged says why the race in groups can no longer be finished as
// ask has it, or "" when nothing stands in the way.
func raceChanged(groups []raceGroup, ask raceAsk) string {
	g, ok := raceGroupByID(groups, ask.Group)
	switch {
	case !ok:
		return T("The race was dismissed.")
	case g.WinnerID != "":
		return T("Its winner was already merged.")
	case g.Starting > 0:
		return T("Some of its agents are still starting.")
	case !slices.Equal(raceEntryIDs(g.Entries), ask.Entries):
		return T("An agent joined the race after you were asked.")
	case !slices.Contains(ask.Entries, ask.Winner):
		return T("The winner is no longer in the race.")
	}
	return ""
}

// raceWinnerReady is the worktree winner runs in, once nothing stops it
// from being merged now; else what stops it, as a title and why.
func (m *model) raceWinnerReady(winner raceEntry) (wt *protocol.Worktree, title, why string) {
	if wt = m.raceWorktree(winner); wt == nil {
		return nil, T("Could not merge winner"), T("Winning session has no worktree")
	}
	if s, ok := m.session(winner.SessionID); ok && ggui.Untrack(func() dotState { return m.dotOf(s) }) == dotWorking {
		return nil, Tf("%s is still working", targetLabel(winner.Target)),
			T("Wait for it to finish, or stop it, before you pick it as the winner.")
	}
	return wt, "", ""
}

// askPickRaceWinner asks before merging the winner. It first reads what
// the winner's worktree has not committed, so the question says what is
// committed for it; an agent still at work is not picked at all, as its
// files are still changing, nor is one while agents are still starting.
// The question is asked in the window the race dialog is in, above it.
func (m *model) askPickRaceWinner(groupID, winnerSessionID string) {
	st := m.Races
	if ggui.Peek(st.Busy) != "" {
		return
	}
	group, ok := raceGroupByID(ggui.Peek(st.Groups), groupID)
	if !ok {
		return
	}
	ask := raceAsk{Group: groupID, Winner: winnerSessionID, Entries: raceEntryIDs(group.Entries)}
	refuse := func(title, why string) {
		m.toaster.Push(ui.Toast(title, why).Destructive())
	}
	if why := raceChanged(ggui.Peek(st.Groups), ask); why != "" {
		refuse(T("Could not merge winner"), why)
		return
	}
	winner, losers, _ := partitionRaceEntries(group.Entries, winnerSessionID)
	wt, title, why := m.raceWinnerReady(winner)
	if why != "" {
		refuse(title, why)
		return
	}
	st.Busy.Set(groupID)
	m.readUncommitted(winner.NodeID, wt.GetPath(), func(ch *protocol.GitChanges, err error) {
		st.Busy.Set("")
		if err != nil {
			refuse(T("Could not read the winner's changes"), nodeErr(err))
			return
		}
		key, dirty, blocked := raceFiles(ch)
		if blocked != "" {
			refuse(T("Could not merge winner"), blocked)
			return
		}
		// The race may have changed while the files were read.
		if why := raceChanged(ggui.Peek(st.Groups), ask); why != "" {
			refuse(T("Could not merge winner"), why)
			return
		}
		ask.Files, ask.Count = key, dirty
		m.askConfirm(confirmation{
			Title:  Tf("Merge %s and clean up %d other worktree(s)?", targetLabel(winner.Target), len(losers)),
			Body:   raceWinnerBody(winner, wt, group.Prompt, dirty),
			Action: T("Merge winner"),
			Owner:  ggui.Peek(st.Owner),
			Run:    func() { m.applyRaceWinner(ask) },
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
// worktree has not, then stops the losers and deletes their worktrees.
// It does so only as the user was asked: the race, its contestants and
// the winner's uncommitted files are checked again first, and anything
// changed since stops it before anything is committed.
func (m *model) applyRaceWinner(ask raceAsk) {
	st := m.Races
	if ggui.Peek(st.Busy) != "" {
		return
	}
	stop := func(why string) {
		st.Busy.Set("")
		m.toaster.Push(ui.Toast(T("Nothing was merged"), why+" "+T("Pick the winner again.")).Destructive())
	}
	if why := raceChanged(ggui.Peek(st.Groups), ask); why != "" {
		stop(why)
		return
	}
	group, _ := raceGroupByID(ggui.Peek(st.Groups), ask.Group)
	winner, losers, _ := partitionRaceEntries(group.Entries, ask.Winner)
	wt, title, why := m.raceWinnerReady(winner)
	if why != "" {
		m.toaster.Push(ui.Toast(title, why).Destructive())
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
			m.toaster.Push(ui.Toast(Tf("Merged winner %s", targetLabel(winner.Target)), cmp.Or(wt.GetBranch(), winner.Branch)))
			m.cleanUpRaceLosers(group.ID, cleanup)
		})
	}
	m.readUncommitted(winner.NodeID, wt.GetPath(), func(ch *protocol.GitChanges, err error) {
		if err != nil {
			fail(T("Could not read the winner's changes"), err)
			return
		}
		key, dirty, blocked := raceFiles(ch)
		if blocked != "" {
			stop(blocked)
			return
		}
		if key != ask.Files {
			st.Busy.Set("")
			m.toaster.Push(ui.Toast(T("The winner's worktree changed"),
				Tf("It had %d uncommitted file(s) when you were asked, and has %d now, or they changed. Nothing was committed or merged: pick the winner again.", ask.Count, dirty)).Destructive())
			return
		}
		// What may have changed while the files were read.
		if why := raceChanged(ggui.Peek(st.Groups), ask); why != "" {
			stop(why)
			return
		}
		if _, title, why := m.raceWinnerReady(winner); why != "" {
			st.Busy.Set("")
			m.toaster.Push(ui.Toast(title, why).Destructive())
			return
		}
		if dirty == 0 {
			merge()
			return
		}
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
	})
}

// markRaceRemoved notes in race groupID that the loser session id ran in
// was deleted.
func (m *model) markRaceRemoved(groupID, id string) {
	m.Races.Groups.Update(func(list []raceGroup) []raceGroup {
		next := slices.Clone(list)
		for i := range next {
			if next[i].ID != groupID {
				continue
			}
			entries := slices.Clone(next[i].Entries)
			for j := range entries {
				if entries[j].SessionID == id {
					entries[j].Removed = true
				}
			}
			next[i].Entries = entries
		}
		return next
	})
}

// cleanUpRaceLosers stops the losing sessions of race groupID and, once
// they have stopped, deletes their worktrees and branches with whatever is
// left in them, which the user agreed to when they picked the winner. A
// session that did not stop keeps its worktree, and every worktree not
// deleted is said. The race stays busy until the last is done.
func (m *model) cleanUpRaceLosers(groupID string, losers []raceLoser) {
	st := m.Races
	if len(losers) == 0 {
		st.Busy.Set("")
		return
	}
	ids := make([]string, len(losers))
	for i, l := range losers {
		ids[i] = l.SessionID
	}
	m.killAll(ids, func() {
		var failed []string
		removed := 0
		report := func() {
			st.Busy.Set("")
			if len(failed) > 0 {
				m.toaster.Push(ui.Toast(Tf("Could not clean up %d race worktree(s)", len(failed)), strings.Join(failed, "\n")).Destructive())
			}
			if removed > 0 {
				m.toaster.Push(ui.Toast(Tf("Removed %d other race worktree(s)", removed), T("Their sessions stopped, and their branches were deleted.")))
			}
		}
		// One after another: removals in one repository at once meet on
		// its locks, such as .git/packed-refs.lock and .git/config.lock.
		var remove func(i int)
		remove = func(i int) {
			if i == len(losers) {
				report()
				return
			}
			l := losers[i]
			label := targetLabel(l.Target)
			if _, alive := m.session(l.SessionID); alive {
				failed = append(failed, Tf("%s: its session did not stop, so its worktree was kept", label))
				remove(i + 1)
				return
			}
			if l.wt == nil {
				remove(i + 1)
				return
			}
			req := &protocol.GitRun{NodeId: m.remoteID(l.NodeID), Root: l.wt.GetRepo(), Op: protocol.GitOpRemoveWT, Worktree: l.wt, Force: true}
			fetch(m, 2*time.Minute, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
				return c.GitRun(ctx, req)
			}, func(_ *protocol.GitResult, err error) {
				if err != nil {
					failed = append(failed, label+": "+nodeErr(err))
				} else {
					removed++
					m.markRaceRemoved(groupID, l.SessionID)
				}
				remove(i + 1)
			})
		}
		remove(0)
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

// raceFooterButton opens the race dialog in the main window, from its
// sidebar footer.
func raceFooterButton(m *model) ggui.Widget { return raceButtonFor(m, "") }

// raceButtonFor opens the race dialog in the window owner names, there
// while at least one race exists: the main window's sidebar footer, or a
// Space window's header.
func raceButtonFor(m *model, owner string) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		groups := m.Races.Groups.Get()
		if len(groups) == 0 {
			return widget.Box()
		}
		tip := Tf("Compare agent races (%d)", len(groups))
		btn := ui.ButtonOf(icon("columns-2").Color(skin.AccentBright), func() { m.openRaceDialogIn(owner) }).Ghost().Pad(6).Radius(6).Name(T("Compare agent races"))
		return ui.Tooltip(btn, tip)
	})
}

// openRaceSession shows session id in the window the race dialog is in,
// owner.
func (m *model) openRaceSession(owner, id string) {
	m.openInboxSession(m.spaceWindows[owner], id, false)
}

// raceCardWidth is how wide a contestant's card is once a race has more
// of them than fit side by side; they then scroll sideways.
const raceCardWidth = 300.0

// raceDialogMaxHeight is the most of the window the race dialog's body
// takes before it scrolls.
const raceDialogMaxHeight = 560.0

// raceDialog is the race dialog of the main window.
func raceDialog(m *model) ggui.Widget { return raceDialogFor(m, "") }

// raceDialogFor is the race dialog in the window owner names: "" for the
// main window, or a Space window's spaceID. It shows in one window at a
// time, the one it was opened in.
func raceDialogFor(m *model, owner string) ggui.Widget {
	st := m.Races
	open := ggui.Bind(func() bool { return st.Open.Get() && st.Owner.Get() == owner }, func(v bool) {
		switch {
		case v:
			st.Owner.Set(owner)
			st.Open.Set(true)
		case ggui.Peek(st.Owner) == owner:
			st.Open.Set(false)
		}
	})
	// What the dialog shows: the race picked, who in it has started, and
	// who is at work, so a contestant that stops has its diff read again.
	shown := ggui.Derived(func() string {
		g, ok := activeRace(st.Groups.Get(), st.Active.Get())
		if !ok {
			return ""
		}
		sessions := m.Sessions.Get()
		key := g.ID
		for _, e := range g.Entries {
			key += "\x00" + e.SessionID
			if s, ok := sessionIn(sessions, e.SessionID); ok && m.dotOf(s) == dotWorking {
				key += "*"
			}
		}
		return key
	})
	// Its diffs are read again as it opens, when another race is picked,
	// when a contestant joins the race shown, and when one stops working.
	ggui.Effect(func() ggui.Cleanup {
		if open.Get() && shown.Get() != "" {
			m.refreshActiveRaceDiffs()
		}
		return nil
	})
	body := ggui.Reactive(func() ggui.Widget {
		if !open.Get() {
			return widget.Box()
		}
		groups := st.Groups.Get()
		cur, ok := activeRace(groups, st.Active.Get())
		if !ok {
			return muted(widget.Text(T("No comparison races started yet. Pick Compare in a New session tab to run two or more agents side by side.")).Size(13))
		}
		changes := st.Changes.Get()
		errs := st.Errs.Get()
		sessions := m.Sessions.Get()
		busy := st.Busy.Get()

		raceID := cur.ID
		topRow := []ggui.Widget{
			widget.Expanded(widget.Text(firstLine(cur.Prompt)).Size(13.5).Weight(ggui.WeightSemibold).NoWrap().Ellipsis()),
		}
		if cur.Starting > 0 {
			topRow = append(topRow, ui.Spinner().Size(13), muted(widget.Text(Tf("Starting %d more agent(s)…", cur.Starting)).Size(12).NoWrap()))
		}
		topRow = append(topRow,
			ui.Button(T("Refresh"), m.refreshActiveRaceDiffs).Ghost().Pad(4, 10).Radius(6),
			ui.Tooltip(ui.Button(T("Dismiss race"), func() { m.dismissRace(raceID) }).Ghost().Pad(4, 10).Radius(6).
				Disabled(busy == raceID || cur.Starting > 0),
				T("Forget this race. Its sessions, worktrees and branches stay as they are.")),
		)
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

		wide := len(cur.Entries) <= 2
		cards := make([]ggui.Widget, 0, len(cur.Entries))
		for _, e := range cur.Entries {
			s, hasSess := sessionIn(sessions, e.SessionID)
			ch := changes[e.SessionID]
			errText := errs[e.SessionID]
			isWinner := cur.WinnerID != "" && cur.WinnerID == e.SessionID

			var added, removed int64
			for _, f := range ch.GetFiles() {
				added += f.GetAdded()
				removed += f.GetRemoved()
			}

			// A contestant is listed once it has started, so one without
			// its session has ended: stopped as a loser, or otherwise.
			statusText := T("Session ended")
			statusCol := skin.Muted
			switch {
			case e.Removed:
				statusText = T("Worktree removed")
			case hasSess:
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
			switch {
			case e.Removed:
				cardRows = append(cardRows, muted(widget.Text(T("Its session stopped, and its worktree and branch were deleted.")).Size(12)))
			case ch != nil:
				cardRows = append(cardRows, widget.Text(Tf("%d files changed · +%d -%d", len(ch.GetFiles()), added, removed)).Size(12).Weight(ggui.WeightMedium))
				for _, file := range ch.GetFiles()[:min(len(ch.GetFiles()), 6)] {
					row := widget.Row(
						widget.Expanded(widget.Text(file.GetPath()).Size(11.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
						widget.Text(fmt.Sprintf("+%d -%d", file.GetAdded(), file.GetRemoved())).Size(11).Color(skin.Muted).NoWrap(),
					).Gap(6).Align(widget.AlignCenter)
					if !hasSess {
						// Its session is gone: what it changed is listed,
						// and opened from its worktree's own tab, if any.
						cardRows = append(cardRows, widget.Box(row).Pad(2, 4))
						continue
					}
					cardRows = append(cardRows, ui.ButtonOf(row, func() {
						open.Set(false)
						wt := cmp.Or(s.GetWorktree(), e.Worktree)
						from := diffTab{Root: fileRoot{Node: s.GetNodeId(), Path: s.GetProject()}, Repo: s.GetProject(), Base: wt.GetBase()}
						m.openDiffIn(m.spaceWindows[owner], from, file)
					}).Ghost().Pad(2, 4).Radius(4).Name(file.GetPath()))
				}
				if len(ch.GetFiles()) > 6 {
					cardRows = append(cardRows, muted(widget.Text(Tf("and %d more", len(ch.GetFiles())-6)).Size(11.5)))
				}
			case errText == "":
				cardRows = append(cardRows, muted(widget.Text(T("No diff summary loaded yet.")).Size(12)))
			}
			if errText != "" && !e.Removed {
				cardRows = append(cardRows, widget.Text(Tf("Could not read its changes: %s", errText)).Size(12).Color(skin.DotDown).Selectable())
			}

			var footer []ggui.Widget
			if hasSess {
				id := e.SessionID
				footer = append(footer, ui.Button(T("Open session"), func() {
					open.Set(false)
					m.openRaceSession(owner, id)
				}).Outline().Pad(4, 10).Radius(6))
			}
			if isWinner {
				footer = append(footer, widget.Box(widget.Text(T("Winner")).Size(11.5).Weight(ggui.WeightSemibold).Color(skin.DotRunning).NoWrap()).
					Fill(skin.DotRunning.Alpha(0.15)).Radius(6).Pad(4, 8))
			} else if cur.WinnerID == "" && !e.Removed {
				id := e.SessionID
				footer = append(footer, ui.Button(T("Pick winner & merge"), func() {
					m.askPickRaceWinner(raceID, id)
				}).Pad(4, 10).Radius(6).Disabled(busy != "" || cur.Starting > 0))
			}
			if len(footer) > 0 {
				cardRows = append(cardRows, widget.Row(footer...).Gap(8).Align(widget.AlignCenter))
			}

			borderCol := skin.Border
			if isWinner {
				borderCol = skin.DotRunning
			}
			card := widget.Box(widget.Column(cardRows...).Gap(8).Stretch()).
				Fill(skin.Surface1).Border(1, borderCol).Radius(10).Pad(12, 14)
			if wide {
				cards = append(cards, widget.Expanded(card))
			} else {
				cards = append(cards, card.Width(raceCardWidth))
			}
		}

		// Two cards share the dialog's width; more keep theirs and scroll
		// sideways rather than squeeze.
		var cardRow ggui.Widget = widget.Row(cards...).Gap(12).Stretch()
		if !wide {
			cardRow = widget.Scroll(cardRow).Horizontal().Bar(skin.Surface3)
		}
		return widget.Column(
			widget.Row(topRow...).Gap(8).Align(widget.AlignCenter),
			cardRow,
		).Gap(12).Stretch()
	})
	content := widget.Box(widget.Scroll(body).Shrink().Bar(skin.Surface3)).MaxHeight(raceDialogMaxHeight)
	return keyedDialog(open, content).Title(T("Compare agent races")).Width(760)
}
