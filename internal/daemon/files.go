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

	"github.com/ironpark/mako/internal/forge"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"google.golang.org/protobuf/proto"
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
	return serve(d, ctx, stream, env, protocol.TypeDir,
		func(r *protocol.ListDir) (*protocol.Dir, error) { return listDir(mcp.ExpandHome(r.GetPath())) })
}

func (d *Daemon) handleReadFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile,
		func(r *protocol.ReadFile) (*protocol.File, error) {
			path := mcp.ExpandHome(r.GetPath()) // as a terminal may write it
			switch {
			case r.GetImage():
				return readImage(path)
			case r.GetRaw():
				return readPiece(path, r.GetOffset(), int(r.GetMax()))
			}
			return readFile(path, int(r.GetMax()))
		})
}

func (d *Daemon) handleWriteFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile,
		func(r *protocol.WriteFile) (*protocol.File, error) {
			if r.GetOffset() > 0 {
				return writePiece(r.GetPath(), r.GetOffset(), r.GetData())
			}
			return writeFile(r.GetPath(), r.GetData(), protocol.TimeOf(r.GetModTime()))
		})
}

func (d *Daemon) handleGitStatus(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitChanges,
		func(r *protocol.GitStatus) (*protocol.GitChanges, error) {
			if err := absolute(r.GetPath()); err != nil {
				return nil, err
			}
			if r.GetBase() != "" {
				return inRepo(ctx, r.GetPath(), func(ctx context.Context) (*protocol.GitChanges, error) {
					return gitstat.BaseStatus(ctx, r.GetPath(), r.GetBase())
				})
			}
			return gitstat.Status(r.GetPath())
		})
}

func (d *Daemon) handleGitDiff(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeDiff,
		func(r *protocol.GitDiff) (*protocol.Diff, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.Diff, error) {
				if r.GetPull() > 0 {
					return forge.PullDiff(ctx, r.GetRoot(), r.GetPull(), r.GetCommit(), r.GetPath())
				}
				if r.GetCommit() != "" {
					return gitstat.CommitDiff(ctx, r.GetRoot(), r.GetCommit(), r.GetPath(), r.GetFrom())
				}
				if r.GetBase() != "" {
					return gitstat.BaseDiff(ctx, r.GetRoot(), r.GetBase(), r.GetPath(), r.GetFrom())
				}
				return gitstat.Diff(r.GetRoot(), r.GetPath(), r.GetFrom())
			})
		})
}

func (d *Daemon) handleFileOp(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile, fileOp)
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
	return serve(d, ctx, stream, env, protocol.TypeGitHistory,
		func(r *protocol.GitLog) (*protocol.GitHistory, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitHistory, error) {
				return gitstat.Log(ctx, r.GetRoot(), int(r.GetMax()), r.GetKnown())
			})
		})
}

func (d *Daemon) handleGitShow(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitCommitInfo,
		func(r *protocol.GitShow) (*protocol.GitCommitInfo, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitCommitInfo, error) {
				return gitstat.Show(ctx, r.GetRoot(), r.GetHash())
			})
		})
}

func (d *Daemon) handleGitRun(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitResult,
		func(r *protocol.GitRun) (*protocol.GitResult, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitResult, error) {
				run := gitstat.Run
				if r.GetOp() == protocol.GitOpPR {
					// A pull request is opened through GitHub's CLI.
					if r.GetWorktree() == nil {
						return nil, errors.New("no worktree named")
					}
					run = func(ctx context.Context, r *protocol.GitRun) (string, error) {
						return forge.WorktreePR(ctx, r.GetWorktree())
					}
				}
				out, err := run(ctx, r)
				return &protocol.GitResult{Output: out}, err
			})
		})
}

func (d *Daemon) handleGitHubList(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeForgeItems,
		func(r *protocol.ForgeList) (*protocol.ForgeItems, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.ForgeItems, error) { return forge.List(ctx, r) })
		})
}

func (d *Daemon) handleGitHubView(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeForgeDetail,
		func(r *protocol.ForgeView) (*protocol.ForgeDetail, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.ForgeDetail, error) { return forge.View(ctx, r) })
		})
}

