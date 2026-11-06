package gui

import (
	"sync"
	"time"
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
	// contextsMoved says the window showed another owner's or project's
	// tabs since the workspace was written down.
	contextsMoved bool
	// The edit the workspace is, the node that made it, what was last
	// written out, and the pause after an edit before it is written down.
	version int64
	node    string
	last    []byte
	timer   *time.Timer
	writes  sync.WaitGroup // workspace writes still reaching disk
}

func newWsState() *wsState {
	return &wsState{layouts: map[string]*layoutNode{}, contexts: map[string]string{}}
}
