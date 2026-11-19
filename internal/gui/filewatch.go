package gui

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// The window has each node follow what it shows of the node's files
// (watch_files): the folders listed, in the Files tab and the file
// browsers, the files open in the editor or the preview, and the work
// trees whose changes the Git tab shows. As the node tells of a change,
// the window reads again what changed, rather than polling: a folder's
// entries, an open file, a tree's changes. A node older than watch_files,
// or not yet watching, is polled as before.

// nodeWatch is the stream that follows one node's files.
type nodeWatch struct {
	cancel context.CancelFunc
	want   atomic.Pointer[protocol.WatchFiles] // what to follow, the latest
	more   chan *protocol.WatchFiles           // what changed of it, for the stream
	live   atomic.Bool                         // the node watches now
}

// fileWatchRetry is how long a stream that ended waits to be opened again.
const fileWatchRetry = 2 * time.Second

// followFiles keeps a stream to each node with something to follow, and
// what it follows in step with what the window shows. It runs under the
// window's owner.
func followFiles(m *model) {
	ggui.Effect(func() ggui.Cleanup {
		want := map[string]*protocol.WatchFiles{}
		add := func(node string) *protocol.WatchFiles {
			if !m.nodeAt(node, protocol.RevisionWatchFiles) {
				return nil
			}
			r := want[node]
			if r == nil {
				r = &protocol.WatchFiles{NodeId: m.remoteID(node)}
				want[node] = r
			}
			return r
		}
		m.Nodes.Get()
		m.Conn.Get()
		for key := range m.files.Dirs.Get() {
			if node, p := splitKey(key); p != "" {
				if r := add(node); r != nil {
					r.Paths = append(r.Paths, p)
				}
			}
		}
		for _, d := range m.Docs.Get() {
			if r := add(d.Node); r != nil && d.Path != "" {
				r.Paths = append(r.Paths, d.Path)
			}
		}
		if p := m.files.Preview.Get(); p.Path != "" && !p.Dir {
			if r := add(p.Node); r != nil {
				r.Paths = append(r.Paths, p.Path)
			}
		}
		for key, n := range m.files.Trees.Get() {
			if node, p := splitKey(key); n > 0 {
				if r := add(node); r != nil {
					r.Trees = append(r.Trees, p)
				}
			}
		}
		ggui.Untrack(func() struct{} { m.watchFiles(want); return struct{}{} })
		return nil
	})
	ggui.OnCleanup(func() { m.watchFiles(nil) })
}

// watchFiles has each node of want follow what it says, and the nodes
// left out follow nothing.
func (m *model) watchFiles(want map[string]*protocol.WatchFiles) {
	for node, w := range m.files.watches {
		if want[node] == nil {
			w.cancel()
			delete(m.files.watches, node)
		}
	}
	for node, r := range want {
		slices.Sort(r.Paths)
		r.Paths = slices.Compact(r.Paths)
		slices.Sort(r.Trees)
		r.Trees = slices.Compact(r.Trees)
		w := m.files.watches[node]
		if w == nil {
			ctx, cancel := context.WithCancel(context.Background())
			w = &nodeWatch{cancel: cancel, more: make(chan *protocol.WatchFiles, 1)}
			w.want.Store(r)
			m.files.watches[node] = w
			go m.runFileWatch(ctx, node, w)
			continue
		}
		if proto.Equal(w.want.Load(), r) {
			continue
		}
		w.want.Store(r)
		// Only the latest waits to be sent.
		select {
		case <-w.more:
		default:
		}
		w.more <- r
	}
}

// runFileWatch keeps node's stream open until ctx ends, opening it again
// as it ends; on opening again, what it may have missed is read again.
func (m *model) runFileWatch(ctx context.Context, node string, w *nodeWatch) {
	for opened := false; ctx.Err() == nil; {
		if c, err := m.link.client(ctx); err == nil {
			// What waits to be sent is in what the stream starts with.
			select {
			case <-w.more:
			default:
			}
			_ = c.WatchFiles(ctx, w.want.Load(), w.more, func(ch *protocol.FilesChanged) {
				if !w.live.Swap(true) {
					if opened {
						m.post(func() { m.rereadNode(node) })
					}
					opened = true
					return
				}
				m.post(func() { m.filesChanged(node, ch) })
			})
		}
		w.live.Store(false)
		select {
		case <-ctx.Done():
		case <-time.After(fileWatchRetry):
		}
	}
}

// fileWatched reports whether node follows the window's files now, so
// that what would poll them may rest.
func (m *model) fileWatched(node string) bool {
	w := m.files.watches[node]
	return w != nil && w.live.Load()
}

// watchTree has root's node follow the changes in its work tree while a
// view shows them, until the cleanup it returns runs.
func (m *model) watchTree(root fileRoot) ggui.Cleanup {
	key := dirKey(root.Node, root.Path)
	count := func(by int) {
		m.files.Trees.Update(func(t map[string]int) map[string]int {
			next := maps.Clone(t)
			if next[key] += by; next[key] <= 0 {
				delete(next, key)
			}
			return next
		})
	}
	count(1)
	return func() { count(-1) }
}

// filesChanged reads again what node says changed: the folders listed
// that it changed in, the files open, the work tree shown.
func (m *model) filesChanged(node string, ch *protocol.FilesChanged) {
	if paths := ch.GetPaths(); len(paths) > 0 {
		dirs := ggui.Peek(m.files.Dirs)
		reread := map[string]bool{}
		for _, p := range paths {
			for _, dir := range []string{p, parentPath(p)} {
				if _, listed := dirs[dirKey(node, dir)]; listed {
					reread[dir] = true
				}
			}
		}
		for dir := range reread {
			m.loadDir(node, dir, true)
		}
		for id, d := range ggui.Peek(m.Docs) {
			if d.Node == node && slices.Contains(paths, d.Path) {
				m.checkDoc(id)
			}
		}
		if p := ggui.Peek(m.files.Preview); p.Node == node && !p.Dir && slices.Contains(paths, p.Path) {
			m.openPreview(p.fileItem)
		}
	}
	if root := ggui.Peek(m.git.View).Root; root.Node == node && slices.Contains(ch.GetTrees(), root.Path) {
		m.loadGit(root)
	}
}

// rereadNode reads again all the window shows of node's files, as after
// its stream opened again, which may have missed changes.
func (m *model) rereadNode(node string) {
	for key := range ggui.Peek(m.files.Dirs) {
		if n, p := splitKey(key); n == node {
			m.loadDir(n, p, true)
		}
	}
	for id, d := range ggui.Peek(m.Docs) {
		if d.Node == node {
			m.checkDoc(id)
		}
	}
	if root := ggui.Peek(m.git.View).Root; root.Node == node {
		m.loadGit(root)
	}
}

// parentPath is the folder p is in, with either separator.
func parentPath(p string) string {
	i := strings.LastIndexAny(strings.TrimRight(p, `/\`), `/\`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	}
	return p[:i]
}

// nodeAt reports whether node runs revision or later: this window's own
// daemon, or another node it knows.
func (m *model) nodeAt(node string, revision int64) bool {
	if self := m.Conn.Get().Self; self.GetId() == node || node == "" {
		return self.GetRevision() >= revision
	}
	for _, n := range m.Nodes.Get() {
		if n.GetId() == node {
			return n.GetRevision() >= revision
		}
	}
	return false
}
