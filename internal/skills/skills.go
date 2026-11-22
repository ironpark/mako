// Package skills embeds Mako's built-in authoring skills (mako-template and
// mako-plugin) and registers them into each supported agent's skills directory
// so agents discover them natively via slash commands and progressive skill
// loading without overwriting user-customized copies.
package skills

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ManagedMarker tags skill files written by Mako so Mako can refresh them on
// upgrade while leaving any user-edited skill (without the marker) untouched.
const ManagedMarker = "<!-- mako-managed-skill -->"

//go:embed builtin
var builtinFS embed.FS

// Skill describes one built-in skill embedded in Mako.
type Skill struct {
	Name    string
	Content []byte
}

// Builtin returns every built-in skill sorted by name.
func Builtin() []Skill {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := builtinFS.ReadFile(filepath.ToSlash(filepath.Join("builtin", e.Name(), "SKILL.md")))
		if err != nil {
			continue
		}
		out = append(out, Skill{Name: e.Name(), Content: data})
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Names returns the sorted names of all built-in skills.
func Names() []string {
	list := Builtin()
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Name
	}
	return out
}

// AgentSkillsDirs returns the skill discovery directories for agentName given
// its launch/runtime environment env. Returns nil for agents without a known
// file-based skills directory.
func AgentSkillsDirs(agentName string, env map[string]string) []string {
	get := func(key string) string {
		if env != nil {
			if v := env[key]; v != "" {
				return v
			}
		}
		return os.Getenv(key)
	}
	home := get("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	switch agentName {
	case "agy":
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".gemini", "config", "skills")}
	case "claude":
		if dir := get("CLAUDE_CONFIG_DIR"); dir != "" {
			return []string{filepath.Join(dir, "skills")}
		}
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".claude", "skills")}
	case "codex":
		if dir := get("CODEX_HOME"); dir != "" {
			return []string{filepath.Join(dir, "skills")}
		}
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".codex", "skills")}
	case "pi":
		if dir := get("PI_CODING_AGENT_DIR"); dir != "" {
			return []string{filepath.Join(dir, "skills")}
		}
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".pi", "agent", "skills")}
	case "opencode":
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".config", "opencode", "skills")}
	default:
		return nil
	}
}

// EnsureAgentSkills installs or updates Mako's built-in skills in the skill
// directory of agentName (resolved from env). Safe to call repeatedly.
func EnsureAgentSkills(agentName string, env map[string]string) error {
	var firstErr error
	for _, dir := range AgentSkillsDirs(agentName, env) {
		if err := SyncToDir(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SyncToDir installs or updates every built-in skill under skillsRoot
// (<skillsRoot>/<name>/SKILL.md). Existing files without ManagedMarker are
// preserved untouched so user overrides are never clobbered.
func SyncToDir(skillsRoot string) error {
	if skillsRoot == "" {
		return nil
	}
	for _, sk := range Builtin() {
		dir := filepath.Join(skillsRoot, sk.Name)
		dst := filepath.Join(dir, "SKILL.md")
		existing, err := os.ReadFile(dst)
		if err == nil {
			if bytes.Equal(existing, sk.Content) {
				continue
			}
			if !bytes.Contains(existing, []byte(ManagedMarker)) {
				continue // user-customized skill: leave untouched
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", dst, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		tmp := dst + ".mako-tmp"
		if err := os.WriteFile(tmp, sk.Content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename %s: %w", dst, err)
		}
	}
	return nil
}
