package gui

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// diffChanges tracks the changed-file lists for open diff tabs independently
// of the bundled Git plugin panel: working-tree groups (for siblings and
// live refresh via watchTree / session gitStamp / fallback poll) and commit
// file lists (for stepping through a commit's files).

// diffGroup identifies a set of working-tree changes shared by open diff tabs.
type diffGroup struct {
	Root fileRoot
	Repo string
	Base string
}

// watchRoot is the work tree watched for changes in g.
func (g diffGroup) watchRoot() fileRoot {
	return fileRoot{Node: g.Root.Node, Path: cmp.Or(g.Repo, g.Root.Path)}
}

// diffCommitKey identifies a commit's changed-file list for open commit diff tabs.
type diffCommitKey struct {
	Root   fileRoot
	Repo   string
	Commit string
}

// diffGroupRead coalesces in-flight GitStatus reads per diffGroup.
type diffGroupRead struct {
	busy    bool
	again   bool
	refresh bool
}

// diffChangesState holds the reactive file lists and in-flight tracking for
// open diff tabs. UI goroutine only.
type diffChangesState struct {
	Groups         *ggui.StateValue[map[diffGroup]*protocol.GitChanges]
	Commits        *ggui.StateValue[map[diffCommitKey][]*protocol.GitFile]
	groupReads     map[diffGroup]*diffGroupRead
	commitInFlight map[diffCommitKey]bool
}

func newDiffChangesState() *diffChangesState {
	return &diffChangesState{
		Groups: ggui.State(map[diffGroup]*protocol.GitChanges{}).WithEqual(func(a, b map[diffGroup]*protocol.GitChanges) bool {
			return maps.EqualFunc(a, b, protocol.Same[*protocol.GitChanges])
		}),
		Commits: ggui.State(map[diffCommitKey][]*protocol.GitFile{}).WithEqual(func(a, b map[diffCommitKey][]*protocol.GitFile) bool {
			return maps.EqualFunc(a, b, protocol.Equal[*protocol.GitFile])
		}),
		groupReads:     map[diffGroup]*diffGroupRead{},
		commitInFlight: map[diffCommitKey]bool{},
	}
}

func (d diffTab) group() diffGroup {
	return diffGroup{Root: d.Root, Repo: d.Repo, Base: d.Base}
}

func (d diffTab) commitKey() diffCommitKey {
	return diffCommitKey{Root: d.Root, Repo: d.Repo, Commit: d.Commit}
}

// openDiffGroups returns the unique working-tree groups and commit keys of
// currently open diff tabs.
func openDiffGroups(diffs map[string]diffTab) (map[diffGroup]bool, map[diffCommitKey]bool) {
	groups := map[diffGroup]bool{}
	commits := map[diffCommitKey]bool{}
	for _, d := range diffs {
		if d.Pull > 0 {
			continue
		}
		if d.Commit != "" {
			commits[d.commitKey()] = true
			continue
		}
		groups[d.group()] = true
	}
	return groups, commits
}

// followDiffChanges watches the work trees and session stamps of open
// working-tree diff tabs, and polls those whose nodes do not watch them.
// It runs under the window's owner (setup).
func followDiffChanges(m *model) {
	// Prune cached state when diff tabs close.
	ggui.Watch(m.Diffs, func(diffs map[string]diffTab) {
		groups, commits := openDiffGroups(diffs)
		m.pruneDiffChanges(groups, commits)
	})

	// Track open working-tree groups.
	openGroups := ggui.Derived(func() []diffGroup {
		groups, _ := openDiffGroups(m.Diffs.Get())
		out := make([]diffGroup, 0, len(groups))
		for g := range groups {
			out = append(out, g)
		}
		slices.SortFunc(out, func(a, b diffGroup) int {
			if c := cmp.Compare(a.Root.Node, b.Root.Node); c != 0 {
				return c
			}
			if c := cmp.Compare(a.Root.Path, b.Root.Path); c != 0 {
				return c
			}
			if c := cmp.Compare(a.Repo, b.Repo); c != 0 {
				return c
			}
			return cmp.Compare(a.Base, b.Base)
		})
		return out
	}).WithEqual(slices.Equal)

	// Watch work trees and run fallback poll only while working-tree diff tabs are open.
	ggui.Effect(func() ggui.Cleanup {
		groups := openGroups.Get()
		if len(groups) == 0 {
			return nil
		}
		var cleanups []ggui.Cleanup
		watched := map[fileRoot]bool{}
		for _, g := range groups {
			wr := g.watchRoot()
			if wr.Path != "" && !watched[wr] {
				watched[wr] = true
				cleanups = append(cleanups, m.watchTree(wr))
			}
		}
		stopPoll := m.every(gitRefresh, func() {
			for _, g := range ggui.Peek(openGroups) {
				if !m.treeWatched(g.watchRoot()) {
					m.loadDiffChanges(g, true)
				}
			}
		})
		return func() {
			stopPoll()
			for _, fn := range cleanups {
				fn()
			}
		}
	})

	// Follow session gitStamps for every open working-tree diff group.
	stamps := ggui.Derived(func() map[diffGroup]string {
		groups := openGroups.Get()
		if len(groups) == 0 {
			return nil
		}
		sessions := m.Sessions.Get()
		out := make(map[diffGroup]string, len(groups))
		for _, g := range groups {
			out[g] = repoStamp(sessions, g.Root, g.Repo)
		}
		return out
	}).WithEqual(maps.Equal)

	last := ggui.Peek(stamps)
	ggui.Watch(stamps, func(now map[diffGroup]string) {
		for g, st := range now {
			if prev, ok := last[g]; ok && prev != "" && st != "" && st != prev {
				m.loadDiffChanges(g, true)
			}
		}
		last = now
	})
}

