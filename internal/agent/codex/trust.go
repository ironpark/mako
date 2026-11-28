package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/homefs"
)

var trustMu sync.Mutex

// configFile returns Codex's config.toml path, honoring CODEX_HOME and HOME.
func configFile(env map[string]string) (string, error) {
	if dir := strings.TrimSpace(agent.LookupEnv(env, place.RootEnv)); dir != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	home, err := agent.LaunchHome(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, place.HomeDir, "config.toml"), nil
}

type trustConfig struct {
	Projects map[string]struct {
		TrustLevel string `toml:"trust_level"`
	} `toml:"projects"`
}

// TrustDirs records each directory in dirs as trusted in Codex's config.toml
// ([projects."<path>"] trust_level = "trusted") so Codex does not prompt on
// folder trust when launched in new projects or with additional directories.
// Existing comments, formatting, and unrelated settings are preserved, and the
// file is not rewritten when every directory is already trusted.
func (*Agent) TrustDirs(l *agent.Launch, dirs []string) error {
	canon := agent.CanonicalDirs(dirs)
	if len(canon) == 0 {
		return nil
	}
	var env map[string]string
	if l != nil {
		env = l.Env
	}
	path, err := configFile(env)
	if err != nil {
		return err
	}
	return writeTrustedProjects(path, canon)
}

func writeTrustedProjects(path string, dirs []string) error {
	trustMu.Lock()
	defer trustMu.Unlock()

	body, err := homefs.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	bom := []byte("\xef\xbb\xbf")
	hasBOM := bytes.HasPrefix(body, bom)
	content := string(bytes.TrimPrefix(body, bom))

	var parsed trustConfig
	if strings.TrimSpace(content) != "" {
		if _, err := toml.Decode(content, &parsed); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}

	var toUpdate, toAppend []string
	for _, dir := range dirs {
		entry, exists := parsed.Projects[dir]
		if exists && entry.TrustLevel == "trusted" {
			continue
		}
		if exists {
			toUpdate = append(toUpdate, dir)
		} else {
			toAppend = append(toAppend, dir)
		}
	}
	if len(toUpdate) == 0 && len(toAppend) == 0 {
		return nil
	}

	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	var lines []string
	if strings.TrimSpace(normalized) != "" {
		trimmed := strings.TrimSuffix(normalized, "\n")
		lines = strings.Split(trimmed, "\n")
	}

	for _, dir := range toUpdate {
		var ok bool
		lines, ok = updateExistingProjectTrust(lines, dir)
		if !ok {
			return fmt.Errorf("update trust_level for %q in %s: unsupported table syntax", dir, path)
		}
	}

	for _, dir := range toAppend {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines,
			"[projects."+tomlString(dir)+"]",
			`trust_level = "trusted"`,
		)
	}

	updated := strings.Join(lines, eol) + eol
	var verify trustConfig
	if _, err := toml.Decode(updated, &verify); err != nil {
		return fmt.Errorf("validate updated %s: %w", path, err)
	}
	for _, dir := range dirs {
		if verify.Projects[dir].TrustLevel != "trusted" {
			return fmt.Errorf("validate updated %s: %q not trusted after edit", path, dir)
		}
	}

	var out []byte
	if hasBOM {
		out = append(out, bom...)
	}
	out = append(out, updated...)
	if err := homefs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return homefs.Write(path, out)
}

