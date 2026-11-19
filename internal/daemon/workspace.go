package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/workspace"
	"github.com/ironpark/mako/pkg/crdt"
	"github.com/ironpark/mako/pkg/fsutil"
)

// The workspace: the spaces, projects, names, looks, pins and layouts of
// one person's nodes, as a CRDT (internal/workspace). The daemon holds it
// and stamps the edits its front-ends give it, as deltas, with its own
// clock. While the node shares it (config.Workspace), it goes whole with
// the daemon's state to the peers it and they let each other in, and
// theirs is merged into it field by field, so that each node's edits stay
// whatever the others did meanwhile. Its projects, as far as their folders
// are on this node, are those the node's agents are grouped by.
//
// Before the workspace was a CRDT, a window kept it as one document, the
// later edit winning whole, and the daemon kept the latest. Such a document
// is taken into the state once, whole, stamped with the edit it was: the
// one workspace.pb kept, when the daemon first starts with a state, and
// the one a window kept, which it gives the daemon once when it first
// runs (Workspace.doc).

// workspaces holds the workspace, and keeps it on disk: workspace.pb, a
// Workspace with the state. Whether the clock times its stamps is the
// state's to say (workspace.Anchored).
type workspaces struct {
	mu   sync.Mutex
	dir  string
	path string
	// state is never changed in place, only replaced, so a reader may
	// keep it.
	state  *protocol.WorkspaceState
	digest []byte
	clock  *crdt.Clock
	// writeMu has one write at a time, of the latest, outside mu.
	writeMu sync.Mutex
}

// physNow is the time stamps are made and judged by, in Unix ms.
func physNow() int64 { return time.Now().UnixMilli() }

// load reads the workspace kept in dir, for the node node. A workspace
// kept before it was a CRDT is taken in from its document.
func (w *workspaces) load(dir, node string) {
	w.mu.Lock()
	w.dir, w.path = dir, filepath.Join(dir, "workspace.pb")
	kept := &protocol.Workspace{}
	if data, err := os.ReadFile(w.path); err == nil && proto.Unmarshal(data, kept) != nil {
		kept = &protocol.Workspace{}
	}
	st, now := kept.GetState(), physNow()
	changed := false
	if workspace.Empty(st) && len(kept.GetDoc()) > 0 {
		if took, err := workspace.FromLegacy(kept.GetDoc(), now); err == nil {
			st, changed = took, true
		}
	}
	if st == nil {
		st = &protocol.WorkspaceState{}
	}
	w.clock = crdt.NewClock(node, workspace.Anchored(st, node))
	workspace.Observe(st, w.clock, now)
	// Stamped while this node's clock was wrong, and since put right.
	changed = workspace.Heal(st, w.clock, now) || changed
	w.setLocked(st)
	w.mu.Unlock()
	if changed {
		w.save()
	}
}

// setLocked holds st as the state.
func (w *workspaces) setLocked(st *protocol.WorkspaceState) {
	w.state, w.digest = st, workspace.Digest(st)
}

// get is the workspace held: its state, and its digest unless
// withDigest is false.
func (w *workspaces) get(withDigest bool) *protocol.Workspace {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := &protocol.Workspace{State: w.state}
	if withDigest {
		out.Digest = w.digest
	}
	return out
}

// shared is the workspace as it goes to the peers: the state and its
// digest, and no document, which a daemon from before the CRDT, keeping
// only those with a version, leaves.
func (w *workspaces) shared() *protocol.Workspace { return w.get(true) }

// currentDigest is the digest of the state held.
func (w *workspaces) currentDigest() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.digest
}

// edit stamps a front-end's delta and merges it in, and reports whether the
// state changed. The delta is the caller's, and is changed.
func (w *workspaces) edit(delta *protocol.WorkspaceState) bool {
	_, changed, _ := w.editChecked(delta, nil)
	return changed
}

// editChecked is edit, but for a delta check refuses against the state it
// would be merged into, which it does under the same lock; it returns the
// state before too.
func (w *workspaces) editChecked(delta *protocol.WorkspaceState, check func(*protocol.WorkspaceState) error) (before *protocol.WorkspaceState, changed bool, err error) {
	changed = w.change(func(next *protocol.WorkspaceState, now int64) bool {
		before = w.state
		if check != nil {
			if err = check(next); err != nil {
				return false
			}
		}
		healed := workspace.Heal(next, w.clock, now)
		workspace.Stamp(delta, w.clock, now)
		return workspace.Merge(next, delta, w.clock, now) || healed
	})
	return before, changed, err
}

