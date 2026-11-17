package gui

import (
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// wsState is how the workspace is kept and written down; see
// workspace.go. Its fields are the UI goroutine's, but for writes, which
// workers wait on.
type wsState struct {
	// The panes of the spaces not shown, what the panes shown are kept by
	// (see layoutKey), and by space the owner or project last shown in it.
	layouts   map[string]*layoutNode
	layoutKey string
	contexts  map[string]string
	// base is the window's document, as JSON, as it last took it from
	// the daemon or gave it the edit to it: what its edits are of. state
	// is the daemon's state it last took, whose ranks its edits are ranked
	// against, and applied that state's view, as it last took one up.
	base    []byte
	state   *protocol.WorkspaceState
	applied []byte
	// giving says an edit is on its way to the daemon, whose answer, the
	// newer, is taken up in place of a state that comes meanwhile. gen
	// counts the daemons the window left, so that an answer of one it
	// left is not taken up.
	giving bool
	gen    int
	// old is the workspace the window kept before it was a CRDT, to give
	// the daemon to take in, once; nil when there is none, or once given.
	old *workspaceFile
	// imported says layout.json was looked at, as the window first took
	// a workspace (winsync.go).
	imported bool
	// give gives the daemon a Workspace, and hands its answer to done on
	// the UI goroutine: the window's daemon (giveDaemon), or a test's.
	give  func(ws *protocol.Workspace, done func(*protocol.Workspace, error))
	timer *time.Timer
	// writer writes workspace.json.
	writer fileWriter
}

func newWsState() *wsState {
	return &wsState{layouts: map[string]*layoutNode{}, contexts: map[string]string{}, writer: fileWriter{what: "workspace"}}
}
