package routines

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