func (d *Daemon) handleGitHubRun(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitResult,
		func(r *protocol.ForgeRun) (*protocol.GitResult, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitResult, error) {
				out, err := forge.Run(ctx, r)
				return &protocol.GitResult{Output: out}, err
			})
		})
}

// serve answers a request this node's own work answers, or a peer does for
// another node: the decode, forward and reply every routed request shares.
func serve[Req routed, Reply proto.Message](d *Daemon, ctx context.Context, stream *transport.Stream, env *protocol.Envelope,
	replyType protocol.Type, work func(Req) (Reply, error)) error {
	req, err := protocol.Body[Req](env)
	if err != nil {
		return err
	}
	var reply Reply
	if d.local(req.GetNodeId()) {
		reply, err = work(req)
	} else {
		reply, err = forward[Reply](ctx, d, env.Type(), req)
	}
	if err != nil {
		return err
	}
	return stream.Send(replyType, env.GetId(), reply)
}

func absolute(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("the path must be absolute")
	}
	return nil
}

func listDir(path string) (*protocol.Dir, error) {
	if err := absolute(path); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := &protocol.Dir{Path: path, Entries: make([]*protocol.DirEntry, 0, len(entries))}
	for _, e := range entries {
		de := &protocol.DirEntry{Name: e.Name(), Dir: e.IsDir(), Link: e.Type()&os.ModeSymlink != 0}
		if info, err := e.Info(); err == nil {
			de.Size, de.ModTime = info.Size(), protocol.Time(info.ModTime())
		}
		if de.GetLink() {
			// A link is shown as what it points to, so one to a
			// directory opens like one.
			if target, err := os.Stat(filepath.Join(path, e.Name())); err == nil {
				de.Dir = target.IsDir()
			}
		}
		out.Entries = append(out.Entries, de)
	}
	markIgnored(path, out.GetEntries())
	slices.SortFunc(out.Entries, func(a, b *protocol.DirEntry) int {
		if a.GetDir() != b.GetDir() {
			if a.GetDir() {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName()))
	})
	return out, nil
}