// takeLegacy takes in whole a workspace a window kept before it was a
// CRDT, stamped with the edit it was, and reports whether the state
// changed. What it has the state has too is decided as any merge is; what
// only one has is kept, so nothing of either is lost.
func (w *workspaces) takeLegacy(doc []byte) bool {
	return w.change(func(next *protocol.WorkspaceState, now int64) bool {
		took, err := workspace.FromLegacy(doc, now)
		if err != nil {
			return false
		}
		merged := workspace.Merge(next, took, w.clock, now)
		if workspace.Anchored(next, w.clock.NodeID()) {
			w.clock.Anchor()
		}
		return merged
	})
}

// merge merges a peer's workspace into the state, entries stamped too far
// ahead of this node's time left out, and reports whether the state
// changed; the clock times its stamps from then on. A peer from before the
// CRDT sends a document, whose edits cannot be told from what it never
// had: it is taken in only into a state that holds nothing.
func (w *workspaces) merge(ws *protocol.Workspace) bool {
	if st := ws.GetState(); st != nil {
		if len(ws.GetDigest()) > 0 && bytes.Equal(ws.GetDigest(), w.currentDigest()) {
			return false
		}
		return w.change(func(next *protocol.WorkspaceState, now int64) bool {
			changed := workspace.Merge(next, st, w.clock, now)
			w.clock.Anchor()
			return changed
		})
	}
	if len(ws.GetDoc()) == 0 {
		return false
	}
	return w.change(func(next *protocol.WorkspaceState, now int64) bool {
		if !workspace.Empty(next) {
			return false
		}
		took, err := workspace.FromLegacy(ws.GetDoc(), now)
		if err != nil {
			return false
		}
		changed := workspace.Merge(next, took, w.clock, now)
		w.clock.Anchor()
		return changed
	})
}

// change changes a copy of the state with fn, which reports whether it
// changed it, and holds and writes down the copy if it did.
func (w *workspaces) change(fn func(next *protocol.WorkspaceState, now int64) bool) bool {
	w.mu.Lock()
	next := proto.CloneOf(w.state)
	if !fn(next, physNow()) {
		w.mu.Unlock()
		return false
	}
	w.setLocked(next)
	w.mu.Unlock()
	w.save()
	return true
}

// scopes are this node's projects, as the state has them; ok is false
// while the state holds no project, created or deleted, to say so.
func (w *workspaces) scopes(node string) (scopes []*protocol.ProjectScope, ok bool) {
	w.mu.Lock()
	st := w.state
	w.mu.Unlock()
	if len(st.GetProjects()) == 0 {
		return nil, false
	}
	return workspace.Scopes(st, node), true
}

// holdsProjects reports whether the state says what the projects are.
func (w *workspaces) holdsProjects() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.state.GetProjects()) > 0
}

// save writes the workspace down, off the lock that readers take.
func (w *workspaces) save() {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.mu.Lock()
	kept := &protocol.Workspace{State: w.state}
	path := w.path
	w.mu.Unlock()
	data, err := proto.Marshal(kept)
	if path != "" && err == nil {
		_ = fsutil.Write(path, data)
	}
}

// syncsWorkspace reports whether this node shares its workspace with its
// peers.
func (d *Daemon) syncsWorkspace() bool {
	return d.followed().workspace.Syncs(d.workspace.dir)
}

// workspaceChanged tells the front-ends watching that the workspace
// changed, and has the projects follow it.
func (d *Daemon) workspaceChanged() {
	d.changes.fireWorkspace()
	d.syncWorkspaceProjects()
}

// syncWorkspaceProjects keeps as this node's projects those the workspace
// holds, once it holds any, and tells the front-ends watching when they
// changed, as a front-end's SetProjects does.
func (d *Daemon) syncWorkspaceProjects() {
	scopes, ok := d.workspace.scopes(d.cfg.NodeID)
	if !ok {
		return
	}
	before := d.projects.get()
	after := d.projects.set(scopes).GetProjects()
	if !slices.EqualFunc(before, after, func(a, b *protocol.ProjectScope) bool { return proto.Equal(a, b) }) {
		// The Agents panels and list_agents follow at once.
		d.changes.fire(false, true)
	}
}

