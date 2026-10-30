package session

import (
	"time"

	"github.com/ironpark/mako/internal/transcript"
)

// TranscriptEnv returns only the paths needed to find this process's native
// conversation logs, including profile overrides.
func (s *Session) TranscriptEnv() map[string]string { return transcript.Env(s.env, nil) }

// PID identifies the launched process while its session is alive.
func (s *Session) PID() int { return s.proc.pid() }

// Outputs counts the program's writes to the terminal, so a watcher can tell
// a session that did something since it last looked from an idle one.
func (s *Session) Outputs() uint64 { return s.outputs.Load() }

func (s *Session) SetConversation(id, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conversationID != id {
		s.agentsWorking = false
		s.childHooks, s.childLogs = nil, nil
		s.lastMessage = time.Time{}
	}
	s.conversationID, s.transcriptPath = id, path
}

func (s *Session) TranscriptPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptPath
}
