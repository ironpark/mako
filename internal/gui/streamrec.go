package gui

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/config"
)

// A developer option records the bytes of every terminal stream the window
// receives, as the session's program wrote them, to train compression
// dictionaries on later: zstd --train takes the files as its samples. They
// are kept by agent and by the system of the session's node, as
// <agent>/<os>/<when>-<session>-<part>.pty under recordDir, each part up
// to recordPart, and recording stops once they hold recordMax.
const (
	recordMax  = 10 << 30
	recordPart = 8 << 20
)

// recordDir is where the recordings are kept.
func recordDir() string {
	dir, _ := config.Dir()
	return filepath.Join(dir, "dev", "pty-streams")
}

// streamRecorder keeps the window's recordings within recordMax.
type streamRecorder struct {
	on  atomic.Bool // the option, as set
	dir string

	mu      sync.Mutex
	used    int64 // bytes the recordings hold
	scanned bool  // used was read from the disk
}

func newStreamRecorder(dir string) *streamRecorder { return &streamRecorder{dir: dir} }

// reserve takes n bytes of the room left, reporting whether there was.
func (r *streamRecorder) reserve(n int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.scanned {
		r.scanned, r.used = true, dirSize(r.dir)
	}
	if r.used+n > recordMax {
		return false
	}
	r.used += n
	return true
}

// size is how many bytes the recordings hold, read from the disk again.
func (r *streamRecorder) size() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanned, r.used = true, dirSize(r.dir)
	return r.used
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// tap is the recording of one attach of a session, by its agent, as
// "claude" or "claude:opus", and the system its node runs.
func (r *streamRecorder) tap(session, agent, goos string) *streamTap {
	agent, _, _ = strings.Cut(agent, ":")
	return &streamTap{
		r:    r,
		dir:  filepath.Join(r.dir, pathPart(agent, "shell"), pathPart(goos, "unknown")),
		base: time.Now().Format("20060102-150405") + "-" + pathPart(session, "session"),
	}
}

// pathPart is s fit for a file name, or or for none.
func pathPart(s, or string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
	if strings.Trim(s, "._") == "" {
		return or
	}
	return s
}

// streamTap writes what one attach receives, in parts, while the option
// is on; turned off, it closes its part.
type streamTap struct {
	r         *streamRecorder
	dir, base string

	mu      sync.Mutex
	f       *os.File
	w       *bufio.Writer
	part    int
	written int64 // in the part open
	full    bool  // recordMax was reached, or the folder cannot be written
}

func (t *streamTap) write(p []byte) {
	if t == nil || len(p) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.r.on.Load() {
		t.closeLocked()
		return
	}
	if t.full || !t.r.reserve(int64(len(p))) {
		t.full = true
		t.closeLocked()
		return
	}
	if t.f != nil && t.written+int64(len(p)) > recordPart {
		t.closeLocked()
	}
	if t.f == nil {
		if err := os.MkdirAll(t.dir, 0o700); err != nil {
			t.full = true
			return
		}
		t.part++
		f, err := os.OpenFile(filepath.Join(t.dir, fmt.Sprintf("%s-%03d.pty", t.base, t.part)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.full = true
			return
		}
		t.f, t.w, t.written = f, bufio.NewWriterSize(f, 64<<10), 0
	}
	if _, err := t.w.Write(p); err != nil {
		t.full = true
		t.closeLocked()
		return
	}
	t.written += int64(len(p))
}

// close ends the part open.
func (t *streamTap) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
}

func (t *streamTap) closeLocked() {
	if t.f == nil {
		return
	}
	_ = t.w.Flush()
	_ = t.f.Close()
	t.f, t.w = nil, nil
}

// developerSettings are the options for working on mako itself.
func developerSettings(m *model) ggui.Widget {
	dir := m.recorder.dir
	used := ggui.State(int64(0))
	// What the recordings hold is read again each few seconds while the
	// page shows.
	measure := func() {
		ggui.Async(func() (int64, error) { return m.recorder.size(), nil }, func(n int64, _ error) { used.Set(n) })
	}
	ggui.Effect(func() ggui.Cleanup { measure(); return m.every(3*time.Second, measure) })
	usage := ggui.View(used, func(n int64) ggui.Widget {
		return muted(ggui.Text(Tf("%s used of %s", byteSize(n), byteSize(recordMax))).Size(12.5).NoWrap())
	})
	open := ui.Button(T("Open folder"), func() {
		if os.MkdirAll(dir, 0o700) == nil {
			_ = runtime.OpenURL(dir)
		}
	}).Outline()
	return page(T("Developer"), T("Options for working on mako itself."),
		section(T("Terminal streams"),
			card(
				row(T("Record terminal streams"), T("Keep every byte the terminals of this window receive, by agent and the system of the session's node, to train compression dictionaries on. Recording stops at 10 GB."),
					ui.Switch(prefField(m, func(p *prefs) *bool { return &p.RecordStreams }), "").Name(T("Record terminal streams"))),
				row(T("Recordings"), dir, ggui.Row(usage, open).Gap(12).Align(ggui.AlignCenter)),
			),
		),
	)
}
