package gui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// Files and folders go to a node and come back from one in pieces, through
// the daemons: dropped on the Files tab or a node's file browser, or
// downloaded from one. Nothing is replaced: a name taken where a file goes
// is given a number, as a desktop's file manager does.

// errOlder is a node whose daemon cannot move a file in pieces, or open a
// tunnel.
var errOlder = &protocol.CodedError{Code: protocol.CodeUnsupported, Message: "this node's daemon is too old"}

// canTransfer says why node cannot move files in pieces or open tunnels,
// when its daemon is from before them: nil when it can, or is not known.
func (m *model) canTransfer(node string) error {
	n := ggui.Peek(m.Conn).Self
	if !m.onThisMachine(node) {
		var ok bool
		if n, ok = nodeIn(ggui.Peek(m.Nodes), node); !ok {
			return nil
		}
	}
	if n.GetRevision() < protocol.RevisionTransfers {
		return errOlder
	}
	return nil
}

// transfer is a copy under way, which a goroutine moves and the window
// shows: how much has moved, of how much when that is known.
type transfer struct {
	done, total atomic.Int64
	buf         []byte // the piece an upload reads into, one for all its files
}

// freeName is name, or name with a number before its extension, whichever
// taken does not hold.
func freeName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // a dotfile
		stem, ext = name, ""
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s %d%s", stem, i, ext); !taken(n) {
			return n
		}
	}
}

// localSize is how many bytes the files at paths hold, folders and all.
func localSize(paths []string) int64 {
	var n int64
	for _, p := range paths {
		_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, err := d.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
	}
	return n
}

// upload copies the files and folders at paths on this machine into the
// folder dir on node.
func upload(ctx context.Context, c *client.Client, node, dir string, paths []string, t *transfer) error {
	taken, err := remoteNames(ctx, c, node, dir)
	if err != nil {
		return err
	}
	var failed error
	for _, p := range paths {
		if err := uploadOne(ctx, c, node, dir, p, taken, t); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed = errors.Join(failed, fmt.Errorf("%s: %w", filepath.Base(p), err))
		}
	}
	return failed
}

// remoteNames is the names in the folder dir on node.
func remoteNames(ctx context.Context, c *client.Client, node, dir string) (map[string]bool, error) {
	d, err := c.ListDir(ctx, &protocol.ListDir{NodeId: node, Path: dir})
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, e := range d.GetEntries() {
		taken[e.GetName()] = true
	}
	return taken, nil
}

// uploadOne copies the file or folder at p into dir on node, under a name
// not in taken, which it adds to.
func uploadOne(ctx context.Context, c *client.Client, node, dir, p string, taken map[string]bool, t *transfer) error {
	info, err := os.Stat(p) // through a link, to what it names
	if err != nil {
		return err
	}
	name := freeName(filepath.Base(p), func(n string) bool { return taken[n] })
	taken[name] = true
	to := joinPath(dir, name)
	if !info.IsDir() {
		return uploadFile(ctx, c, node, p, to, t)
	}
	if _, err := c.FileOp(ctx, &protocol.FileOp{NodeId: node, Op: protocol.FileMkdir, Path: to}); err != nil {
		return err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	inside := map[string]bool{}
	for _, e := range entries {
		err := uploadOne(ctx, c, node, to, filepath.Join(p, e.Name()), inside, t)
		if errors.Is(err, fs.ErrNotExist) && e.Type()&os.ModeSymlink != 0 {
			continue // a link to nothing
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// uploadFile copies the file at from to the path to on node, which it
// makes, refusing a name taken: the first piece writes it, and the rest
// grow it.
func uploadFile(ctx context.Context, c *client.Client, node, from, to string, t *transfer) error {
	f, err := os.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := c.FileOp(ctx, &protocol.FileOp{NodeId: node, Op: protocol.FileCreate, Path: to}); err != nil {
		return err
	}
	if t.buf == nil {
		t.buf = make([]byte, stageChunk)
	}
	return sendPieces(f, t.buf, func(off int64, p []byte) error {
		if _, err := c.WriteFile(ctx, &protocol.WriteFile{NodeId: node, Path: to, Data: p, Offset: off}); err != nil {
			return err
		}
		t.done.Add(int64(len(p)))
		return nil
	})
}

// download copies the file or folder at from on node into the folder dir
// here, and returns where it went.
func download(ctx context.Context, c *client.Client, node, from string, isDir bool, dir string, t *transfer) (string, error) {
	name := freeName(baseName(from), func(n string) bool {
		_, err := os.Lstat(filepath.Join(dir, n))
		return err == nil
	})
	to := filepath.Join(dir, name)
	if !isDir {
		return to, downloadFile(ctx, c, node, from, to, t)
	}
	d, err := c.ListDir(ctx, &protocol.ListDir{NodeId: node, Path: from})
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(to, 0o755); err != nil {
		return "", err
	}
	for _, e := range d.GetEntries() {
		t.total.Add(e.GetSize())
	}
	for _, e := range d.GetEntries() {
		if _, err := download(ctx, c, node, joinPath(from, e.GetName()), e.GetDir(), to, t); err != nil {
			return to, err
		}
	}
	return to, nil
}

// downloadFile copies the file at from on node to the new file to, a piece
// at a time; a copy that fails is not left half done.
func downloadFile(ctx context.Context, c *client.Client, node, from, to string, t *transfer) (err error) {
	f, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(to)
		}
	}()
	for off := int64(0); ; {
		piece, err := c.ReadFile(ctx, &protocol.ReadFile{NodeId: node, Path: from, Offset: off, Raw: true, Max: stageChunk})
		if err != nil {
			return err
		}
		if _, err := f.Write(piece.GetData()); err != nil {
			return err
		}
		off += int64(len(piece.GetData()))
		t.done.Add(int64(len(piece.GetData())))
		if !piece.GetTruncated() || len(piece.GetData()) == 0 {
			return nil
		}
	}
}
