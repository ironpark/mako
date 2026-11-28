// Package homefs reads and writes the files of agents' homes for the
// daemon. In the homes of sandboxed sessions, which the agents there write
// as they like, a path is opened through an os.Root at the agent's home:
// a symbolic link the agent made, of a folder or of the file itself, can
// take neither a read nor a write of the daemon outside it. Elsewhere a
// path is opened as it is.
package homefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ironpark/mako/pkg/fsutil"
)

// SandboxBase is the folder of the agent homes sandboxed sessions use:
// $MAKO_HOME/sandbox when MAKO_HOME is set, otherwise ~/.mako/sandbox.
func SandboxBase() (string, error) {
	if custom := os.Getenv("MAKO_HOME"); custom != "" {
		return filepath.Join(custom, "sandbox"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mako", "sandbox"), nil
}

// caseFolds reports whether paths name a file in any case, as on macOS.
var caseFolds = runtime.GOOS == "darwin"

// split is path's names, after the root.
func split(path string) []string {
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return nil
	}
	return strings.Split(path, "/")
}

// relIn is path relative to dir when it is in dir, in slash form.
func relIn(dir, path string) (string, bool) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(dir) {
		return "", false
	}
	d, p := split(dir), split(path)
	if filepath.VolumeName(dir) != filepath.VolumeName(path) || len(p) < len(d) {
		return "", false
	}
	for i := range d {
		if d[i] != p[i] && !(caseFolds && strings.EqualFold(d[i], p[i])) {
			return "", false
		}
	}
	if len(p) == len(d) {
		return ".", true
	}
	return strings.Join(p[len(d):], "/"), true
}

// Within reports whether path is dir or in it, as the file system takes
// paths: in any case on macOS, and through dir's symbolic links.
func Within(path, dir string) bool {
	for _, d := range forms(dir) {
		if _, ok := relIn(d, path); ok {
			return true
		}
	}
	return false
}

func forms(path string) []string {
	clean := filepath.Clean(path)
	out := []string{clean}
	if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
		out = append(out, real)
	}
	return out
}

// ErrLink is the error for a sandbox home that is a symbolic link.
var ErrLink = errors.New("a sandbox home is a symbolic link")

// home is the sandbox home path is in, opened, and path in it; nil for a
// path in none. create makes the home when it is not there.
func home(path string, create bool) (*os.Root, string, error) {
	base, err := SandboxBase()
	if err != nil || base == "" {
		return nil, "", nil
	}
	for _, b := range forms(base) {
		rel, ok := relIn(b, path)
		if !ok {
			continue
		}
		if rel == "." {
			// The folder of the homes is the daemon's, which no agent
			// writes.
			return nil, "", nil
		}
		name, rest, _ := strings.Cut(rel, "/")
		if create {
			if err := os.MkdirAll(b, 0o700); err != nil {
				return nil, "", err
			}
		}
		parent, err := os.OpenRoot(b)
		if err != nil {
			return nil, "", err
		}
		defer parent.Close()
		info, err := parent.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist) && create:
			if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, "", err
			}
		case err != nil:
			return nil, "", err
		case info.Mode()&fs.ModeSymlink != 0 || !info.IsDir():
			return nil, "", fmt.Errorf("%s: %w", filepath.Join(b, name), ErrLink)
		}
		r, err := parent.OpenRoot(name)
		if err != nil {
			return nil, "", err
		}
		if rest == "" {
			rest = "."
		}
		return r, filepath.FromSlash(rest), nil
	}
	return nil, "", nil
}

// ReadFile is os.ReadFile.
func ReadFile(path string) ([]byte, error) {
	r, rel, err := home(path, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return os.ReadFile(path)
	}
	defer r.Close()
	return r.ReadFile(rel)
}

// Open is os.Open.
func Open(path string) (*os.File, error) {
	r, rel, err := home(path, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return os.Open(path)
	}
	defer r.Close()
	return r.Open(rel)
}

// MkdirAll is os.MkdirAll.
func MkdirAll(path string, perm fs.FileMode) error {
	r, rel, err := home(path, true)
	if err != nil {
		return err
	}
	if r == nil {
		return os.MkdirAll(path, perm)
	}
	defer r.Close()
	if rel == "." {
		return nil
	}
	return r.MkdirAll(rel, perm)
}

// Write is fsutil.Write: it replaces the file at path whole, through a
// file of its own beside it renamed over path.
func Write(path string, data []byte) error {
	r, rel, err := home(path, false)
	if err != nil {
		return err
	}
	if r == nil {
		return fsutil.Write(path, data)
	}
	defer r.Close()
	if rel == "." {
		return fmt.Errorf("%s: is a sandbox home", path)
	}
	dir, name := filepath.Split(rel)
	var tmp string
	var f *os.File
	for range 100 {
		var b [6]byte
		_, _ = rand.Read(b[:])
		tmp = filepath.Join(dir, "."+name+"-"+hex.EncodeToString(b[:]))
		f, err = r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer r.Remove(tmp) // gone once renamed; left behind on an error
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return r.Rename(tmp, rel)
}

// Remove is os.Remove.
func Remove(path string) error {
	r, rel, err := home(path, false)
	if err != nil {
		return err
	}
	if r == nil {
		return os.Remove(path)
	}
	defer r.Close()
	if rel == "." {
		return fmt.Errorf("%s: is a sandbox home", path)
	}
	return r.Remove(rel)
}

// Lstat is os.Lstat.
func Lstat(path string) (fs.FileInfo, error) {
	r, rel, err := home(path, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return os.Lstat(path)
	}
	defer r.Close()
	return r.Lstat(rel)
}

// Readlink is os.Readlink.
func Readlink(path string) (string, error) {
	r, rel, err := home(path, false)
	if err != nil {
		return "", err
	}
	if r == nil {
		return os.Readlink(path)
	}
	defer r.Close()
	return r.Readlink(rel)
}

// Symlink is os.Symlink: newname, in a sandbox home, may point anywhere,
// as it is the agent's to follow, not the daemon's.
func Symlink(oldname, newname string) error {
	r, rel, err := home(newname, false)
	if err != nil {
		return err
	}
	if r == nil {
		return os.Symlink(oldname, newname)
	}
	defer r.Close()
	return r.Symlink(oldname, rel)
}

// ReadDir is os.ReadDir.
func ReadDir(path string) ([]fs.DirEntry, error) {
	r, rel, err := home(path, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return os.ReadDir(path)
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}
