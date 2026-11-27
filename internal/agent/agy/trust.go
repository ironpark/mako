package agy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/pkg/fsutil"
)

var trustMu sync.Mutex

// settingsFile returns agy's global settings.json, honoring HOME in env when set.
func settingsFile(env map[string]string) (string, error) {
	home, err := agent.LaunchHome(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), nil
}

// TrustDirs adds each directory in dirs to agy's trustedWorkspaces list in
// settings.json so agy starts without prompting on workspace trust. Existing
// settings and trustedWorkspaces entries are preserved, and the file is left
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
	path, err := settingsFile(env)
	if err != nil {
		return err
	}
	return writeTrustedWorkspaces(path, canon)
}

func writeTrustedWorkspaces(path string, dirs []string) error {
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

	var trusted []string
	if raw := bytes.TrimSpace(doc["trustedWorkspaces"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return fmt.Errorf("parse trustedWorkspaces in %s: %w", path, err)
		}
	}

	have := make(map[string]bool, len(trusted))
	for _, p := range trusted {
		have[p] = true
		if cleaned := filepath.Clean(p); cleaned != p {
			have[cleaned] = true
		}
	}

	changed := false
	for _, dir := range dirs {
		if have[dir] {
			continue
		}
		have[dir] = true
		trusted = append(trusted, dir)
		changed = true
	}
	if !changed {
		return nil
	}

	encoded, err := json.Marshal(trusted)
	if err != nil {
		return err
	}
	doc["trustedWorkspaces"] = encoded
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.Write(path, append(out, '\n'))
}
