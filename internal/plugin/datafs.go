package plugin

import (
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// Sandboxed per-scope private file storage for plugins under
// plugin-data/<plugin-id>/files/<scope-dir>/. Every path is checked with
// relPath and accessed exclusively through Go's os.Root so symlinks or ".."
// segments can never escape the plugin's assigned directory.

const (
	defaultDataFSQuota = 64 << 20 // 64 MiB total across all scopes of a plugin
	maxDataFileSize    = 4 << 20  // 4 MiB max single file size
	maxDataReadPiece   = 2 << 20  // 2 MiB max single read slice
	maxDataFiles       = 4096     // max regular files across all scopes
	datafsOpsPerMinute = 240
	datafsTmpPrefix    = ".mako-tmp-"
)

type datafsStore struct {
	mu    sync.Mutex
	base  string // plugin-data/<id>/files
	quota int
}

func newDatafsStore(base string, quota int) *datafsStore {
	if quota <= 0 {
		quota = defaultDataFSQuota
	}
	return &datafsStore{base: base, quota: quota}
}

// scopeDirName maps a validated canonical scopeKey to a deterministic,
// human-readable, ASCII-only folder name under plugin-data/<id>/files/.
func datafsScopeDirName(scopeKey string) (string, error) {
	kind, id, err := parseScopeKey(scopeKey)
	if err != nil {
		return "", err
	}
	if scopeKey == "" {
		return "global", nil
	}
	sum := sha256.Sum256([]byte(scopeKey))
	hash := hex.EncodeToString(sum[:4])

	// Take the trailing segment after '/' or ':' for a readable folder slug.
	tail := id
	if i := strings.LastIndexAny(tail, "/:"); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	var slug strings.Builder
	for _, r := range strings.ToLower(tail) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			slug.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			if slug.Len() > 0 && slug.String()[slug.Len()-1] != '-' {
				slug.WriteByte('-')
			}
		}
		if slug.Len() >= 24 {
			break
		}
	}
	s := strings.Trim(slug.String(), "-")
	if s == "" {
		return kind + "-" + hash, nil
	}
	return kind + "-" + s + "-" + hash, nil
}

func isInternalTmpPath(p string) bool {
	for part := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(part, datafsTmpPrefix) {
			return true
		}
	}
	return false
}

func datafsPath(raw string, allowRoot bool) (string, error) {
	if !allowRoot && raw == "." {
		return "", fmt.Errorf("%q is not a file or subfolder path", raw)
	}
	rel, err := relPath(raw, allowRoot)
	if err != nil {
		return "", err
	}
	if (!allowRoot || (raw != "" && raw != ".")) && isInternalTmpPath(raw) {
		return "", errors.New("reserved internal file name")
	}
	return rel, nil
}

func (s *datafsStore) openRootLocked(scopeKey string, create bool) (*os.Root, error) {
	name, err := datafsScopeDirName(scopeKey)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.base, name)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	} else {
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fs.ErrNotExist
			}
			return nil, err
		}
	}
	return os.OpenRoot(dir)
}

// usageLocked totals regular file bytes and file count across all scopes of
// this plugin.
func (s *datafsStore) usageLocked() (usedBytes int64, fileCount int) {
	if _, err := os.Stat(s.base); err != nil {
		return 0, 0
	}
	_ = filepath.WalkDir(s.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), datafsTmpPrefix) {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			usedBytes += info.Size()
			fileCount++
		}
		return nil
	})
	return usedBytes, fileCount
}

func (s *datafsStore) read(scopeKey, rawPath string, off float64, length uint32) (*hostapi.ReadDataResponse, error) {
	rel, err := datafsPath(rawPath, false)
	if err != nil {
		return nil, err
	}
	if off < 0 || off != math.Trunc(off) || off > 1<<53 {
		return nil, errors.New("offset is a whole number of bytes, 0 or more")
	}
	n := int(length)
	if n == 0 || n > maxDataReadPiece {
		n = maxDataReadPiece
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.ReadDataResponse{Found: false}, nil
		}
		return nil, err
	}
	defer r.Close()

	info, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.ReadDataResponse{Found: false}, nil
		}
		return nil, pathError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rawPath)
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, pathError(err)
	}
	defer f.Close()

	buf := make([]byte, n)
	got, err := f.ReadAt(buf, int64(off))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:got]
	out := &hostapi.ReadDataResponse{
		Found:      true,
		Data:       buf,
		Size:       float64(info.Size()),
		Eof:        int64(off)+int64(got) >= info.Size(),
		ModifiedMs: float64(info.ModTime().UnixMilli()),
	}
	if utf8.Valid(buf) {
		out.Text = string(buf)
	} else {
		out.Binary = true
	}
	return out, nil
}

func (s *datafsStore) write(scopeKey, rawPath, text string, rawData []byte, makeDirs bool) (*hostapi.WriteDataResponse, error) {
	rel, err := datafsPath(rawPath, false)
	if err != nil {
		return nil, err
	}
	data := rawData
	if text != "" {
		if len(data) > 0 {
			return nil, errors.New("give data or text, not both")
		}
		data = []byte(text)
	}
	if len(data) > maxDataFileSize {
		return nil, fmt.Errorf("a data file is at most %d MiB", maxDataFileSize>>20)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, true)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var existingSize int64
	existed := false
	if info, err := r.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", rawPath)
		}
		existingSize = info.Size()
		existed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, pathError(err)
	}

	used, count := s.usageLocked()
	nextUsed := used - existingSize + int64(len(data))
	if nextUsed > int64(s.quota) {
		return nil, fmt.Errorf("plugin data file quota of %d MiB reached", s.quota>>20)
	}
	if !existed && count+1 > maxDataFiles {
		return nil, fmt.Errorf("plugin data file limit of %d files reached", maxDataFiles)
	}

	dir := filepath.Dir(rel)
	if makeDirs && dir != "." {
		if err := r.MkdirAll(dir, 0o700); err != nil {
			return nil, pathError(err)
		}
	}

	tmp := filepath.Join(dir, datafsTmpPrefix+nonce()+".tmp")
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	return &hostapi.WriteDataResponse{
		Size:       float64(len(data)),
		UsedBytes:  uint32(min(nextUsed, math.MaxUint32)),
		QuotaBytes: uint32(min(int64(s.quota), math.MaxUint32)),
	}, nil
}