// updateExistingProjectTrust updates an existing project entry for dir to
// trust_level = "trusted" in place while preserving comments and other keys.
func updateExistingProjectTrust(lines []string, dir string) ([]string, bool) {
	// First, try standard [projects."<dir>"] table section.
	for i, line := range lines {
		key, ok := headerProjectKey(line)
		if !ok || key != dir {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if isTableHeader(lines[j]) {
				end = j
				break
			}
		}
		for j := i + 1; j < end; j++ {
			if next, replaced := replaceTrustLevelLine(lines[j]); replaced {
				lines[j] = next
				return lines, true
			}
		}
		// Table exists without trust_level: insert right after the header.
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:i+1]...)
		out = append(out, `trust_level = "trusted"`)
		out = append(out, lines[i+1:]...)
		return out, true
	}

	// Second, handle dotted-key assignments at root (`projects."<dir>".trust_level = ...`)
	// or directly inside a `[projects]` table (`"<dir>".trust_level = ...`).
	section := ""
	for i, line := range lines {
		if isTableHeader(line) {
			if isProjectsTableHeader(line) {
				section = "projects"
			} else {
				section = "other"
			}
			continue
		}
		if section == "other" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		prefix := ""
		if section == "projects" {
			prefix = "[projects]\n"
		}
		var probe trustConfig
		if _, err := toml.Decode(prefix+line+"\n", &probe); err != nil {
			continue
		}
		if entry, exists := probe.Projects[dir]; exists && entry.TrustLevel != "" {
			if next, ok := replaceStringLiteralValue(line, entry.TrustLevel, "trusted"); ok {
				lines[i] = next
				return lines, true
			}
		}
	}
	return lines, false
}

// isTableHeader reports whether line is a TOML table or array-of-tables header.
func isTableHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "[")
}

// headerProjectKey returns the project path key if line is a TOML table header
// of the exact form `[projects.<key>]`.
func headerProjectKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	var probe struct {
		Projects map[string]map[string]any `toml:"projects"`
	}
	if _, err := toml.Decode(trimmed+"\n_mako_probe = 1\n", &probe); err != nil {
		return "", false
	}
	if len(probe.Projects) != 1 {
		return "", false
	}
	for k, m := range probe.Projects {
		if v, ok := m["_mako_probe"]; ok && v == int64(1) {
			return k, true
		}
	}
	return "", false
}

func isProjectsTableHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return false
	}
	var probe struct {
		Projects map[string]any `toml:"projects"`
	}
	if _, err := toml.Decode(trimmed+"\n_mako_probe = 1\n", &probe); err != nil {
		return false
	}
	v, ok := probe.Projects["_mako_probe"]
	return ok && v == int64(1)
}

// replaceTrustLevelLine rewrites a `trust_level = ...` assignment line to
// `trust_level = "trusted"`, preserving indentation and any trailing comment.
func replaceTrustLevelLine(line string) (string, bool) {
	indentLen := len(line) - len(strings.TrimLeft(line, " \t"))
	indent := line[:indentLen]
	rest := line[indentLen:]
	afterKey, ok := strings.CutPrefix(rest, "trust_level")
	if !ok {
		return line, false
	}
	afterKeyTrim := strings.TrimLeft(afterKey, " \t")
	if !strings.HasPrefix(afterKeyTrim, "=") {
		return line, false
	}
	comment := trailingComment(afterKeyTrim[1:])
	if comment != "" {
		return indent + `trust_level = "trusted"` + comment, true
	}
	return indent + `trust_level = "trusted"`, true
}

// replaceStringLiteralValue replaces the first TOML string literal matching oldVal
// after an `=` sign on line with `"<newVal>"`.
func replaceStringLiteralValue(line, oldVal, newVal string) (string, bool) {
	for _, needle := range []string{tomlString(oldVal), "'" + oldVal + "'"} {
		idx := strings.LastIndex(line, needle)
		if idx >= 0 {
			return line[:idx] + tomlString(newVal) + line[idx+len(needle):], true
		}
	}
	return line, false
}

// trailingComment returns any `#...` comment (including preceding spaces) on a
// single-line TOML value expression, ignoring `#` characters inside quotes.
func trailingComment(expr string) string {
	inBasic := false
	inLiteral := false
	escaped := false
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case inBasic:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inBasic = false
			}
		case inLiteral:
			if c == '\'' {
				inLiteral = false
			}
		default:
			switch c {
			case '"':
				inBasic = true
			case '\'':
				inLiteral = true
			case '#':
				start := i
				for start > 0 && (expr[start-1] == ' ' || expr[start-1] == '\t') {
					start--
				}
				return expr[start:]
			}
		}
	}
	return ""
}
