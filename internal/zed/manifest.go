// Package zed reads and installs extensions made for the Zed editor, so
// sanbo's editor takes its languages from them: the tree-sitter grammars
// they carry, compiled to wasm, and the queries and settings of each
// language. An extension is the directory Zed's store hands out, unpacked
// as it is.
package zed

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Manifest is an extension's extension.toml, as far as sanbo reads it.
type Manifest struct {
	ID            string   `toml:"id"`
	Name          string   `toml:"name"`
	Version       string   `toml:"version"`
	SchemaVersion int      `toml:"schema_version"`
	Description   string   `toml:"description"`
	Repository    string   `toml:"repository"`
	Authors       []string `toml:"authors"`
	// Languages are the extension's language directories, relative to it;
	// without them, every directory under languages/ is one.
	Languages       []string                  `toml:"languages"`
	Grammars        map[string]Grammar        `toml:"grammars"`
	LanguageServers map[string]LanguageServer `toml:"language_servers"`
	// ContextServers are the MCP servers the extension's wasm starts, by
	// ID; Zed's manifest says nothing more of them.
	ContextServers map[string]struct{} `toml:"context_servers"`
	// Capabilities are what the extension's wasm asks to be allowed.
	Capabilities []Capability `toml:"capabilities"`
	Lib          struct {
		Kind    string `toml:"kind"`
		Version string `toml:"version"` // of the extension API its wasm is built against
	} `toml:"lib"`
}

// Capability is a thing an extension's wasm may do. Of Zed's kinds, sanbo
// reads "process:exec": a command, or "*" for any, and its arguments,
// each a value or "*" for any one, the last "**" for any rest.
type Capability struct {
	Kind    string   `toml:"kind"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
}

// AllowsExec reports whether the manifest asks to run command with args.
func (m Manifest) AllowsExec(command string, args []string) bool {
	for _, c := range m.Capabilities {
		if c.Kind == "process:exec" && (c.Command == "*" || c.Command == command) && argsMatch(c.Args, args) {
			return true
		}
	}
	return false
}

func argsMatch(patterns, args []string) bool {
	for i, p := range patterns {
		if p == "**" && i == len(patterns)-1 {
			return true
		}
		if i >= len(args) || p != "*" && p != args[i] {
			return false
		}
	}
	return len(args) == len(patterns)
}

// ContextServerIDs lists the extension's context servers, sorted.
func (m Manifest) ContextServerIDs() []string {
	ids := make([]string, 0, len(m.ContextServers))
	for id := range m.ContextServers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Grammar is where a grammar the extension carries was built from.
type Grammar struct {
	Repository string `toml:"repository"`
	Rev        string `toml:"rev"`
	Path       string `toml:"path"`
}

// LanguageServer is a language server the extension's wasm starts.
type LanguageServer struct {
	Name        string            `toml:"name"`
	Language    string            `toml:"language"`
	Languages   []string          `toml:"languages"`
	LanguageIDs map[string]string `toml:"language_ids"`
}

// ServesLanguage reports whether the server is for the language named.
func (s LanguageServer) ServesLanguage(name string) bool {
	return s.Language == name || slices.Contains(s.Languages, name)
}

// LanguageConfig is a language's config.toml.
type LanguageConfig struct {
	Name         string   `toml:"name"`
	Grammar      string   `toml:"grammar"`
	PathSuffixes []string `toml:"path_suffixes"`
	LineComments []string `toml:"line_comments"`
	LineComment  string   `toml:"line_comment"` // the older, single form
	// FirstLinePattern is a regular expression a file's first line
	// matches, as a shebang does, when its name says nothing.
	FirstLinePattern string `toml:"first_line_pattern"`
	TabSize          int    `toml:"tab_size"`
	HardTabs         bool   `toml:"hard_tabs"`
}

// Comment is the line comment the language writes.
func (c LanguageConfig) Comment() string {
	if len(c.LineComments) > 0 {
		return c.LineComments[0]
	}
	return c.LineComment
}

// readManifest reads the extension.toml in dir.
func readManifest(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, "extension.toml"))
	if err != nil {
		return m, err
	}
	if err := toml.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("zed: %s: %w", filepath.Join(dir, "extension.toml"), err)
	}
	if m.ID == "" {
		return m, fmt.Errorf("zed: %s: no id", dir)
	}
	return m, nil
}

// languageDirs are the extension's language directories, absolute.
func (m Manifest) languageDirs(dir string) []string {
	var out []string
	if len(m.Languages) > 0 {
		for _, l := range m.Languages {
			out = append(out, filepath.Join(dir, filepath.FromSlash(l)))
		}
		return out
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "languages"))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, "languages", e.Name()))
		}
	}
	return out
}

// readLanguageConfig reads a language directory's config.toml.
func readLanguageConfig(dir string) (LanguageConfig, error) {
	var c LanguageConfig
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("zed: %s: %w", filepath.Join(dir, "config.toml"), err)
	}
	if c.Name == "" {
		return c, fmt.Errorf("zed: %s: a language without a name", dir)
	}
	return c, nil
}

// MatchesName reports whether a file name is one of the language's: its
// whole name or its extension is a path suffix of the language's.
func (c LanguageConfig) MatchesName(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range c.PathSuffixes {
		s = strings.ToLower(s)
		if lower == s || strings.HasSuffix(lower, "."+s) {
			return true
		}
	}
	return false
}