// markIgnored marks the entries of a directory that git leaves out of its
// repository, as .gitignore and the like say; outside a repository, or
// without git, none are.
func markIgnored(dir string, entries []*protocol.DirEntry) {
	if len(entries) == 0 {
		return
	}
	var in bytes.Buffer
	for _, e := range entries {
		in.WriteString(e.GetName())
		if e.GetDir() {
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
		entries[i].Ignored = ignored[entries[i].GetName()]
	}
}

// openToRead opens the file at path, not a directory, for a read of at
// most limit bytes, capped at maxRead.
func openToRead(path string, limit int) (*os.File, os.FileInfo, int, error) {
	if err := absolute(path); err != nil {
		return nil, nil, 0, err
	}
	if limit <= 0 || limit > maxRead {
		limit = maxRead
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	info, err := f.Stat()
	if err == nil && info.IsDir() {
		err = errors.New("a directory is not a file")
	}
	if err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	return f, info, limit, nil
}

func readFile(path string, limit int) (*protocol.File, error) {
	f, info, limit, err := openToRead(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return nil, err
	}
	out := &protocol.File{Path: path, Size: info.Size(), ModTime: protocol.Time(info.ModTime()), Truncated: info.Size() > int64(len(data))}
	if isBinary(data, out.GetTruncated()) {
		out.Binary = true
		return out, nil
	}
	out.Data = data
	return out, nil
}

// readPiece reads a file from offset on, at most limit bytes of it,
// whatever they are: a piece of a download.
func readPiece(path string, offset int64, limit int) (*protocol.File, error) {
	f, info, limit, err := openToRead(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, max(0, min(int64(limit), info.Size()-offset)))
	n, err := f.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &protocol.File{Path: path, Size: info.Size(), ModTime: protocol.Time(info.ModTime()), Data: data[:n],
		Truncated: offset+int64(n) < info.Size()}, nil
}

// writePiece adds a later piece of an upload to the end of the file the
// first one wrote, which must be offset long: a piece sent twice, or one
// gone missing, fails rather than leaving the file wrong.
func writePiece(path string, offset int64, data []byte) (*protocol.File, error) {
	if err := absolute(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != offset {
		return nil, fmt.Errorf("%s is %d bytes long, not %d", filepath.Base(path), info.Size(), offset)
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		return nil, err
	}
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	return &protocol.File{Path: path, Size: info.Size(), ModTime: protocol.Time(info.ModTime())}, f.Close()
}

// writeFile replaces a file's contents, or creates it. The data goes to a
// file beside it first, renamed over it once whole, so a failed write
// leaves the old contents; the file keeps its mode. A link is written
// through to its target. Unless was is zero, a file changed since then is
// left alone.
func writeFile(path string, data []byte, was time.Time) (*protocol.File, error) {
	if err := absolute(path); err != nil {
		return nil, err
	}
	asked := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	switch info, err := os.Stat(path); {
	case err == nil && info.IsDir():
		return nil, errors.New("a directory is not a file")
	case err == nil:
		mode = info.Mode().Perm()
		if !was.IsZero() && !info.ModTime().Equal(was) {
			return nil, &protocol.CodedError{Code: protocol.CodeChanged, Message: "the file changed on disk since it was read"}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".mako-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // gone already once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &protocol.File{Path: asked, Size: info.Size(), ModTime: protocol.Time(info.ModTime())}, nil
}

// readImage reads an image file whole, or only its size when it is larger
// than maxImage.
func readImage(path string) (*protocol.File, error) {
	if err := absolute(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("a directory is not a file")
	}
	out := &protocol.File{Path: path, Size: info.Size(), Binary: true}
	if info.Size() > maxImage {
		out.Truncated = true
		return out, nil
	}
	if out.Data, err = os.ReadFile(path); err != nil {
		return nil, err
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

// routed is a request addressed to a node.
type routed interface {
	proto.Message
	GetNodeId() string
}

// forward makes a request of a peer for the node it is addressed to.
func forward[R proto.Message](ctx context.Context, d *Daemon, typ protocol.Type, req routed) (R, error) {
	reply, stream, err := open[R](ctx, d, typ, req)
	if err == nil {
		stream.Close()
	}
	return reply, err
}

// open sends req to the node it names, as forward does, and keeps the
// stream for what follows the reply on it, as a claim or a device's
// connection. ctx's deadline bounds the wait for the reply.
func open[R proto.Message](ctx context.Context, d *Daemon, typ protocol.Type, req routed) (R, *transport.Stream, error) {
	var zero R
	node, err := d.node(req.GetNodeId())
	if err != nil {
		return zero, nil, err
	}
	stream, err := d.peerStream(ctx, node)
	if err != nil {
		return zero, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetReadDeadline(deadline)
	}
	reply, err := transport.Call[R](stream, typ, req)
	if err != nil {
		stream.Close()
		return zero, nil, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	return reply, stream, nil
}

// fileOp makes, renames or deletes a file or folder. None of them replaces
// what is already there.
func fileOp(r *protocol.FileOp) (*protocol.File, error) {
	if err := absolute(r.GetPath()); err != nil {
		return nil, err
	}
	p := filepath.Clean(r.GetPath())
	if filepath.Dir(p) == p {
		return nil, errors.New("not the root of the file system")
	}
	switch r.GetOp() {
	case protocol.FileCreate:
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, err
		}
		return &protocol.File{Path: p}, f.Close()
	case protocol.FileMkdir:
		return &protocol.File{Path: p}, os.Mkdir(p, 0o755)
	case protocol.FileRename:
		if err := absolute(r.GetTo()); err != nil {
			return nil, err
		}
		to := filepath.Clean(r.GetTo())
		if _, err := os.Lstat(to); err == nil {
			return nil, fmt.Errorf("%s already exists", filepath.Base(to))
		}
		return &protocol.File{Path: to}, os.Rename(p, to)
	case protocol.FileDelete:
		if _, err := os.Lstat(p); err != nil {
			return nil, err
		}
		return &protocol.File{Path: p}, os.RemoveAll(p)
	}
	return nil, fmt.Errorf("unknown file operation %q", r.GetOp())
}
