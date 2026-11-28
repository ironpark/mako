package routines

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/pkg/fsutil"
)

const (
	routinesFileName = "routines.json"
	historyFileName  = "routines-history.json"
	maxHistoryItems  = 200
)

// FilePath returns the path to routines.json inside configDir.
func FilePath(configDir string) string { return filepath.Join(configDir, routinesFileName) }

// HistoryPath returns the path to routines-history.json inside configDir.
func HistoryPath(configDir string) string { return filepath.Join(configDir, historyFileName) }

// Load reads all configured routines from configDir. Missing file returns empty.
func Load(configDir string) ([]Routine, error) {
	data, err := os.ReadFile(FilePath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Routine
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = list[i].Normalized()
	}
	return list, nil
}

// Save atomically persists routines to configDir/routines.json.
func Save(configDir string, list []Routine) error {
	clean := make([]Routine, len(list))
	for i, r := range list {
		r = r.Normalized()
		if err := r.Validate(); err != nil {
			return err
		}
		clean[i] = r
	}
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.Write(FilePath(configDir), raw)
}

// Find looks up a routine by exact ID or case-insensitive name. A name
// more than one routine has finds none: an ID tells them apart.
func Find(list []Routine, idOrName string) (int, bool) {
	q := strings.TrimSpace(idOrName)
	for i, r := range list {
		if r.ID == q {
			return i, true
		}
	}
	found := -1
	for i, r := range list {
		if strings.EqualFold(r.Name, q) {
			if found >= 0 {
				return -1, false
			}
			found = i
		}
	}
	return found, found >= 0
}

// LoadHistory reads recent run records from configDir (newest first).
func LoadHistory(configDir string) ([]RunRecord, error) {
	data, err := os.ReadFile(HistoryPath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []RunRecord
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AppendHistory prepends rec to routines-history.json and trims to
// maxHistoryItems. A history that cannot be read is left as it is, and its
// error returned, rather than replaced by rec alone. It reads and writes
// the file unlocked: its caller serializes appends, as the daemon does.
func AppendHistory(configDir string, rec RunRecord) error {
	list, err := LoadHistory(configDir)
	if err != nil {
		return fmt.Errorf("routine history not updated: %w", err)
	}
	if rec.ID == "" {
		rec.ID = NewID("run")
	}
	next := append([]RunRecord{rec}, list...)
	if len(next) > maxHistoryItems {
		next = slices.Clone(next[:maxHistoryItems])
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.Write(HistoryPath(configDir), raw)
}
