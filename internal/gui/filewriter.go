package gui

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/ironpark/mako/internal/atomicfile"
)

// fileWriter writes a file of the window's off the UI goroutine, as a
// synced write can take a while. One goroutine writes at a time, the
// writes in the order asked for, and of those asked for while one is
// under way only the latest: an older one landing last would leave the
// file behind the window.
type fileWriter struct {
	what string // for the log
	mu   sync.Mutex
	// writing is set while a goroutine writes, and next is the file it
	// writes after, the latest asked for.
	writing bool
	next    *pendingWrite
	wg      sync.WaitGroup // writes still reaching disk
}

// pendingWrite is a file to write.
type pendingWrite struct {
	path string
	body []byte
}

// write writes body to path on a goroutine.
func (w *fileWriter) write(path string, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next = &pendingWrite{path, body}
	if w.writing {
		return
	}
	w.writing = true
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			w.mu.Lock()
			p := w.next
			w.next = nil
			if p == nil {
				w.writing = false
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			if err := writeWindowFile(p.path, p.body); err != nil {
				log.Printf("mako: save %s: %v", w.what, err)
			}
		}
	}()
}

// wait returns once every write asked for has reached disk.
func (w *fileWriter) wait() { w.wg.Wait() }

// writeWindowFile writes a file of the window's at path, making its folder
// if need be, whole: a crash, or the window read meanwhile, finds the old
// file or the new, never half of one.
func writeWindowFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, body)
}
