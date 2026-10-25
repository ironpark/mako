package session

import (
	"os"
	"strings"
)

// TranscriptEnv returns only the paths needed to find this process's native
// conversation logs, including profile overrides. Credentials stay private.
func (s *Session) TranscriptEnv() map[string]string { return transcriptEnv(s.cmd.Env) }

// TranscriptEnvFrom is TranscriptEnv for a launch that has not started yet:
// the daemon's environment with the launch's overrides.
func TranscriptEnvFrom(overrides map[string]string) map[string]string {
	out := transcriptEnv(os.Environ())
	for key, value := range overrides {
		if transcriptEnvKey(key) {
			out[key] = value
		}
	}
	return out
}

func transcriptEnv(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && transcriptEnvKey(key) {
			out[key] = value
		}
	}
	return out
}

func transcriptEnvKey(key string) bool {
	return key == "HOME" || key == "CLAUDE_CONFIG_DIR" || key == "CODEX_HOME"
}

// PID identifies the launched process while its session is alive.
func (s *Session) PID() int { return s.cmd.Process.Pid }

func (s *Session) SetConversation(id, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversationID, s.transcriptPath = id, path
}

func (s *Session) TranscriptPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptPath
}
