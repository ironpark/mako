// Package atomicfile replaces a file whole: whoever reads it, a crash
// included, finds the old file or the new, never half of one.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces the file at path with data, through a file of its own
// beside it (mode 0600) renamed over path once written and synced.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone once renamed; left behind on an error
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