func (m *model) pruneDiffChanges(groups map[diffGroup]bool, commits map[diffCommitKey]bool) {
	if cur := ggui.Peek(m.diffChanges.Groups); len(cur) > 0 {
		for g := range cur {
			if !groups[g] {
				m.diffChanges.Groups.Update(func(v map[diffGroup]*protocol.GitChanges) map[diffGroup]*protocol.GitChanges {
					next := maps.Clone(v)
					for k := range next {
						if !groups[k] {
							delete(next, k)
						}
					}
					return next
				})
				break
			}
		}
	}
	if cur := ggui.Peek(m.diffChanges.Commits); len(cur) > 0 {
		for k := range cur {
			if !commits[k] {
				m.diffChanges.Commits.Update(func(v map[diffCommitKey][]*protocol.GitFile) map[diffCommitKey][]*protocol.GitFile {
					next := maps.Clone(v)
					for ck := range next {
						if !commits[ck] {
							delete(next, ck)
						}
					}
					return next
				})
				break
			}
		}
	}
}

// loadDiffChanges reads the changed files of g from its node. When refreshOpen
// is true, open diff tabs in g are updated via followChanges once the status
// arrives.
func (m *model) loadDiffChanges(g diffGroup, refreshOpen bool) {
	path := cmp.Or(g.Repo, g.Root.Path)
	if path == "" || m.post == nil {
		return
	}
	st := m.diffChanges.groupReads[g]
	if st == nil {
		st = &diffGroupRead{}
		m.diffChanges.groupReads[g] = st
	}
	if st.busy {
		st.again = true
		st.refresh = st.refresh || refreshOpen
		return
	}
	st.busy = true
	req := &protocol.GitStatus{NodeId: m.remoteID(g.Root.Node), Path: path, Base: g.Base}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitChanges, error) {
		return c.GitStatus(ctx, req)
	}, func(ch *protocol.GitChanges, err error) {
		again, againRefresh := st.again, st.refresh
		st.busy, st.again, st.refresh = false, false, false
		if !again {
			delete(m.diffChanges.groupReads, g)
		}
		groups, _ := openDiffGroups(ggui.Peek(m.Diffs))
		if err == nil && groups[g] {
			m.diffChanges.Groups.Update(func(v map[diffGroup]*protocol.GitChanges) map[diffGroup]*protocol.GitChanges {
				next := maps.Clone(v)
				next[g] = ch
				return next
			})
			if refreshOpen {
				m.followChanges(g, ch)
			}
		}
		if again && groups[g] {
			m.loadDiffChanges(g, againRefresh)
		}
	})
}

// loadDiffCommit reads a commit's changed-file list for stepping through an
// open commit diff tab's siblings.
func (m *model) loadDiffCommit(k diffCommitKey) {
	path := cmp.Or(k.Repo, k.Root.Path)
	if path == "" || k.Commit == "" || m.post == nil {
		return
	}
	if _, ok := ggui.Peek(m.diffChanges.Commits)[k]; ok || m.diffChanges.commitInFlight[k] {
		return
	}
	m.diffChanges.commitInFlight[k] = true
	req := &protocol.GitShow{NodeId: m.remoteID(k.Root.Node), Root: path, Hash: k.Commit}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitCommitInfo, error) {
		return c.GitShow(ctx, req)
	}, func(info *protocol.GitCommitInfo, err error) {
		delete(m.diffChanges.commitInFlight, k)
		_, commits := openDiffGroups(ggui.Peek(m.Diffs))
		if err == nil && commits[k] {
			m.diffChanges.Commits.Update(func(v map[diffCommitKey][]*protocol.GitFile) map[diffCommitKey][]*protocol.GitFile {
				next := maps.Clone(v)
				next[k] = info.GetFiles()
				return next
			})
		}
	})
}

// diffTreesChanged reloads open working-tree diff groups whose watched tree
// reported a filesystem change on node.
func (m *model) diffTreesChanged(node string, trees []string) {
	if len(trees) == 0 {
		return
	}
	groups, _ := openDiffGroups(ggui.Peek(m.Diffs))
	for g := range groups {
		if g.Root.Node != node {
			continue
		}
		if slices.Contains(trees, g.watchRoot().Path) || slices.Contains(trees, g.Root.Path) {
			m.loadDiffChanges(g, true)
		}
	}
}

// rereadDiffChanges reloads all open working-tree diff groups on node after
// its file-watch stream reconnects.
func (m *model) rereadDiffChanges(node string) {
	groups, _ := openDiffGroups(ggui.Peek(m.Diffs))
	for g := range groups {
		if g.Root.Node == node {
			m.loadDiffChanges(g, true)
		}
	}
}

// refreshDiffChangesForRoot reloads open working-tree diff groups matching
// node and repo/root path after a direct git operation.
func (m *model) refreshDiffChangesForRoot(node, path string) {
	groups, _ := openDiffGroups(ggui.Peek(m.Diffs))
	for g := range groups {
		if g.Root.Node == node && (g.Repo == path || g.Root.Path == path) {
			m.loadDiffChanges(g, true)
		}
	}
}
