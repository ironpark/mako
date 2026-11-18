package pack

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// ReadInstalled reads a plugin's folder as a package installed it: its
// contents' digest, of every file but the signature's and those starting
// with a dot, which no package holds and no plugin reads (a file
// manager's own, as Finder's .DS_Store); and its signature's file, nil
// if it has none. A folder a package could not have written, with a
// symbolic link, a file other than a regular one, or over a package's
// bounds, is an error: it is not one that was installed.
func ReadInstalled(dir string) (digest string, sig *Signature, err error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", nil, err
	}
	if !fi.IsDir() {
		return "", nil, fmt.Errorf("%s is not a folder", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	files := map[string][]byte{}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link", name)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", name)
		case len(files) >= MaxFiles:
			return fmt.Errorf("more than %d files", MaxFiles)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
		if err != nil {
			return err
		}
		if total += len(b); len(b) > MaxFileBytes || total > MaxTotalBytes {
			return fmt.Errorf("over a package's bounds")
		}
		files[name] = b
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if b, ok := files[SignatureName]; ok {
		var s Signature
		if json.Unmarshal(b, &s) == nil {
			sig = &s
		}
	}
	return Digest(files), sig, nil
}