func (s *datafsStore) list(scopeKey, rawPath string, recursive bool) (*hostapi.ListDataResponse, error) {
	rel, err := datafsPath(rawPath, true)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.ListDataResponse{}, nil
		}
		return nil, err
	}
	defer r.Close()

	info, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.ListDataResponse{}, nil
		}
		return nil, pathError(err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", rawPath)
	}

	out := &hostapi.ListDataResponse{}
	var walk func(relDir, slashPrefix string) error
	walk = func(relDir, slashPrefix string) error {
		d, err := r.Open(relDir)
		if err != nil {
			return pathError(err)
		}
		entries, err := d.ReadDir(maxDirEntries + 1)
		_ = d.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, datafsTmpPrefix) || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if len(out.Entries) >= maxDirEntries {
				out.Truncated = true
				return nil
			}
			entrySlash := name
			if slashPrefix != "" {
				entrySlash = slashPrefix + "/" + name
			}
			de := &hostapi.DataEntry{
				Path: entrySlash,
				Name: name,
				Dir:  e.IsDir(),
			}
			if fi, err := e.Info(); err == nil {
				if fi.Mode().IsRegular() {
					de.Size = float64(fi.Size())
				}
				de.ModifiedMs = float64(fi.ModTime().UnixMilli())
			}
			out.Entries = append(out.Entries, de)
			if recursive && e.IsDir() {
				subRel := name
				if relDir != "." {
					subRel = filepath.Join(relDir, name)
				}
				if err := walk(subRel, entrySlash); err != nil {
					return err
				}
				if out.Truncated {
					return nil
				}
			}
		}
		return nil
	}

	prefix := ""
	if rawPath != "" && rawPath != "." {
		prefix = path.Clean(rawPath)
	}
	if err := walk(rel, prefix); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *datafsStore) stat(scopeKey, rawPath string) (*hostapi.StatDataResponse, error) {
	rel, err := datafsPath(rawPath, true)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.StatDataResponse{Found: false}, nil
		}
		return nil, err
	}
	defer r.Close()

	info, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &hostapi.StatDataResponse{Found: false}, nil
		}
		return nil, pathError(err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return &hostapi.StatDataResponse{Found: false}, nil
	}
	resp := &hostapi.StatDataResponse{
		Found:      true,
		Dir:        info.IsDir(),
		ModifiedMs: float64(info.ModTime().UnixMilli()),
	}
	if info.Mode().IsRegular() {
		resp.Size = float64(info.Size())
	}
	return resp, nil
}

func (s *datafsStore) mkdir(scopeKey, rawPath string) error {
	rel, err := datafsPath(rawPath, false)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, true)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.MkdirAll(rel, 0o700); err != nil {
		return pathError(err)
	}
	return nil
}

func (s *datafsStore) remove(scopeKey, rawPath string, recursive bool) (bool, error) {
	rel, err := datafsPath(rawPath, false)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer r.Close()

	info, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, pathError(err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is a symbolic link", rawPath)
	}
	if info.IsDir() && recursive {
		if err := r.RemoveAll(rel); err != nil {
			return false, pathError(err)
		}
		return true, nil
	}
	if err := r.Remove(rel); err != nil {
		return false, pathError(err)
	}
	return true, nil
}

func (s *datafsStore) move(scopeKey, fromRaw, toRaw string, makeDirs bool) error {
	fromRel, err := datafsPath(fromRaw, false)
	if err != nil {
		return err
	}
	toRel, err := datafsPath(toRaw, false)
	if err != nil {
		return err
	}
	if fromRaw == toRaw {
		return nil
	}
	if strings.HasPrefix(toRaw+"/", fromRaw+"/") {
		return errors.New("cannot move a folder inside itself")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.openRootLocked(scopeKey, false)
	if err != nil {
		return pathError(err)
	}
	defer r.Close()

	srcInfo, err := r.Lstat(fromRel)
	if err != nil {
		return pathError(err)
	}
	if srcInfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", fromRaw)
	}
	if dstInfo, err := r.Lstat(toRel); err == nil {
		if dstInfo.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", toRaw)
		}
		if dstInfo.IsDir() != srcInfo.IsDir() {
			return fmt.Errorf("cannot replace %s with a different entry type", toRaw)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pathError(err)
	}

	if makeDirs {
		if dir := filepath.Dir(toRel); dir != "." {
			if err := r.MkdirAll(dir, 0o700); err != nil {
				return pathError(err)
			}
		}
	}
	if err := r.Rename(fromRel, toRel); err != nil {
		return pathError(err)
	}
	return nil
}

func (s *datafsStore) stats(scopeKey string) (*hostapi.DataStatsResponse, error) {
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	used, count := s.usageLocked()
	return &hostapi.DataStatsResponse{
		UsedBytes:  uint32(min(used, math.MaxUint32)),
		QuotaBytes: uint32(min(int64(s.quota), math.MaxUint32)),
		FileCount:  uint32(min(count, math.MaxUint32)),
	}, nil
}
