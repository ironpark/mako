package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/pkg/fsutil"
)

// Lifecycle hook event names fired across all supported agents.
const (
	EventSessionStart = "on_session_start"
	EventTurnDone     = "on_turn_done"
	EventSessionEnd   = "on_session_end"
)

const (
	hooksFileName      = "hooks.json"
	projectHooksFile   = ".mako/hooks.toml"
	disableHooksEnv    = "MAKO_NO_LIFECYCLE_HOOKS"
	defaultHookTimeout = 30
	maxHookTimeout     = 300
)

// Hook defines a deterministic shell command executed on an agent lifecycle event.
type Hook struct {
	ID         string   `json:"id" toml:"id"`
	Name       string   `json:"name,omitempty" toml:"name"`
	Enabled    bool     `json:"enabled" toml:"enabled"`
	Event      string   `json:"event" toml:"event"`
	Agents     []string `json:"agents,omitempty" toml:"agents"`
	Command    string   `json:"command" toml:"command"`
	TimeoutSec int      `json:"timeout_sec,omitempty" toml:"timeout_sec"`
}

// HookContext carries session metadata passed as environment variables to hooks.
type HookContext struct {
	SessionID string
	Agent     string
	Project   string
	Branch    string
	Event     string
}

type projectHooksDoc struct {
	Hooks []Hook `toml:"hook"`
}

// Validate checks that h has a valid event and non-empty command.
func (h Hook) Validate() error {
	switch h.Event {
	case EventSessionStart, EventTurnDone, EventSessionEnd:
	default:
		return fmt.Errorf("unknown hook event %q", h.Event)
	}
	if strings.TrimSpace(h.Command) == "" {
		return errors.New("hook command cannot be empty")
	}
	return nil
}

// Normalized populates defaults for h.
func (h Hook) Normalized() Hook {
	h.ID = strings.TrimSpace(h.ID)
	if h.ID == "" {
		h.ID = NewID("hk")
	}
	h.Name = strings.TrimSpace(h.Name)
	h.Event = strings.TrimSpace(h.Event)
	h.Command = strings.TrimSpace(h.Command)
	if h.TimeoutSec <= 0 {
		h.TimeoutSec = defaultHookTimeout
	}
	if h.TimeoutSec > maxHookTimeout {
		h.TimeoutSec = maxHookTimeout
	}
	return h
}

// LoadHooks reads global lifecycle hooks from configDir/hooks.json.
func LoadHooks(configDir string) ([]Hook, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(configDir, hooksFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Hook
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = list[i].Normalized()
	}
	return list, nil
}

// SaveHooks atomically persists global lifecycle hooks to configDir/hooks.json.
func SaveHooks(configDir string, hooks []Hook) error {
	clean := make([]Hook, len(hooks))
	for i, h := range hooks {
		h = h.Normalized()
		if err := h.Validate(); err != nil {
			return err
		}
		clean[i] = h
	}
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.Write(filepath.Join(configDir, hooksFileName), raw)
}

// LoadProjectHooks reads optional repository lifecycle hooks from <project>/.mako/hooks.toml.
func LoadProjectHooks(projectDir string) []Hook {
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(projectHooksFile)))
	if err != nil {
		return nil
	}
	var doc projectHooksDoc
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil
	}
	var out []Hook
	for _, h := range doc.Hooks {
		h = h.Normalized()
		if h.Validate() == nil {
			out = append(out, h)
		}
	}
	return out
}

// RunHooks executes all matching enabled global and project hooks for hctx.
// Returns the first error encountered (if any), while still attempting all hooks.
func RunHooks(ctx context.Context, configDir string, hctx HookContext) error {
	if os.Getenv(disableHooksEnv) == "1" {
		return nil
	}
	globalHooks, _ := LoadHooks(configDir)
	projectHooks := LoadProjectHooks(hctx.Project)
	all := slices.Concat(globalHooks, projectHooks)
	var firstErr error
	for _, h := range all {
		if !h.Enabled || h.Event != hctx.Event {
			continue
		}
		if len(h.Agents) > 0 {
			matched := false
			for _, a := range h.Agents {
				if a == "*" || strings.EqualFold(strings.TrimSpace(a), hctx.Agent) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if err := runSingleHook(ctx, h, hctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runSingleHook(ctx context.Context, h Hook, hctx HookContext) error {
	timeout := time.Duration(h.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultHookTimeout * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/C", h.Command)
	} else {
		cmd = exec.CommandContext(runCtx, "sh", "-c", h.Command)
	}
	if hctx.Project != "" {
		cmd.Dir = hctx.Project
	}
	cmd.Env = append(os.Environ(),
		"AGENT_MAKO_SESSION="+hctx.SessionID,
		"AGENT_MAKO_AGENT="+hctx.Agent,
		"AGENT_MAKO_PROJECT="+hctx.Project,
		"AGENT_MAKO_BRANCH="+hctx.Branch,
		"AGENT_MAKO_EVENT="+hctx.Event,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("hook %s failed: %w: %s", h.ID, err, msg)
		}
		return fmt.Errorf("hook %s failed: %w", h.ID, err)
	}
	return nil
}
