package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The windows' workspace: their tabs and panes, which the front-end writes
// down and the daemon keeps, the latest of those a window of its gave it
// and those its peers sent. It goes out with the daemon's state to the
// peers it trusts both ways, which take it up when it is newer than their
// own, so the windows of one person's nodes follow each other. Whether a
// window does is its own setting: one that does not follows nothing and
// gives nothing.

// workspaces holds the latest workspace, and keeps it on disk.
type workspaces struct {
	mu      sync.Mutex
	cur     *protocol.Workspace
	path    string
	writeMu sync.Mutex // one write at a time, of the latest, outside mu
}

// load reads the workspace kept in dir, if there is one.
func (w *workspaces) load(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.path = filepath.Join(dir, "workspace.pb")
	data, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	cur := &protocol.Workspace{}
	if proto.Unmarshal(data, cur) == nil {
		w.cur = cur
	}
}

// get is the workspace held, nil for none.
func (w *workspaces) get() *protocol.Workspace {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// put keeps ws if it is newer than the one held, and reports whether it
// did. A tie of times goes to the greater node ID, as everywhere.
func (w *workspaces) put(ws *protocol.Workspace) bool {
	if ws == nil || ws.GetVersion() == 0 {
		return false
	}
	w.mu.Lock()
	if cur := w.cur; cur != nil && !protocol.Newer(ws.GetVersion(), ws.GetNodeId(), cur.GetVersion(), cur.GetNodeId()) {
		w.mu.Unlock()
		return false
	}
	w.cur = proto.CloneOf(ws)
	w.mu.Unlock()
	w.save()
	return true
}

// save writes the latest workspace down, off the lock that readers take.
func (w *workspaces) save() {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.mu.Lock()
	data, _ := proto.Marshal(w.cur)
	path := w.path
	w.mu.Unlock()
	if path != "" {
		_ = writeAtomic(path, data)
	}
}

// handleWorkspace answers a front-end asking for the workspace, or giving
// its own, with the one the daemon holds after.
func (d *Daemon) handleWorkspace(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errors.New("the workspace is for this node's own front-ends")
	}
	if env.Type() == protocol.TypeWorkspace {
		ws, err := protocol.Body[*protocol.Workspace](env)
		if err != nil {
			return err
		}
		if d.workspace.put(ws) {
			// The daemon's other windows follow, and its peers with the
			// next state.
			d.changes.fireWorkspace()
		}
	}
	cur := d.workspace.get()
	if cur == nil {
		cur = &protocol.Workspace{}
	}
	return stream.Send(protocol.TypeWorkspace, env.GetId(), cur)
}
