package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/gitstat"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// Front-ends browse the files of a session's project, and its git changes,
// through its node's daemon. Whoever can reach a daemon can already start a shell on it, so
// reading its files opens nothing new.

// maxRead caps what ReadFile returns of a file.
const maxRead = 1 << 20

func (d *Daemon) handleListDir(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeDir, func(r *protocol.ListDir) *string { return &r.NodeID },
		func(r protocol.ListDir) (protocol.Dir, error) { return listDir(r.Path) })
}

func (d *Daemon) handleReadFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile, func(r *protocol.ReadFile) *string { return &r.NodeID },
		func(r protocol.ReadFile) (protocol.File, error) { return readFile(r.Path, r.Max) })
}

func (d *Daemon) handleGitStatus(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitChanges, func(r *protocol.GitStatus) *string { return &r.NodeID },
		func(r protocol.GitStatus) (protocol.GitChanges, error) {
			if err := absolute(r.Path); err != nil {
				return protocol.GitChanges{}, err
			}
			return gitstat.Status(r.Path)
		})
}

func (d *Daemon) handleGitDiff(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeDiff, func(r *protocol.GitDiff) *string { return &r.NodeID },
		func(r protocol.GitDiff) (protocol.Diff, error) {
			if err := absolute(r.Root); err != nil {
				return protocol.Diff{}, err
			}
			return gitstat.Diff(r.Root, r.Path, r.From)
		})
}

// serve answers a request this node's own work answers, or a peer does for
// another node: the decode, forward and reply every file request shares.
// node is the request's node field, cleared as it goes to the peer.
func serve[Req, Reply any](d *Daemon, ctx context.Context, stream *transport.Stream, env *protocol.Envelope,
	replyType protocol.Type, node func(*Req) *string, work func(Req) (Reply, error)) error {
	var req Req
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	var reply Reply
	var err error
	if id := *node(&req); d.local(id) {
		reply, err = work(req)
	} else {
		*node(&req) = "" // the peer serves it locally
		err = d.forward(ctx, id, env.Type, req, &reply)
	}
	if err != nil {
		return err
	}
	return stream.Send(replyType, env.ID, reply)
}

func absolute(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("the path must be absolute")
	}
	return nil
}

func listDir(path string) (protocol.Dir, error) {
	if err := absolute(path); err != nil {
		return protocol.Dir{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return protocol.Dir{}, err
	}
	out := protocol.Dir{Path: path, Entries: make([]protocol.DirEntry, 0, len(entries))}
	for _, e := range entries {
		de := protocol.DirEntry{Name: e.Name(), Dir: e.IsDir(), Link: e.Type()&os.ModeSymlink != 0}
		if info, err := e.Info(); err == nil {
			de.Size, de.ModTime = info.Size(), info.ModTime()
		}
		if de.Link {
			// A link is shown as what it points to, so one to a
			// directory opens like one.
			if target, err := os.Stat(filepath.Join(path, e.Name())); err == nil {
				de.Dir = target.IsDir()
			}
		}
		out.Entries = append(out.Entries, de)
	}
	slices.SortFunc(out.Entries, func(a, b protocol.DirEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

func readFile(path string, limit int) (protocol.File, error) {
	if err := absolute(path); err != nil {
		return protocol.File{}, err
	}
	if limit <= 0 || limit > maxRead {
		limit = maxRead
	}
	f, err := os.Open(path)
	if err != nil {
		return protocol.File{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return protocol.File{}, err
	}
	if info.IsDir() {
		return protocol.File{}, errors.New("a directory is not a file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return protocol.File{}, err
	}
	out := protocol.File{Path: path, Size: info.Size(), Truncated: info.Size() > int64(len(data))}
	if isBinary(data, out.Truncated) {
		out.Binary = true
		return out, nil
	}
	out.Data = data
	return out, nil
}

// isBinary guesses as git does: a NUL in the first 8000 bytes, or text that
// is not UTF-8. A cut may split the last rune, which does not count.
func isBinary(data []byte, cut bool) bool {
	if gitstat.Binary(data) {
		return true
	}
	for i := 0; cut && i < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); i++ {
		data = data[:len(data)-1]
	}
	return !utf8.Valid(data)
}

// forward makes a request of a peer for the node it is addressed to.
func (d *Daemon) forward(ctx context.Context, nodeID string, typ protocol.Type, req, reply any) error {
	node, err := d.node(nodeID)
	if err != nil {
		return err
	}
	stream, err := d.peerStream(ctx, node)
	if err != nil {
		return err
	}
	defer stream.Close()
	return stream.Request(typ, req, reply)
}
