package session

import (
	"time"

	"github.com/ironpark/mako/internal/transcript"
)

// TranscriptEnv returns only the paths needed to find this process's native
// conversation logs, including profile overrides.
func (s *Session) TranscriptEnv() map[string]string {
	s.mu.Lock()
	env := s.env
	s.mu.Unlock()
	return transcript.Env(env, nil)
}

// PID identifies the launched process while its session is alive.
func (s *Session) PID() int {
	if p := s.currentProc(); p != nil {
		return p.pid()
	}
	return 0
}

// Outputs counts the program's writes to the terminal, so a watcher can tell
// a session that did something since it last looked from an idle one.
func (s *Session) Outputs() uint64 { return s.outputs.Load() }

func (s *Session) SetConversation(id, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.conversationID
	s.conversationID, s.transcriptPath = id, path
	if prev != id {
		s.agentsWorking = false
		s.childHooks, s.childLogs = nil, nil
		s.lastMessage = time.Time{}
		// Only clear on a switch between two bound conversations (e.g. /clear,
		// /resume, or thread switch). Initial binding from "" -> id must not
		// wipe an auth error reported on the session's first turn before the
		// transcript file materialized on disk.
		if prev != "" && s.authError != nil {
			s.authError = nil
			s.authErrorSeq = max(time.Now().UnixNano(), s.authErrorSeq+1)
			s.recomputeLocked(time.Now())
		}
		if s.goal != nil && (s.goal.GetSourceSessionId() == "" || s.goal.GetSourceSessionId() != id) {
			s.goal = nil
			s.changedLocked()
		}
	}
}

func (s *Session) TranscriptPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptPath
}
