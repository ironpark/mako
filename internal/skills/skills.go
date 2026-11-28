// Package skills embeds Mako's unified built-in skill (mako, structured across
// multiple topic folders) and registers it into each supported agent's skills
// directory so agents discover it natively via slash commands and progressive
// skill loading without overwriting user-customized copies.
//
// Users can also customize SKILL.md or any reference file inside Mako (stored
// under <configDir>/skills/<name>/<relPath>). Effective() resolves overrides on
// top of the embedded defaults, and SyncToDir distributes the full folder tree
// to every agent skills directory managed by Mako.
package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/homefs"
	"github.com/ironpark/mako/pkg/fsutil"
)

// ManagedMarker tags skill files written by Mako so Mako can refresh them on
// upgrade or after a user edits a built-in skill in Mako, while leaving any
// manually placed skill file (without the marker) untouched.
const ManagedMarker = "<!-- mako-managed-skill -->"

// SupportedAgents lists the agent drivers whose skill directories Mako syncs.
var SupportedAgents = []string{"agy", "claude", "codex", "pi", "omp", "omo", "opencode"}

// LegacyBuiltinNames are former built-in skill folders superseded by the
// unified "mako" skill. Managed copies (tagged with ManagedMarker) are pruned
// automatically during SyncFromOverridesDir while user-authored copies without
// the marker are preserved untouched.
var LegacyBuiltinNames = []string{"mako-plugin", "mako-template"}

//go:embed builtin
var builtinFS embed.FS

// SkillFile describes a single file inside a built-in skill's folder tree.
type SkillFile struct {
	Path       string // slash-separated path relative to skill root, e.g. "SKILL.md", "cli/projects-and-agents.md"
	Content    []byte
	Customized bool
}

// Skill describes one built-in skill (and all files in its folder tree).
type Skill struct {
	Name        string
	Description string
	Content     []byte      // SKILL.md content
	Files       []SkillFile // SKILL.md first, followed by subfiles sorted by Path
	Customized  bool        // true if SKILL.md or any subfile is customized
}

// File returns the SkillFile at slash-separated relPath, if present.
func (s Skill) File(relPath string) (SkillFile, bool) {
	relPath = normalizeRelPath(relPath)
	for _, f := range s.Files {
		if f.Path == relPath {
			return f, true
		}
	}
	return SkillFile{}, false
}

// Frontmatter extracts the YAML frontmatter name and description fields from a
// SKILL.md document, supporting both single-line values and folded/literal
// multiline YAML block scalars (>, >-, |, |-).
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
	lines := strings.Split(rest[:end], "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if v == ">" || v == ">-" || v == "|" || v == "|-" {
			var parts []string
			for i+1 < len(lines) {
				next := lines[i+1]
				if len(next) == 0 || (next[0] != ' ' && next[0] != '\t') {
					break
				}
				i++
				parts = append(parts, strings.TrimSpace(next))
			}
			v = strings.Join(parts, " ")
		} else {
			v = strings.Trim(v, `"'`)
		}
		switch k {
		case "name":
			name = v
		case "description":
			description = v
		}
	}
	return name, description
}

