package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/pkg/fsutil"
)

var trustMu sync.Mutex

// stateFile returns Claude Code's state JSON file (.claude.json), which lives
// inside $CLAUDE_CONFIG_DIR when that variable is set and under $HOME otherwise.
func stateFile(env map[string]string) (string, error) {
	if dir := strings.TrimSpace(agent.LookupEnv(env, place.RootEnv)); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := agent.LaunchHome(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// TrustDirs marks each directory in dirs as trusted in Claude Code's state file
// (projects[<path>].hasTrustDialogAccepted = true) so neither the working
// directory nor any --add-dir directories block startup on interactive trust
// prompts. Existing fields in .claude.json are preserved, and the file is left
// untouched if every directory is already trusted.
func (*Agent) TrustDirs(l *agent.Launch, dirs []string) error {
	canon := agent.CanonicalDirs(dirs)
	if len(canon) == 0 {
		return nil
	}
	var env map[string]string
	if l != nil {
		env = l.Env
	}
	path, err := stateFile(env)
	if err != nil {
		return err
	}
	return writeTrustedProjects(path, canon)
}

func writeTrustedProjects(path string, dirs []string) error {
	trustMu.Lock()
	defer trustMu.Unlock()

	doc := map[string]json.RawMessage{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, &doc); err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			if doc == nil {
				return fmt.Errorf("parse %s: expected JSON object", path)
			}
		}
	case !os.IsNotExist(err):
		return err
	}

	projects := map[string]json.RawMessage{}
	if raw := bytes.TrimSpace(doc["projects"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return fmt.Errorf("parse projects in %s: %w", path, err)
		}
		if projects == nil {
			return fmt.Errorf("parse projects in %s: expected JSON object", path)
		}
	}

	changed := false
	for _, dir := range dirs {
		entry := map[string]json.RawMessage{}
		if raw := bytes.TrimSpace(projects[dir]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("parse project %q in %s: %w", dir, path, err)
			}
			if entry == nil {
				return fmt.Errorf("parse project %q in %s: expected JSON object", dir, path)
			}
		}
		if bytes.Equal(bytes.TrimSpace(entry["hasTrustDialogAccepted"]), []byte("true")) {
			continue
		}
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		projects[dir] = encoded
		changed = true
	}
	if !changed {
		return nil
	}

	encodedProjects, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	doc["projects"] = encodedProjects
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.Write(path, append(out, '\n'))
}
