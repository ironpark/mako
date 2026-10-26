package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/gitstat"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// Front-ends browse the files of a session's project, and its git changes,
// through its node's daemon. Whoever can reach a daemon can already start a shell on it, so
// reading its files opens nothing new.

// maxRead caps what ReadFile returns of a file: what an editor opens
// whole, its base64 well inside a frame.
const maxRead = 4 << 20

// maxImage caps the image ReadFile returns whole: a frame carries the bytes
// in base64, a third more.
const maxImage = 5 << 20

func (d *Daemon) handleListDir(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeDir, func(r *protocol.ListDir) *string { return &r.NodeID },
		func(r protocol.ListDir) (protocol.Dir, error) { return listDir(r.Path) })
}

func (d *Daemon) handleReadFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile, func(r *protocol.ReadFile) *string { return &r.NodeID },
		func(r protocol.ReadFile) (protocol.File, error) {
			if r.Image {
				return readImage(r.Path)
			}
			return readFile(r.Path, r.Max)
		})
}

func (d *Daemon) handleWriteFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile, func(r *protocol.WriteFile) *string { return &r.NodeID },
		func(r protocol.WriteFile) (protocol.File, error) { return writeFile(r.Path, r.Data, r.ModTime) })
}

func (d *Daemon) handleGitStatus(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitChanges, func(r *protocol.GitStatus) *string { return &r.NodeID },
		func(r protocol.GitStatus) (protocol.GitChanges, error) {
			if err := absolute(r.Path); err != nil {
				return protocol.GitChanges{}, err
			}
			if r.Base != "" {
				return inRepo(ctx, r.Path, func(ctx context.Context) (protocol.GitChanges, error) {
					return gitstat.BaseStatus(ctx, r.Path, r.Base)
				})
			}
			return gitstat.Status(r.Path)
		})
}

func (d *Daemon) handleGitDiff(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeDiff, func(r *protocol.GitDiff) *string { return &r.NodeID },
		func(r protocol.GitDiff) (protocol.Diff, error) {
			return inRepo(ctx, r.Root, func(ctx context.Context) (protocol.Diff, error) {
				if r.Commit != "" {
					return gitstat.CommitDiff(ctx, r.Root, r.Commit, r.Path, r.From)
				}
				if r.Base != "" {
					return gitstat.BaseDiff(ctx, r.Root, r.Base, r.Path, r.From)
				}
				return gitstat.Diff(r.Root, r.Path, r.From)
			})
		})
}

func (d *Daemon) handleFileOp(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile, func(r *protocol.FileOp) *string { return &r.NodeID }, fileOp)
}

// gitTimeout bounds a git operation, a push or a pull over a slow link
// among them.
const gitTimeout = 2 * time.Minute

// inRepo runs work on the repository at root, an absolute path, within
// gitTimeout.
func inRepo[T any](ctx context.Context, root string, work func(context.Context) (T, error)) (T, error) {
	if err := absolute(root); err != nil {
		var zero T
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	return work(ctx)
}

func (d *Daemon) handleGitLog(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitHistory, func(r *protocol.GitLog) *string { return &r.NodeID },
		func(r protocol.GitLog) (protocol.GitHistory, error) {
			return inRepo(ctx, r.Root, func(ctx context.Context) (protocol.GitHistory, error) {
				return gitstat.Log(ctx, r.Root, r.Max, r.Known)
			})
		})
}

func (d *Daemon) handleGitShow(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitCommitInfo, func(r *protocol.GitShow) *string { return &r.NodeID },
		func(r protocol.GitShow) (protocol.GitCommitInfo, error) {
			return inRepo(ctx, r.Root, func(ctx context.Context) (protocol.GitCommitInfo, error) {
				return gitstat.Show(ctx, r.Root, r.Hash)
			})
		})
}

