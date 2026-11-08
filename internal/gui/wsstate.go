package gui

import (
	"log"
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
	// writing is set while a goroutine writes the workspace down, and
	// next is the file it writes after, the latest asked for; see write.
	writeMu sync.Mutex
	writing bool
	next    *wsWrite
}

// wsWrite is a workspace file to write.
type wsWrite struct {
	path string
	body []byte
}

// write writes body to path on a goroutine. One goroutine writes at a
// time, the writes in the order asked for, and of those asked for while
// one is under way only the latest: an older one landing last would
// leave the file behind the window.
func (ws *wsState) write(path string, body []byte) {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	ws.next = &wsWrite{path, body}
	if ws.writing {
		return
	}
	ws.writing = true
	ws.writes.Add(1)
	go func() {
		defer ws.writes.Done()
		for {
			ws.writeMu.Lock()
			w := ws.next
			ws.next = nil
			if w == nil {
				ws.writing = false
				ws.writeMu.Unlock()
				return
			}
			ws.writeMu.Unlock()
			if err := writeWindowFile(w.path, w.body); err != nil {
				log.Printf("mako: save workspace: %v", err)
			}
		}
	}()
}

func newWsState() *wsState {
	return &wsState{layouts: map[string]*layoutNode{}, contexts: map[string]string{}}
}
