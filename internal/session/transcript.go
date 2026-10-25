package session

import "strings"

// TranscriptEnv returns only the paths needed to find this process's native
// conversation logs, including profile overrides. Credentials stay private.
func (s *Session) TranscriptEnv() map[string]string {
	out := map[string]string{}
	for _, entry := range s.cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "HOME" || key == "CLAUDE_CONFIG_DIR" || key == "CODEX_HOME") {
			out[key] = value
		}
	}
	return out
}
