package pack

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

// ReadFolder reads a plugin's folder for packing: every regular file but
// those whose names, or whose folders' names, start with a dot, and
// node_modules, which the daemon does not read either. A symbolic link,
// a path the format does not allow, or a folder over its bounds is
// refused.
func ReadFolder(dir string) (map[string][]byte, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
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
		if strings.HasPrefix(d.Name(), ".") || d.IsDir() && d.Name() == "node_modules" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link, which a package may not hold", name)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", name)
		case name == SignatureName:
			return fmt.Errorf("%s is the signature's name, which a plugin's own file may not take", name)
		}
		if err := CheckPath(name); err != nil {
			return err
		}
		if len(files) >= MaxFiles-1 {
			return fmt.Errorf("the folder holds more than %d files", MaxFiles-1)
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
		if len(b) > MaxFileBytes {
			return fmt.Errorf("%s is over %d MiB", name, MaxFileBytes>>20)
		}
		if total += len(b); total > MaxTotalBytes {
			return fmt.Errorf("the folder's files are over %d MiB in all", MaxTotalBytes>>20)
		}
		files[name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Build packs files and signs them with priv, returning the package's
// bytes. What it makes is opened again before it is returned, so that
// nothing that would not install leaves it.
func Build(files map[string][]byte, priv ed25519.PrivateKey) ([]byte, *Package, error) {
	if _, ok := files[SignatureName]; ok {
		return nil, nil, fmt.Errorf("%s is the signature's name, which a plugin's own file may not take", SignatureName)
	}
	folded := map[string]bool{}
	for p := range files {
		if err := CheckPath(p); err != nil {
			return nil, nil, err
		}
		if folded[strings.ToLower(p)] {
			return nil, nil, fmt.Errorf("%q and another path differ only in case", p)
		}
		folded[strings.ToLower(p)] = true
	}
	man, err := checkManifest(files)
	if err != nil {
		return nil, nil, err
	}
	digest := Digest(files)
	sig := Signature{
		Format: SignatureFormat, ID: man.ID, Version: man.Version, Key: PublicKey(priv), Contents: digest,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, Statement(man.ID, man.Version, digest))),
	}
	sigData, err := json.MarshalIndent(sig, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range paths {
		if err := writeEntry(zw, p, files[p]); err != nil {
			return nil, nil, err
		}
	}
	if err := writeEntry(zw, SignatureName, append(sigData, '\n')); err != nil {
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	pkg, err := Open(buf.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("the package made does not open: %w", err)
	}
	return buf.Bytes(), pkg, nil
}

// writeEntry writes a file into the archive as packing always does:
// stored, mode 0644, dated 1980-01-01 with no other time, so that the
// same files make the same bytes.
func writeEntry(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
	h.SetMode(0o644)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Resign packs a package's files again, signed with priv in place of
// the signature it had, which need not hold: for a publisher to sign
// what was packed elsewhere.
func Resign(data []byte, priv ed25519.PrivateKey) ([]byte, *Package, error) {
	files, _, err := readArchive(data)
	if err != nil {
		return nil, nil, err
	}
	return Build(files, priv)
}

// WriteTo writes the package's files, and its signature, into dir,
// which must not exist, through an os.Root that keeps every path inside
// it.
func (p *Package) WriteTo(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	paths := make([]string, 0, len(p.Files)+1)
	for name := range p.Files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	write := func(name string, data []byte) error {
		if err := CheckPath(name); err != nil {
			return err
		}
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			if err := root.MkdirAll(name[:i], 0o755); err != nil {
				return err
			}
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	for _, name := range paths {
		if err := write(name, p.Files[name]); err != nil {
			return err
		}
	}
	if p.sig == nil {
		return errors.New("the package was not opened")
	}
	return write(SignatureName, p.sig)
}
