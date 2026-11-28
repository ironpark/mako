// Package repotrust keeps which command files of repositories the user of
// a node trusts. A command file is a file a repository tracks that names
// commands mako would run for it on its own, such as a command to run in
// each new worktree or on a session's events. Anyone who can commit to the
// repository, or an agent working in it, can write one, so mako runs its
// commands only once the user has read the file and trusted it as it is.
//
// A trust is of one file of one repository as it reads: the repository's
// root with its links resolved, the file's path from that root, and the
// SHA-256 of its content. A file that changes is not trusted again until
// the user trusts it as it reads then.
//
// The package knows no kind of command file. A caller names its file by
// its path from the repository's root, and asks about the bytes it read
// and is about to act on, not the file as it may read a moment later.
package repotrust

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/pkg/fsutil"
)

// FileName is the file of the configuration directory the trusts are kept
// in.
const FileName = "repo-trust.json"

// MaxSize is the largest command file Read reads.
const MaxSize = 256 << 10

// Record is one command file trusted.
type Record struct {
	Root      string    `json:"root"`   // the repository's root, links resolved
	Path      string    `json:"path"`   // the file, slash-separated, from Root
	SHA256    string    `json:"sha256"` // of the content trusted, lowercase hex
	TrustedAt time.Time `json:"trusted_at"`
}

// stored is the file the records are kept in.
type stored struct {
	Files []Record `json:"files"`
}

// Store keeps the records in one file, written whole each time. A nil
// Store trusts nothing.
type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// Open is the store of the configuration directory dir. Nothing is read
// until it is asked.
func Open(dir string) *Store {
	return &Store{path: filepath.Join(dir, FileName), now: time.Now}
}

// Hash is the SHA-256 of data, in lowercase hex, as a record names
// content.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Key is what a trust of the file rel of the repository at root is kept
// by: root absolute with its links resolved, and rel clean and
// slash-separated, inside root.
func Key(root, rel string) (string, string, error) {
	if !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("%q is not an absolute path", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", "", fmt.Errorf("%q is not a path from the repository's root", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%q is not a file in the repository", rel)
	}
	return real, filepath.ToSlash(clean), nil
}

// Read reads the file rel of the repository at root to be shown and
// trusted: a regular file, links followed, of at most MaxSize bytes.
func Read(root, rel string) ([]byte, error) {
	real, p, err := Key(root, rel)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(real, filepath.FromSlash(p))
	// Stat first: opening a FIFO waits for a writer.
	before, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s changed as it was read", p)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(io.LimitReader(f, MaxSize+1)); err != nil {
		return nil, err
	}
	if b.Len() > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, MaxSize)
	}
	return b.Bytes(), nil
}

// Trusted reports whether the user trusts the file rel of the repository
// at root as data has it. Whatever cannot be read, the store included,
// is not trusted.
func (s *Store) Trusted(root, rel string, data []byte) bool {
	rec, ok := s.Lookup(root, rel)
	return ok && rec.SHA256 == Hash(data)
}

// Lookup is the record of the file rel of the repository at root, of
// whatever content it was trusted at.
func (s *Store) Lookup(root, rel string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	real, p, err := Key(root, rel)
	if err != nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return Record{}, false
	}
	i := slices.IndexFunc(files, func(r Record) bool { return r.Root == real && r.Path == p })
	if i < 0 {
		return Record{}, false
	}
	return files[i], true
}

// Trust trusts the file rel of the repository at root as its content
// hashes to sum, in place of whatever it was trusted at before.
func (s *Store) Trust(root, rel, sum string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("no configuration directory to keep trust in")
	}
	real, p, err := Key(root, rel)
	if err != nil {
		return Record{}, err
	}
	sum = strings.ToLower(sum)
	if len(sum) != sha256.Size*2 || strings.Trim(sum, "0123456789abcdef") != "" {
		return Record{}, fmt.Errorf("%q is not a SHA-256 in hex", sum)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return Record{}, err
	}
	rec := Record{Root: real, Path: p, SHA256: sum, TrustedAt: s.now().UTC().Truncate(time.Second)}
	files = slices.DeleteFunc(files, func(r Record) bool { return r.Root == real && r.Path == p })
	files = append(files, rec)
	if err := s.save(files); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Revoke forgets the file at path, or every file of the repository whose
// root is path, and returns the records it forgot. path is taken as it is
// and with its links resolved, so a repository no longer there is
// forgotten too.
func (s *Store) Revoke(path string) ([]Record, error) {
	if s == nil {
		return nil, errors.New("no configuration directory to keep trust in")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%q is not an absolute path", path)
	}
	names := []string{filepath.Clean(path)}
	if real, err := filepath.EvalSymlinks(path); err == nil && real != names[0] {
		names = append(names, real)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return nil, err
	}
	var gone []Record
	kept := slices.DeleteFunc(files, func(r Record) bool {
		match := slices.Contains(names, r.Root) || slices.Contains(names, filepath.Join(r.Root, filepath.FromSlash(r.Path)))
		if match {
			gone = append(gone, r)
		}
		return match
	})
	if len(gone) == 0 {
		return nil, nil
	}
	return gone, s.save(kept)
}

// List is every record, by repository and path.
func (s *Store) List() ([]Record, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b Record) int { return cmp.Or(cmp.Compare(a.Root, b.Root), cmp.Compare(a.Path, b.Path)) })
	return files, nil
}

// load reads the records; s.mu is held. No file is no records.
func (s *Store) load() ([]Record, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st stored
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return st.Files, nil
}

// save writes the records whole, as the only writer of the file; s.mu is
// held.
func (s *Store) save(files []Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stored{Files: files}, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.Write(s.path, append(data, '\n'))
}
