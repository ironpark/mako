package routines

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// TriggerKind describes what initiates a routine execution.
type TriggerKind string

const (
	TriggerCron        TriggerKind = "cron"
	TriggerForgePR     TriggerKind = "forge_pr_opened"
	TriggerForgeCIFail TriggerKind = "forge_ci_failed"
)

// Routine is a self-hosted scheduled or event-driven agent task owned by makod.
type Routine struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Enabled      bool        `json:"enabled"`
	Trigger      TriggerKind `json:"trigger"`
	Schedule     string      `json:"schedule,omitempty"`
	Project      string      `json:"project"`
	Agent        string      `json:"agent"`
	Worktree     bool        `json:"worktree,omitempty"`
	BranchPrefix string      `json:"branch_prefix,omitempty"`
	Prompt       string      `json:"prompt"`
}

// RunRecord captures one execution attempt of a routine.
type RunRecord struct {
	ID          string `json:"id"`
	RoutineID   string `json:"routine_id"`
	RoutineName string `json:"routine_name"`
	Trigger     string `json:"trigger"`
	SessionID   string `json:"session_id,omitempty"`
	Status      string `json:"status"` // "started" or "error"
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at"` // unix seconds
}

// NewID generates a compact random identifier for a routine or run record.
func NewID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// Validate checks that r has valid fields before saving or scheduling.
func (r Routine) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("routine name cannot be empty")
	}
	if strings.TrimSpace(r.Project) == "" {
		return errors.New("routine project cannot be empty")
	}
	if strings.TrimSpace(r.Agent) == "" {
		return errors.New("routine agent cannot be empty")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("routine prompt cannot be empty")
	}
	if err := ValidBranchPrefix(r.BranchPrefix); err != nil {
		return err
	}
	switch r.Trigger {
	case "", TriggerCron:
		if _, err := ParseSchedule(r.Schedule); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	case TriggerForgePR, TriggerForgeCIFail:
		// Forge event triggers do not require a cron schedule.
	default:
		return fmt.Errorf("unknown routine trigger %q", r.Trigger)
	}
	return nil
}

// Normalized returns r with defaults populated.
func (r Routine) Normalized() Routine {
	r.ID = strings.TrimSpace(r.ID)
	if r.ID == "" {
		r.ID = NewID("rtn")
	}
	r.Name = strings.TrimSpace(r.Name)
	r.Project = strings.TrimSpace(r.Project)
	r.Agent = strings.TrimSpace(r.Agent)
	r.Schedule = strings.TrimSpace(r.Schedule)
	r.BranchPrefix = strings.TrimSpace(r.BranchPrefix)
	r.Prompt = strings.TrimSpace(r.Prompt)
	if r.Trigger == "" {
		r.Trigger = TriggerCron
	}
	return r
}

// ValidBranchPrefix checks a routine's worktree branch prefix: "" is the
// default prefix, and any other must make a branch name git
// check-ref-format --branch takes once a run's suffix is added to it.
func ValidBranchPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	bad := func(why string) error { return fmt.Errorf("invalid branch prefix %q: %s", prefix, why) }
	if strings.HasSuffix(prefix, "/") {
		return bad(`it ends with "/"`)
	}
	if why := badBranch(prefix + "-20060102-150405-000000"); why != "" {
		return bad(why)
	}
	return nil
}

// badBranch says why name is not a branch name git check-ref-format
// --branch takes, "" when it is.
func badBranch(name string) string {
	switch {
	case strings.HasPrefix(name, "-"):
		return `it begins with "-"`
	case strings.HasPrefix(name, "/"):
		return `it begins with "/"`
	case strings.HasSuffix(name, "."):
		return `it ends with "."`
	case strings.Contains(name, ".."):
		return `it contains ".."`
	case strings.Contains(name, "//"):
		return `it contains "//"`
	case strings.Contains(name, "@{"), name == "@":
		return `it contains "@{"`
	}
	for _, c := range name {
		switch {
		case c < 0x20 || c == 0x7f:
			return "it contains a control character"
		case unicode.IsSpace(c):
			return "it contains white space"
		case strings.ContainsRune(`~^:?*[\`, c):
			return fmt.Sprintf("it contains %q", c)
		}
	}
	for comp := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(comp, ".") {
			return `a part of it begins with "."`
		}
		if strings.HasSuffix(comp, ".lock") {
			return `a part of it ends with ".lock"`
		}
	}
	return ""
}
