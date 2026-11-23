// Package skills embeds Mako's built-in authoring skills (mako-template and
// mako-plugin) and registers them into each supported agent's skills directory
// so agents discover them natively via slash commands and progressive skill
// loading without overwriting user-customized copies.
//
// Users can also customize any built-in skill inside Mako (stored under
// <configDir>/skills/<name>/SKILL.md). Effective() resolves overrides on top of
// the embedded defaults, and SyncToDir distributes them to every agent skills
// directory managed by Mako.
package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/pkg/fsutil"
)

// ManagedMarker tags skill files written by Mako so Mako can refresh them on
// upgrade or after a user edits a built-in skill in Mako, while leaving any
// manually placed skill file (without the marker) untouched.
const ManagedMarker = "<!-- mako-managed-skill -->"

// SupportedAgents lists the agent drivers whose skill directories Mako syncs.
var SupportedAgents = []string{"agy", "claude", "codex", "pi", "omp", "omo", "opencode"}

//go:embed builtin
var builtinFS embed.FS

// Skill describes one built-in skill (or its user-customized override).
type Skill struct {
	Name        string
	Description string
	Content     []byte
	Customized  bool
}

// Frontmatter extracts the YAML frontmatter name and description fields from a
// SKILL.md document, if present.
func Frontmatter(content []byte) (name, description string) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", ""
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		switch k {
		case "name":
			name = v
		case "description":
			description = v
		}
	}
	return name, description
}

// Builtin returns every built-in skill embedded in Mako, sorted by name.
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
		_, desc := Frontmatter(data)
		out = append(out, Skill{Name: e.Name(), Description: desc, Content: data})
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// BuiltinByName returns the embedded built-in skill with the given name.
func BuiltinByName(name string) (Skill, bool) {
	for _, s := range Builtin() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
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

// OverridesDir returns Mako's skill override directory (<configDir>/skills),
// honoring MAKO_HOME to match internal/config.SkillsDir without an import cycle.
func OverridesDir() (string, error) {
	if custom := os.Getenv("MAKO_HOME"); custom != "" {
		return filepath.Join(custom, "skills"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mako", "skills"), nil
}

// OverridePath returns the on-disk override path (<configDir>/skills/<name>/SKILL.md)
// for a built-in skill.
func OverridePath(name string) (string, error) {
	if _, ok := BuiltinByName(name); !ok {
		return "", fmt.Errorf("unknown built-in skill %q", name)
	}
	dir, err := OverridesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name, "SKILL.md"), nil
}

// Effective returns every built-in skill with any user override from
// <configDir>/skills/<name>/SKILL.md applied on top.
func Effective() []Skill {
	dir, _ := OverridesDir()
	return EffectiveFrom(dir)
}

// EffectiveFrom returns every built-in skill with overrides read from
// <overridesDir>/<name>/SKILL.md applied on top.
func EffectiveFrom(overridesDir string) []Skill {
	list := Builtin()
	if overridesDir == "" {
		return list
	}
	for i := range list {
		path := filepath.Join(overridesDir, list[i].Name, "SKILL.md")
		if data, err := os.ReadFile(path); err == nil {
			_, desc := Frontmatter(data)
			list[i].Content = data
			if desc != "" {
				list[i].Description = desc
			}
			list[i].Customized = true
		}
	}
	return list
}

// EffectiveByName returns the effective (possibly customized) skill for name.
func EffectiveByName(name string) (Skill, bool) {
	for _, s := range Effective() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// SaveOverride writes a customized version of a built-in skill to
// <configDir>/skills/<name>/SKILL.md.
func SaveOverride(name string, content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return errors.New("skill content cannot be empty")
	}
	dst, err := OverridePath(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := fsutil.Write(dst, content); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// ResetOverride removes the user override for a built-in skill, restoring the
// embedded default.
func ResetOverride(name string) error {
	dst, err := OverridePath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dst, err)
	}
	_ = os.Remove(filepath.Dir(dst)) // remove empty skill folder if nothing else is inside
	return nil
}

// ensureManagedContent guarantees ManagedMarker is present when syncing into
// agent skill directories so subsequent edits or resets in Mako continue to
// propagate cleanly while still respecting manually authored files without the marker.
func ensureManagedContent(content []byte) []byte {
	if bytes.Contains(content, []byte(ManagedMarker)) {
		return content
	}
	out := make([]byte, 0, len(content)+len(ManagedMarker)+2)
	out = append(out, content...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, ManagedMarker...)
	out = append(out, '\n')
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
	case "omp":
		if dir := get("PI_CODING_AGENT_DIR"); dir != "" {
			return []string{filepath.Join(dir, "skills")}
		}
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".omp", "agent", "skills")}
	case "omo":
		if dir := get("OMO_CODING_AGENT_DIR"); dir != "" {
			return []string{filepath.Join(dir, "skills")}
		}
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".omo", "agent", "skills")}
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

// SyncToDir installs or updates every effective built-in skill under skillsRoot
// (<skillsRoot>/<name>/SKILL.md). Existing files without ManagedMarker are
// preserved untouched so manually placed user overrides are never clobbered.
func SyncToDir(skillsRoot string) error {
	dir, _ := OverridesDir()
	return SyncFromOverridesDir(dir, skillsRoot)
}

// SyncFromOverridesDir is SyncToDir reading overrides from overridesDir.
func SyncFromOverridesDir(overridesDir, skillsRoot string) error {
	if skillsRoot == "" {
		return nil
	}
	for _, sk := range EffectiveFrom(overridesDir) {
		dir := filepath.Join(skillsRoot, sk.Name)
		dst := filepath.Join(dir, "SKILL.md")
		desired := ensureManagedContent(sk.Content)
		existing, err := os.ReadFile(dst)
		if err == nil {
			if bytes.Equal(existing, desired) {
				continue
			}
			if !bytes.Contains(existing, []byte(ManagedMarker)) {
				continue // manually placed skill without marker: leave untouched
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", dst, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		if err := fsutil.Write(dst, desired); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}
