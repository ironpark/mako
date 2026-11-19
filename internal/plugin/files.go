package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// A plugin with fs:project reads the files of the projects this node
// knows, and with fs:project:write writes them. Which project is not
// guessed from what the window shows or which session is in front: the
// plugin names a project by its ID and one of its folders by its path,
// as ListProjects gives them, the same whichever window, phone or none
// is looking. Each call opens the folder as an os.Root, which follows no
// path or symbolic link out of it, and reads or writes regular files
// alone, so that a FIFO or a device cannot hold the daemon.

const (
	maxFilePiece   = 1 << 20
	maxDirEntries  = 2000
	maxRelPath     = 1024
	fsReadsPerMin  = 120
	fsWritesPerMin = 30
)

// ProjectRoots is a project this node knows: its ID, name and folders.
type ProjectRoots struct {
	ID, Name string
	Roots    []string
}

// relPath checks a path within a root: relative, clean, without "..", a
// backslash, a NUL or a colon (a Windows drive or stream), at most
// maxRelPath bytes; "" and "." are the root itself when dir allows.
func relPath(p string, dir bool) (string, error) {
	if dir && (p == "" || p == ".") {
		return ".", nil
	}
	if p == "" || len(p) > maxRelPath || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00:") ||
		path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return "", fmt.Errorf("%q is not a path relative to the folder: no .., no leading /, no backslash", p)
	}
	return filepath.FromSlash(p), nil
}

// openRoot opens the folder root of the project id, if the node knows
// it so.
func (h *host) openRoot(project, root string) (*os.Root, error) {
	list := h.p.m.cfg.Projects
	if list == nil {
		return nil, errors.New("this node knows no projects")
	}
	for _, pr := range list() {
		if pr.ID == project && slices.Contains(pr.Roots, root) {
			r, err := os.OpenRoot(root)
			if err != nil {
				return nil, fmt.Errorf("the project's folder %s: %w", root, errors.Unwrap(err))
			}
			return r, nil
		}
	}
	return nil, fmt.Errorf("this node knows no project %q with the folder %q: see listProjects", project, root)
}

func (h *host) FilesListProjects(context.Context, *hostapi.ListProjectsRequest) (*hostapi.ListProjectsResponse, error) {
	out := &hostapi.ListProjectsResponse{}
	if list := h.p.m.cfg.Projects; list != nil {
		for _, pr := range list() {
			out.Projects = append(out.Projects, &hostapi.Project{Id: pr.ID, Name: pr.Name, Roots: slices.Clone(pr.Roots)})
		}
	}
	return out, nil
}

func (h *host) FilesReadFile(_ context.Context, req *hostapi.ReadFileRequest) (*hostapi.ReadFileResponse, error) {
	if !h.p.fsRate.take() {
		return nil, fmt.Errorf("the plugin read files more than %d times a minute", fsReadsPerMin)
	}
	rel, err := relPath(req.GetPath(), false)
	if err != nil {
		return nil, err
	}
	off := req.GetOffset()
	if off < 0 || off != math.Trunc(off) || off > 1<<53 {
		return nil, errors.New("offset is a whole number of bytes, 0 or more")
	}
	n := int(req.GetLength())
	if n == 0 || n > maxFilePiece {
		n = maxFilePiece
	}
	r, err := h.openRoot(req.GetProject(), req.GetRoot())
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, pathError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", req.GetPath())
	}
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, int64(off))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:got]
	out := &hostapi.ReadFileResponse{Data: buf, Size: float64(info.Size()), Eof: int64(off)+int64(got) >= info.Size()}
	if utf8.Valid(buf) {
		out.Text = string(buf)
	} else {
		out.Binary = true
	}
	return out, nil
}

func (h *host) FilesListDir(_ context.Context, req *hostapi.ListDirRequest) (*hostapi.ListDirResponse, error) {
	if !h.p.fsRate.take() {
		return nil, fmt.Errorf("the plugin read files more than %d times a minute", fsReadsPerMin)
	}
	rel, err := relPath(req.GetPath(), true)
	if err != nil {
		return nil, err
	}
	r, err := h.openRoot(req.GetProject(), req.GetRoot())
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := r.Open(rel)
	if err != nil {
		return nil, pathError(err)
	}
	defer d.Close()
	if info, err := d.Stat(); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", req.GetPath())
	}
	entries, err := d.ReadDir(maxDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	out := &hostapi.ListDirResponse{}
	if len(entries) > maxDirEntries {
		entries, out.Truncated = entries[:maxDirEntries], true
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, e := range entries {
		de := &hostapi.DirEntry{Name: e.Name(), Dir: e.IsDir(), Link: e.Type()&fs.ModeSymlink != 0}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			de.Size = float64(info.Size())
		}
		out.Entries = append(out.Entries, de)
	}
	return out, nil
}

func (h *host) FilesWriteFile(_ context.Context, req *hostapi.WriteFileRequest) (*hostapi.WriteFileResponse, error) {
	if !h.p.fsWriteRate.take() {
		return nil, fmt.Errorf("the plugin wrote files more than %d times a minute", fsWritesPerMin)
	}
	rel, err := relPath(req.GetPath(), false)
	if err != nil {
		return nil, err
	}
	// A folder named .git is the repository's own: its hooks and config
	// run programs when git runs, as the window's git does.
	for part := range strings.SplitSeq(req.GetPath(), "/") {
		if strings.EqualFold(part, ".git") {
			return nil, errors.New("a plugin does not write under a .git folder")
		}
	}
	data := req.GetData()
	if req.GetText() != "" {
		if len(data) > 0 {
			return nil, errors.New("give data or text, not both")
		}
		data = []byte(req.GetText())
	}
	if len(data) > maxFilePiece {
		return nil, fmt.Errorf("a file written is at most %d MiB", maxFilePiece>>20)
	}
	r, err := h.openRoot(req.GetProject(), req.GetRoot())
	if err != nil {
		return nil, err
	}
	defer r.Close()
	dir := filepath.Dir(rel)
	if req.GetMakeDirs() && dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return nil, pathError(err)
		}
	}
	mode := fs.FileMode(0o644)
	if info, err := r.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file: a plugin writes over files alone, not links or folders", req.GetPath())
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, pathError(err)
	}
	// Written whole beside it, then moved over it: a reader finds the old
	// file or the new, never half of one.
	tmp := filepath.Join(dir, ".mako-plugin-"+nonce()+".tmp")
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return nil, pathError(err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = r.Rename(tmp, rel)
	}
	if err != nil {
		_ = r.Remove(tmp)
		return nil, pathError(err)
	}
	return &hostapi.WriteFileResponse{Size: float64(len(data))}, nil
}

// pathError is an error of a path in a root, without the root's own
// path, which a plugin need not learn more of than ListProjects says.
func pathError(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return fmt.Errorf("%s: %w", filepath.ToSlash(pe.Path), pe.Err)
	}
	if le, ok := errors.AsType[*os.LinkError](err); ok {
		return fmt.Errorf("%s: %w", filepath.ToSlash(le.New), le.Err)
	}
	return err
}
