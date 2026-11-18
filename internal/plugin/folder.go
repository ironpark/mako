package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// A plugin in development is a folder of the daemon's plugins folder,
// named for its ID: its manifest and its JavaScript. The folder is read
// through an os.Root, which keeps every path inside it, and a symbolic
// link anywhere in it, the folder itself included, is refused rather
// than followed. Only .js files are read as modules, those whose paths
// an import can name, within bounds of number and size; folders starting
// with a dot, and node_modules, are passed over.

const (
	maxModules     = 64
	maxSourceBytes = 2 << 20
	maxFolderDepth = 8
)

// version is a plugin's code as read from its folder, checked.
type version struct {
	man   *Manifest
	src   map[string]string // module name, its path in the folder, to its source
	order []string          // the module names, sorted
	print string            // the folder's fingerprint when read
	sum   string            // its manifest's and modules' content, hashed
}

// skipDir says a folder of a plugin's is not read.
func skipDir(name string) bool { return strings.HasPrefix(name, ".") || name == "node_modules" }

// openPluginRoot opens a plugin's folder, refusing one that is a symbolic
// link or not a folder.
func openPluginRoot(dir, id string) (*os.Root, error) {
	p := filepath.Join(dir, id)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("the plugin's folder %s is a symbolic link, which is refused", id)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", id)
	}
	return os.OpenRoot(p)
}

// walkPlugin calls fn with each file of a plugin's folder read, by its
// path there, refusing a symbolic link, and a folder too deep.
func walkPlugin(root *os.Root, fn func(name string, fi fs.FileInfo) error) error {
	return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link, which is refused", name)
		}
		if d.IsDir() {
			if name != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			if strings.Count(name, "/") >= maxFolderDepth {
				return fmt.Errorf("%s is deeper than %d folders", name, maxFolderDepth)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return fn(name, fi)
	})
}

// fingerprint is a plugin's folder as its files' names, sizes, times and
// modes stand, which changes when any of them does; an error stands for
// itself, so a folder that cannot be read is read again once it changes.
func fingerprint(dir, id string) string {
	h := sha256.New()
	root, err := openPluginRoot(dir, id)
	if err != nil {
		fmt.Fprintf(h, "error %v", err)
		return hex.EncodeToString(h.Sum(nil))
	}
	defer root.Close()
	err = walkPlugin(root, func(name string, fi fs.FileInfo) error {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%v\n", name, fi.Size(), fi.ModTime().UnixNano(), fi.Mode())
		return nil
	})
	if err != nil {
		fmt.Fprintf(h, "error %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readVersion reads and checks a plugin's folder: its manifest, of the
// folder's ID, and its modules, the entry among them.
func readVersion(dir, id string) (*version, error) {
	print := fingerprint(dir, id)
	root, err := openPluginRoot(dir, id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readBounded(root, ManifestName, maxManifest)
	if err != nil {
		return nil, err
	}
	man, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	if man.ID != id {
		return nil, fmt.Errorf("the manifest's id %q is not its folder's name %q", man.ID, id)
	}
	v := &version{man: man, src: map[string]string{}, print: print}
	total := 0
	err = walkPlugin(root, func(name string, fi fs.FileInfo) error {
		if path.Ext(name) != ".js" || !entryPath.MatchString(name) {
			return nil
		}
		if len(v.src) >= maxModules {
			return fmt.Errorf("more than %d modules", maxModules)
		}
		total += int(fi.Size())
		if total > maxSourceBytes {
			return fmt.Errorf("the modules are over %d MiB", maxSourceBytes>>20)
		}
		b, err := readBounded(root, name, maxSourceBytes)
		if err != nil {
			return err
		}
		v.src[name] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := v.src[man.Entry]; !ok {
		return nil, fmt.Errorf("the entry %s is not a module of the folder", man.Entry)
	}
	for name := range v.src {
		v.order = append(v.order, name)
	}
	slices.Sort(v.order)
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s", len(data), data)
	for _, name := range v.order {
		fmt.Fprintf(h, "%s\x00%d\x00%s", name, len(v.src[name]), v.src[name])
	}
	v.sum = hex.EncodeToString(h.Sum(nil))
	return v, nil
}

// readBounded reads a file of root, refusing a symbolic link, and one
// over max bytes.
func readBounded(root *os.Root, name string, max int) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s is missing", name)
		}
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%s is over %d KiB", name, max>>10)
	}
	return b, nil
}
