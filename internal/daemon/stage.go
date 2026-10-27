package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// A file dropped on a window goes to the node of the session it was
// dropped on, which cannot read the window's disk: the window sends it in
// pieces, and the node keeps it in a folder of its own under the temporary
// directory, whose path the session is given. A day on, the next file
// staged clears it away.

// maxStaged caps a staged file, which a session is handed to read.
const maxStaged = 512 << 20

// stagedFor is how long a staged file is kept.
const stagedFor = 24 * time.Hour

// stageRoot is where staged files are kept on this node.
func stageRoot() string { return filepath.Join(os.TempDir(), "sanbo-drops") }

func (d *Daemon) handleStageFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeStaged, func(r *protocol.StageFile) (*protocol.Staged, error) {
		return stageFile(stageRoot(), r)
	})
}

// stageFile writes one piece of a staged file under root: the first makes
// the file, named as the window's, in a folder of its own; the rest go
// into the file the first made, at their offsets.
func stageFile(root string, r *protocol.StageFile) (*protocol.Staged, error) {
	path := r.GetPath()
	if path == "" {
		name := filepath.Base(filepath.Clean("/" + r.GetName()))
		if name == "/" || name == "." || strings.ContainsRune(name, 0) {
			return nil, errors.New("a staged file needs a name")
		}
		clearStaged(root, time.Now().Add(-stagedFor))
		var id [8]byte
		_, _ = rand.Read(id[:])
		dir := filepath.Join(root, time.Now().Format("20060102-150405")+"-"+hex.EncodeToString(id[:]))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	} else if rel, err := filepath.Rel(root, filepath.Clean(path)); err != nil || strings.HasPrefix(rel, "..") || strings.Count(rel, string(filepath.Separator)) != 1 {
		return nil, fmt.Errorf("%s is not a staged file", path)
	}
	if r.GetOffset() < 0 || r.GetOffset()+int64(len(r.GetData())) > maxStaged {
		return nil, fmt.Errorf("a staged file is at most %d MB", maxStaged>>20)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteAt(r.GetData(), r.GetOffset()); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return &protocol.Staged{Path: path, Size: info.Size()}, nil
}

// clearStaged removes the staged folders under root older than before.
func clearStaged(root string, before time.Time) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() && info.ModTime().Before(before) {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