func (d *Daemon) handleGitRun(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitResult, func(r *protocol.GitRun) *string { return &r.NodeID },
		func(r protocol.GitRun) (protocol.GitResult, error) {
			return inRepo(ctx, r.Root, func(ctx context.Context) (protocol.GitResult, error) {
				out, err := gitstat.Run(ctx, r)
				return protocol.GitResult{Output: out}, err
			})
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
	markIgnored(path, out.Entries)
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

// markIgnored marks the entries of a directory that git leaves out of its
// repository, as .gitignore and the like say; outside a repository, or
// without git, none are.
func markIgnored(dir string, entries []protocol.DirEntry) {
	if len(entries) == 0 {
		return
	}
	var in bytes.Buffer
	for _, e := range entries {
		in.WriteString(e.Name)
		if e.Dir {
			in.WriteByte('/') // so a pattern for directories matches
		}
		in.WriteByte(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "check-ignore", "-z", "--stdin")
	cmd.Stdin = &in
	out, _ := cmd.Output() // it exits 1 when none is ignored, 128 outside a repository
	ignored := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		ignored[strings.TrimSuffix(name, "/")] = true
	}
	for i := range entries {
		entries[i].Ignored = ignored[entries[i].Name]
	}
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
	out := protocol.File{Path: path, Size: info.Size(), ModTime: info.ModTime(), Truncated: info.Size() > int64(len(data))}
	if isBinary(data, out.Truncated) {
		out.Binary = true
		return out, nil
	}
	out.Data = data
	return out, nil
}

// writeFile replaces a file's contents, or creates it. The data goes to a
// file beside it first, renamed over it once whole, so a failed write
// leaves the old contents; the file keeps its mode. A link is written
// through to its target. Unless was is zero, a file changed since then is
// left alone.
func writeFile(path string, data []byte, was time.Time) (protocol.File, error) {
	if err := absolute(path); err != nil {
		return protocol.File{}, err
	}
	asked := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	switch info, err := os.Stat(path); {
	case err == nil && info.IsDir():
		return protocol.File{}, errors.New("a directory is not a file")
	case err == nil:
		mode = info.Mode().Perm()
		if !was.IsZero() && !info.ModTime().Equal(was) {
			return protocol.File{}, &protocol.CodedError{Code: protocol.CodeChanged, Message: "the file changed on disk since it was read"}
		}
	case !errors.Is(err, os.ErrNotExist):
		return protocol.File{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".sanbo-*")
	if err != nil {
		return protocol.File{}, err
	}
	defer os.Remove(tmp.Name()) // gone already once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return protocol.File{}, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return protocol.File{}, err
	}
	if err := tmp.Close(); err != nil {
		return protocol.File{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return protocol.File{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return protocol.File{}, err
	}
	return protocol.File{Path: asked, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// readImage reads an image file whole, or only its size when it is larger
// than maxImage.
func readImage(path string) (protocol.File, error) {
	if err := absolute(path); err != nil {
		return protocol.File{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return protocol.File{}, err
	}
	if info.IsDir() {
		return protocol.File{}, errors.New("a directory is not a file")
	}
	out := protocol.File{Path: path, Size: info.Size(), Binary: true}
	if info.Size() > maxImage {
		out.Truncated = true
		return out, nil
	}
	if out.Data, err = os.ReadFile(path); err != nil {
		return protocol.File{}, err
	}
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

// fileOp makes, renames or deletes a file or folder. None of them replaces
// what is already there.
func fileOp(r protocol.FileOp) (protocol.File, error) {
	if err := absolute(r.Path); err != nil {
		return protocol.File{}, err
	}
	p := filepath.Clean(r.Path)
	if filepath.Dir(p) == p {
		return protocol.File{}, errors.New("not the root of the file system")
	}
	switch r.Op {
	case protocol.FileCreate:
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return protocol.File{}, err
		}
		return protocol.File{Path: p}, f.Close()
	case protocol.FileMkdir:
		return protocol.File{Path: p}, os.Mkdir(p, 0o755)
	case protocol.FileRename:
		if err := absolute(r.To); err != nil {
			return protocol.File{}, err
		}
		to := filepath.Clean(r.To)
		if _, err := os.Lstat(to); err == nil {
			return protocol.File{}, fmt.Errorf("%s already exists", filepath.Base(to))
		}
		return protocol.File{Path: to}, os.Rename(p, to)
	case protocol.FileDelete:
		if _, err := os.Lstat(p); err != nil {
			return protocol.File{}, err
		}
		return protocol.File{Path: p}, os.RemoveAll(p)
	}
	return protocol.File{}, fmt.Errorf("unknown file operation %q", r.Op)
}
