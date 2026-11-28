package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/ironpark/mako/pkg/fsutil"
)

const (
	stateFileName = "routines-state.json"
	// maxSeenRuns bounds the failed runs a routine's state names.
	maxSeenRuns = 256
)

// TriggerState is what the daemon's scheduler keeps of one routine's
// triggers, between its ticks and across its restarts, so a restart
// neither runs a routine twice nor loses where its Forge polls were.
type TriggerState struct {
	// Source is the trigger and project the Forge cursors below are of;
	// a routine changed to another starts them afresh.
	Source string `json:"source,omitempty"`
	// LastCronMinute is the unix minute the routine last ran by its
	// schedule.
	LastCronMinute int64 `json:"last_cron_minute,omitempty"`
	// Baselined is set once a poll of the forge has seen what was there
	// before: what the first poll sees runs nothing.
	Baselined bool `json:"baselined,omitempty"`
	// PRWatermark is the highest pull request number a poll has seen.
	PRWatermark int64 `json:"pr_watermark,omitempty"`
	// FailedRuns are the failed CI runs a poll has seen.
	FailedRuns Seen `json:"failed_runs,omitzero"`
	// LastForgeRun is when, in unix seconds, a Forge event last ran the
	// routine, for its cooldown.
	LastForgeRun int64 `json:"last_forge_run,omitempty"`
}

// For is s as the state of a routine whose trigger and project are
// source: s itself, or for another source s's cron minute alone.
func (s TriggerState) For(source string) TriggerState {
	if s.Source == source {
		return s
	}
	return TriggerState{Source: source, LastCronMinute: s.LastCronMinute}
}

// Seen is a bounded set of increasing IDs, such as a forge's run IDs: the
// maxSeenRuns highest added, and a floor at or below which every ID counts
// as seen, raised past each ID let go.
type Seen struct {
	Floor int64   `json:"floor,omitempty"`
	IDs   []int64 `json:"ids,omitempty"` // ascending
}

// Has reports whether id was added, or is at or below the floor.
func (s Seen) Has(id int64) bool {
	if id <= s.Floor {
		return true
	}
	_, ok := slices.BinarySearch(s.IDs, id)
	return ok
}

// Add adds id, letting the lowest go past maxSeenRuns.
func (s *Seen) Add(id int64) {
	if s.Has(id) {
		return
	}
	i, _ := slices.BinarySearch(s.IDs, id)
	s.IDs = slices.Insert(s.IDs, i, id)
	if over := len(s.IDs) - maxSeenRuns; over > 0 {
		s.Floor = max(s.Floor, s.IDs[over-1])
		s.IDs = slices.Delete(s.IDs, 0, over)
	}
}

// StatePath returns the path to routines-state.json inside configDir.
func StatePath(configDir string) string { return filepath.Join(configDir, stateFileName) }

// LoadState reads the routines' trigger states, by routine ID; a missing
// file is none.
func LoadState(configDir string) (map[string]TriggerState, error) {
	data, err := os.ReadFile(StatePath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]TriggerState{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := map[string]TriggerState{}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return st, nil
}

// SaveState writes the routines' trigger states atomically.
func SaveState(configDir string, st map[string]TriggerState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.Write(StatePath(configDir), append(raw, '\n'))
}