func normalizeRelPath(relPath string) string {
	clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(relPath), `\`, "/"))
	if clean == "." || clean == "" {
		return "SKILL.md"
	}
	return strings.TrimPrefix(clean, "./")
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
		root := path.Join("builtin", e.Name())
		var files []SkillFile
		var rootContent []byte
		_ = fs.WalkDir(builtinFS, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(p, root+"/")
			if rel == "" || strings.HasPrefix(path.Base(rel), ".") {
				return nil
			}
			data, readErr := builtinFS.ReadFile(p)
			if readErr != nil {
				return nil
			}
			if rel == "SKILL.md" {
				rootContent = data
			}
			files = append(files, SkillFile{Path: rel, Content: data})
			return nil
		})
		if len(rootContent) == 0 {
			continue
		}
		slices.SortFunc(files, func(a, b SkillFile) int {
			if a.Path == "SKILL.md" && b.Path != "SKILL.md" {
				return -1
			}
			if b.Path == "SKILL.md" && a.Path != "SKILL.md" {
				return 1
			}
			return strings.Compare(a.Path, b.Path)
		})
		_, desc := Frontmatter(rootContent)
		out = append(out, Skill{
			Name:        e.Name(),
			Description: desc,
			Content:     rootContent,
			Files:       files,
		})
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

// BuiltinFile returns the embedded file at relPath within built-in skill name.
func BuiltinFile(name, relPath string) (SkillFile, bool) {
	sk, ok := BuiltinByName(name)
	if !ok {
		return SkillFile{}, false
	}
	return sk.File(relPath)
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
	return OverrideFilePath(name, "SKILL.md")
}

// OverrideFilePath returns the on-disk override path (<configDir>/skills/<name>/<relPath>)
// for a file belonging to a built-in skill.
func OverrideFilePath(name, relPath string) (string, error) {
	relPath = normalizeRelPath(relPath)
	if _, ok := BuiltinFile(name, relPath); !ok {
		return "", fmt.Errorf("unknown built-in skill file %q/%q", name, relPath)
	}
	dir, err := OverridesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name, filepath.FromSlash(relPath)), nil
}

// Effective returns every built-in skill with any user overrides from
// <configDir>/skills/<name>/... applied on top.
func Effective() []Skill {
	dir, _ := OverridesDir()
	return EffectiveFrom(dir)
}

// EffectiveFrom returns every built-in skill with overrides read from
// <overridesDir>/<name>/... applied on top.
func EffectiveFrom(overridesDir string) []Skill {
	list := Builtin()
	if overridesDir == "" {
		return list
	}
	for i := range list {
		for j := range list[i].Files {
			rel := list[i].Files[j].Path
			p := filepath.Join(overridesDir, list[i].Name, filepath.FromSlash(rel))
			if data, err := os.ReadFile(p); err == nil {
				list[i].Files[j].Content = data
				list[i].Files[j].Customized = true
				list[i].Customized = true
				if rel == "SKILL.md" {
					list[i].Content = data
					if _, desc := Frontmatter(data); desc != "" {
						list[i].Description = desc
					}
				}
			}
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

// SaveOverride writes a customized version of a built-in skill's SKILL.md to
// <configDir>/skills/<name>/SKILL.md.
func SaveOverride(name string, content []byte) error {
	return SaveOverrideFile(name, "SKILL.md", content)
}

// SaveOverrideFile writes a customized version of a file inside a built-in skill
// to <configDir>/skills/<name>/<relPath>.
func SaveOverrideFile(name, relPath string, content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return errors.New("skill content cannot be empty")
	}
	dst, err := OverrideFilePath(name, relPath)
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

// ResetOverride removes all user overrides for a built-in skill, restoring the
// embedded defaults across every file in the skill.
func ResetOverride(name string) error {
	sk, ok := BuiltinByName(name)
	if !ok {
		return fmt.Errorf("unknown built-in skill %q", name)
	}
	for _, f := range sk.Files {
		if err := ResetOverrideFile(name, f.Path); err != nil {
			return err
		}
	}
	return nil
}

// ResetOverrideFile removes the user override for a single file inside a
// built-in skill, cleaning up any now-empty parent directories up to the skill root.
func ResetOverrideFile(name, relPath string) error {
	dst, err := OverrideFilePath(name, relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dst, err)
	}
	overridesDir, err := OverridesDir()
	if err == nil {
		pruneEmptyDirsUpTo(filepath.Dir(dst), overridesDir)
	}
	return nil
}

func pruneEmptyDirsUpTo(dir, stop string) {
	stop = filepath.Clean(stop)
	for cur := filepath.Clean(dir); cur != stop && strings.HasPrefix(cur, stop+string(filepath.Separator)); cur = filepath.Dir(cur) {
		if err := os.Remove(cur); err != nil {
			break
		}
	}
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
		if dir := get("XDG_CONFIG_HOME"); dir != "" {
			return []string{filepath.Join(dir, "opencode", "skills")}
		}
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
// (<skillsRoot>/<name>/...). Existing files without ManagedMarker are preserved
// untouched so manually placed user overrides are never clobbered.
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
		for _, f := range sk.Files {
			dst := filepath.Join(skillsRoot, sk.Name, filepath.FromSlash(f.Path))
			desired := ensureManagedContent(f.Content)
			existing, err := homefs.ReadFile(dst)
			if err == nil {
				if bytes.Equal(existing, desired) {
					continue
				}
				if !bytes.Contains(existing, []byte(ManagedMarker)) {
					continue // manually placed skill file without marker: leave untouched
				}
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("read %s: %w", dst, err)
			}
			if err := homefs.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
			}
			if err := homefs.Write(dst, desired); err != nil {
				return fmt.Errorf("write %s: %w", dst, err)
			}
		}
	}
	// Remove superseded managed legacy skill files (e.g. mako-plugin, mako-template)
	// while leaving user-authored copies without ManagedMarker untouched.
	for _, legacy := range LegacyBuiltinNames {
		if _, active := BuiltinByName(legacy); active {
			continue
		}
		legacyFile := filepath.Join(skillsRoot, legacy, "SKILL.md")
		existing, err := homefs.ReadFile(legacyFile)
		if err != nil || !bytes.Contains(existing, []byte(ManagedMarker)) {
			continue
		}
		if homefs.Remove(legacyFile) == nil {
			_ = homefs.Remove(filepath.Dir(legacyFile))
		}
	}
	return nil
}
