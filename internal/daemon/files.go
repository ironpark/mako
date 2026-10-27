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

	"github.com/ironpark/mako/internal/github"
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
		func(r *protocol.ListDir) (*protocol.Dir, error) { return listDir(r.GetPath()) })
}

func (d *Daemon) handleReadFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile,
		func(r *protocol.ReadFile) (*protocol.File, error) {
			path := mcp.ExpandHome(r.GetPath()) // as a terminal may write it
			if r.GetImage() {
				return readImage(path)
			}
			return readFile(path, int(r.GetMax()))
		})
}

func (d *Daemon) handleWriteFile(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeFile,
		func(r *protocol.WriteFile) (*protocol.File, error) {
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
					return github.PullDiff(ctx, r.GetRoot(), r.GetPull(), r.GetCommit(), r.GetPath())
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
						return github.WorktreePR(ctx, r.GetWorktree())
					}
				}
				out, err := run(ctx, r)
				return &protocol.GitResult{Output: out}, err
			})
		})
}

func (d *Daemon) handleGitHubList(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitHubItems,
		func(r *protocol.GitHubList) (*protocol.GitHubItems, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitHubItems, error) { return github.List(ctx, r) })
		})
}

func (d *Daemon) handleGitHubView(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitHubDetail,
		func(r *protocol.GitHubView) (*protocol.GitHubDetail, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitHubDetail, error) { return github.View(ctx, r) })
		})
}

func (d *Daemon) handleGitHubRun(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeGitResult,
		func(r *protocol.GitHubRun) (*protocol.GitResult, error) {
			return inRepo(ctx, r.GetRoot(), func(ctx context.Context) (*protocol.GitResult, error) {
				out, err := github.Run(ctx, r)
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

func readFile(path string, limit int) (*protocol.File, error) {
	if err := absolute(path); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRead {
		limit = maxRead
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("a directory is not a file")
	}
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
	var zero R
	node, err := d.node(req.GetNodeId())
	if err != nil {
		return zero, err
	}
	stream, err := d.peerStream(ctx, node)
	if err != nil {
		return zero, err
	}
	defer stream.Close()
	return transport.Call[R](stream, typ, forPeer(req))
}

// forPeer is a request as it goes on to its node's daemon: a copy, its
// node cleared, so the peer serves it locally.
func forPeer(req routed) proto.Message {
	c := proto.Clone(req).ProtoReflect()
	c.Clear(c.Descriptor().Fields().ByName("node_id"))
	return c.Interface()
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
