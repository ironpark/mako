package session

import (
	"os"
	"strings"

	"github.com/ironpark/sanbo/internal/transcript"
)

// TranscriptEnv returns only the paths needed to find this process's native
// conversation logs, including profile overrides. Credentials stay private.
func (s *Session) TranscriptEnv() map[string]string { return transcriptEnv(s.cmd.Env) }

// TranscriptEnvFrom is TranscriptEnv for a launch that has not started yet:
// the daemon's environment with the launch's overrides.
func TranscriptEnvFrom(overrides map[string]string) map[string]string {
	out := transcriptEnv(os.Environ())
	for key, value := range overrides {
		if transcript.EnvKey(key) {
			out[key] = value
		}
	}
	return out
}

func transcriptEnv(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && transcript.EnvKey(key) {
			out[key] = value
		}
	}
	return out
}

// PID identifies the launched process while its session is alive.
func (s *Session) PID() int { return s.cmd.Process.Pid }

// Outputs counts the program's writes to the terminal, so a watcher can tell
// a session that did something since it last looked from an idle one.
func (s *Session) Outputs() uint64 { return s.outputs.Load() }

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
