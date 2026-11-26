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
	"regexp"
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
	maxWasmBytes   = 8 << 20
	maxFolderDepth = 8
)

// version is a plugin's code as read from its folder, checked.
type version struct {
	man   *Manifest
	src   map[string]string // module name, its path in the folder, to its source (.js plugins)
	order []string          // the module names, sorted (.js plugins)
	wasm  []byte            // WebAssembly binary when entry ends in .wasm
	print string            // the folder's fingerprint when read
	sum   string            // its manifest's and modules'/wasm's content, hashed
}

func (v *version) isWasm() bool {
	return v != nil && v.man != nil && path.Ext(v.man.Entry) == ".wasm"
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

// rootFS wraps an os.Root's fs.FS with Lstat so readBoundedFS refuses symbolic
// links even when they point inside the folder.
type rootFS struct {
	fs.FS
	root *os.Root
}

func (r rootFS) Lstat(name string) (fs.FileInfo, error) { return r.root.Lstat(name) }

// walkPlugin calls fn with each file of a plugin's folder read, by its
// path there, refusing a symbolic link, and a folder too deep.
func walkPlugin(root *os.Root, fn func(name string, fi fs.FileInfo) error) error {
	return walkPluginFS(root.FS(), fn)
}

// walkPluginFS walks a plugin's fs.FS, calling fn for each regular file while
// refusing symbolic links and directories deeper than maxFolderDepth.
func walkPluginFS(fsys fs.FS, fn func(name string, fi fs.FileInfo) error) error {
	return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
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
	return readVersionDir(dir, id, false)
}

func readVersionDir(dir, id string, bundled bool) (*version, error) {
	print := fingerprint(dir, id)
	root, err := openPluginRoot(dir, id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readVersionFS(rootFS{FS: root.FS(), root: root}, id, print, bundled)
}

// readVersionFS reads and validates a plugin's manifest and modules from fsys.
// When print is empty (e.g. an embedded bundled plugin), v.print is set to the
// content hash v.sum.
func readVersionFS(fsys fs.FS, id, print string, bundled bool) (*version, error) {
	data, err := readBoundedFS(fsys, ManifestName, maxManifest)
	if err != nil {
		return nil, err
	}
	man, err := parseManifest(data, bundled)
	if err != nil {
		return nil, err
	}
	if man.ID != id {
		return nil, fmt.Errorf("the manifest's id %q is not its folder's name %q", man.ID, id)
	}
	v := &version{man: man, src: map[string]string{}, print: print}
	if path.Ext(man.Entry) == ".wasm" {
		// Validate directory tree (no symlinks / deep folders), then read the entry binary.
		if err := walkPluginFS(fsys, func(string, fs.FileInfo) error { return nil }); err != nil {
			return nil, err
		}
		b, err := readBoundedFS(fsys, man.Entry, maxWasmBytes)
		if err != nil {
			return nil, fmt.Errorf("the entry %s: %w", man.Entry, err)
		}
		if len(b) < 8 || string(b[:4]) != "\x00asm" {
			return nil, fmt.Errorf("the entry %s is not a WebAssembly binary", man.Entry)
		}
		v.wasm = b
		h := sha256.New()
		fmt.Fprintf(h, "%d\x00%s", len(data), data)
		fmt.Fprintf(h, "%s\x00%d\x00", man.Entry, len(b))
		h.Write(b)
		v.sum = hex.EncodeToString(h.Sum(nil))
		if v.print == "" {
			v.print = v.sum
		}
		return v, nil
	}
	total := 0
	err = walkPluginFS(fsys, func(name string, fi fs.FileInfo) error {
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
		b, err := readBoundedFS(fsys, name, maxSourceBytes)
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
	if v.print == "" {
		v.print = v.sum
	}
	v.order = topoOrderModules(v.src, v.order)
	return v, nil
}

// staticImportRE matches static ES module import/re-export specifiers with
// relative paths (./ or ../) so modules load after their dependencies in
// QuickJS's sequential LoadModule pass.
var staticImportRE = regexp.MustCompile(`(?s)(?:^|[;\n])[ \t]*(?:import|export)\s+(?:[^'";]*?\s+from\s+)?["'](\.\.?/[^"']+)["']`)

func topoOrderModules(src map[string]string, sorted []string) []string {
	if len(sorted) <= 1 {
		return sorted
	}
	deps := make(map[string][]string, len(sorted))
	for _, name := range sorted {
		dir := path.Dir(name)
		matches := staticImportRE.FindAllStringSubmatch(src[name], -1)
		for _, m := range matches {
			spec := m[1]
			target := path.Clean(path.Join(dir, spec))
			if _, ok := src[target]; ok && target != name {
				if !slices.Contains(deps[name], target) {
					deps[name] = append(deps[name], target)
				}
			}
		}
		slices.Sort(deps[name])
	}
	// Deterministic DFS post-order; cycles fall back cleanly without looping.
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(sorted))
	out := make([]string, 0, len(sorted))
	var visit func(name string)
	visit = func(name string) {
		if state[name] != unvisited {
			return
		}
		state[name] = visiting
		for _, dep := range deps[name] {
			visit(dep)
		}
		state[name] = visited
		out = append(out, name)
	}
	for _, name := range sorted {
		visit(name)
	}
	return out
}

// readBoundedFS reads a regular file of fsys up to max bytes, refusing symlinks
// when fsys supports Lstat.
func readBoundedFS(fsys fs.FS, name string, max int) ([]byte, error) {
	var (
		fi  fs.FileInfo
		err error
	)
	if lfs, ok := fsys.(interface {
		Lstat(string) (fs.FileInfo, error)
	}); ok {
		fi, err = lfs.Lstat(name)
	} else {
		fi, err = fs.Stat(fsys, name)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s is missing", name)
		}
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := fsys.Open(name)
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