// takeWorkspace takes in the workspace a peer sent with its state, one this
// node and it let each other in. A peer whose workspace differs, but which
// sent only its digest, is asked for it.
func (d *Daemon) takeWorkspace(from string, ws *protocol.Workspace) {
	switch {
	case ws == nil:
	case ws.GetState() == nil && len(ws.GetDigest()) > 0:
		if !bytes.Equal(ws.GetDigest(), d.workspace.currentDigest()) {
			d.fetchWorkspace(from)
		}
	case d.workspace.merge(ws):
		d.workspaceChanged()
	}
}

// fetchWorkspace asks the peer node for its workspace and merges it in,
// once at a time.
func (d *Daemon) fetchWorkspace(node string) {
	d.mu.Lock()
	if d.fetching == nil {
		d.fetching = map[string]bool{}
	}
	if d.fetching[node] {
		d.mu.Unlock()
		return
	}
	d.fetching[node] = true
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.fetching, node)
			d.mu.Unlock()
		}()
		ws, err := forward[*protocol.Workspace](d.ctx, d, protocol.TypeGetWorkspace, &protocol.GetWorkspace{NodeId: node})
		// One that answers without a digest has stopped sharing since.
		if err != nil || ws.GetState() == nil || len(ws.GetDigest()) == 0 {
			return
		}
		if d.workspace.merge(&protocol.Workspace{State: ws.GetState(), Digest: ws.GetDigest()}) {
			d.workspaceChanged()
		}
	}()
}

// handleWorkspace answers a front-end asking for the workspace, or giving
// an edit of it, or the workspace it kept before it was a CRDT, with the
// one the daemon holds after. Another node's is asked of it: a node that
// lets this one in, which runs its programs already, shows it its
// workspace to copy from, whether it shares it or not. Its digest goes
// with it only while the node shares it: a peer merges only what comes
// with one (fetchWorkspace).
//
// An edit is given to another node as a request is, by to_node. A node
// takes one from a peer it lets in as from its own front-ends, stamped by
// its own clock and merged: a node let in starts sessions and runs
// programs here already, so editing the workspace is no more than it can
// do. A phone, which lets no one in and so merges no computer's
// workspace, shows and edits a computer's this way. A peer gives a delta
// only, never a document from before the CRDT.
func (d *Daemon) handleWorkspace(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if env.Type() == protocol.TypeGetWorkspace {
		return serve(d, ctx, stream, env, protocol.TypeWorkspace, func(*protocol.GetWorkspace) (*protocol.Workspace, error) {
			if !fromPeer {
				return d.frontWorkspace(), nil
			}
			return d.workspace.get(d.syncsWorkspace()), nil
		})
	}
	ws, err := protocol.Body[*protocol.Workspace](env)
	if err != nil {
		return err
	}
	if !d.local(ws.GetToNode()) {
		// The asker's bound goes on with it, as serve passes it on.
		if within := transport.Within(env); within > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, within)
			defer cancel()
		}
		reply, err := forward[*protocol.Workspace](ctx, d, env.Type(), givenTo{ws})
		if err != nil {
			return err
		}
		return stream.Send(protocol.TypeWorkspace, env.GetId(), reply)
	}
	if fromPeer && (ws.GetState() == nil || len(ws.GetDoc()) > 0) {
		return errors.New("a peer gives the workspace a delta only")
	}
	changed := false
	switch {
	case ws.GetState() != nil:
		changed, _ = d.editWorkspace(ws.GetState(), nil, true)
	case len(ws.GetDoc()) > 0:
		changed = d.workspace.takeLegacy(ws.GetDoc())
	}
	if changed {
		d.workspaceChanged()
	}
	if fromPeer {
		return stream.Send(protocol.TypeWorkspace, env.GetId(), d.workspace.get(d.syncsWorkspace()))
	}
	return stream.Send(protocol.TypeWorkspace, env.GetId(), d.frontWorkspace())
}

// givenTo is an edit of the workspace as it is routed: by to_node, as
// other requests are by node_id, which Workspace holds for another use.
type givenTo struct{ *protocol.Workspace }

func (w givenTo) GetNodeId() string { return w.GetToNode() }
