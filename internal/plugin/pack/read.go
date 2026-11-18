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
	"strings"

	"github.com/ironpark/mako/internal/plugin"
)

// Package is a package read and checked: its signature holds for its
// contents, by the key it names, and its manifest reads and is the
// signed id and version. Whose key that is, and whether it is the one
// expected, is the reader's to check.
type Package struct {
	Manifest *plugin.Manifest
	// Files are the plugin's files by path, the signature's left out.
	Files map[string][]byte
	// Key is the publisher key that signed it, and Digest the contents'.
	Key    string
	Digest string
	sig    []byte // the signature's file as read
}

// Open reads and checks a package. Nothing in it is trusted until this
// returns: sizes are counted as the bytes are read, never taken from the
// archive's headers, and a path is checked before it is used.
func Open(data []byte) (*Package, error) {
	files, sigData, err := readArchive(data)
	if err != nil {
		return nil, err
	}
	if sigData == nil {
		return nil, fmt.Errorf("the package holds no %s: it is not signed", SignatureName)
	}
	var sig Signature
	dec := json.NewDecoder(bytes.NewReader(sigData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sig); err != nil {
		return nil, fmt.Errorf("%s does not read: %v", SignatureName, err)
	}
	if sig.Format != SignatureFormat {
		return nil, fmt.Errorf("%s is of format %q, which this mako does not read", SignatureName, trim(sig.Format))
	}
	pub, err := ParseKey(sig.Key)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", SignatureName, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig.Signature)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%s: the signature is not 64 bytes in unpadded base64url", SignatureName)
	}
	digest := Digest(files)
	if sig.Contents != digest || !ed25519.Verify(pub, Statement(sig.ID, sig.Version, digest), raw) {
		return nil, errors.New("the package's signature does not hold for its contents: it was changed after it was signed, or signed wrongly")
	}
	man, err := checkManifest(files)
	if err != nil {
		return nil, err
	}
	if man.ID != sig.ID || man.Version != sig.Version {
		return nil, fmt.Errorf("the package's manifest is %s %s, but its signature is for %s %s", man.ID, man.Version, trim(sig.ID), trim(sig.Version))
	}
	return &Package{Manifest: man, Files: files, Key: sig.Key, Digest: digest, sig: sigData}, nil
}

// checkManifest reads the files' manifest strictly, as a folder's is
// read, and finds its entry among them.
func checkManifest(files map[string][]byte) (*plugin.Manifest, error) {
	data, ok := files[plugin.ManifestName]
	if !ok {
		return nil, fmt.Errorf("the package holds no %s at its root", plugin.ManifestName)
	}
	man, err := plugin.ParseManifest(data)
	if err != nil {
		return nil, err
	}
	if _, ok := files[man.Entry]; !ok {
		return nil, fmt.Errorf("the entry %s is not in the package", man.Entry)
	}
	return man, nil
}

// readArchive reads the archive's files, refusing anything the format
// does not allow; the signature's file comes apart, nil if there is none.
func readArchive(data []byte) (map[string][]byte, []byte, error) {
	if len(data) > MaxPackageBytes {
		return nil, nil, fmt.Errorf("the package is over %d MiB", MaxPackageBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("the package is not a zip archive that reads: %v", err)
	}
	if len(zr.File) > MaxFiles {
		return nil, nil, fmt.Errorf("the package holds more than %d files", MaxFiles)
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	var sig []byte
	total := 0
	for _, f := range zr.File {
		if err := CheckPath(f.Name); err != nil {
			return nil, nil, fmt.Errorf("the package holds %v", err)
		}
		// Two paths one file system takes for one file would let the
		// second written replace the first, unseen.
		folded := strings.ToLower(f.Name)
		if seen[folded] {
			return nil, nil, fmt.Errorf("the package holds %q twice", trim(f.Name))
		}
		seen[folded] = true
		switch {
		case !f.Mode().IsRegular():
			return nil, nil, fmt.Errorf("the package holds %q, which is not a regular file (a folder or a link)", trim(f.Name))
		case f.Flags&0x1 != 0:
			return nil, nil, fmt.Errorf("the package holds %q encrypted", trim(f.Name))
		case f.Method != zip.Store && f.Method != zip.Deflate:
			return nil, nil, fmt.Errorf("the package holds %q compressed in a way mako does not read", trim(f.Name))
		case f.UncompressedSize64 > MaxFileBytes:
			return nil, nil, fmt.Errorf("the package holds %q, over %d MiB", trim(f.Name), MaxFileBytes>>20)
		}
		b, err := readFile(f)
		if err != nil {
			return nil, nil, err
		}
		if total += len(b); total > MaxTotalBytes {
			return nil, nil, fmt.Errorf("the package's files are over %d MiB in all", MaxTotalBytes>>20)
		}
		if f.Name == SignatureName {
			if len(b) > maxSignature {
				return nil, nil, fmt.Errorf("%s is over %d KiB", SignatureName, maxSignature>>10)
			}
			sig = b
			continue
		}
		files[f.Name] = b
	}
	return files, sig, nil
}

// readFile reads a file of the archive, no more than it may hold, and
// as many bytes as its header says, whose checksum must hold.
func readFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("the package's %q does not read: %v", trim(f.Name), err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("the package's %q does not read: %v", trim(f.Name), err)
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("the package holds %q, over %d MiB once read", trim(f.Name), MaxFileBytes>>20)
	}
	if uint64(len(b)) != f.UncompressedSize64 {
		return nil, fmt.Errorf("the package's %q is not the size its header says", trim(f.Name))
	}
	return b, nil
}
