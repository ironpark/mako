package daemon

import (
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transcript"
)

func conversationUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// optionalFlags remembers, per program and flag, whether the program's
// version has the flag.
var optionalFlags sync.Map

// withOptionalFlag adds flag to command when the program's --help lists it.
func withOptionalFlag(ctx context.Context, command []string, flag string) []string {
	key := command[0] + "\x00" + flag
	value, ok := optionalFlags.Load(key)
	if !ok {
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		help, err := exec.CommandContext(check, command[0], "--help").Output()
		value = err == nil && strings.Contains(string(help), flag)
		optionalFlags.Store(key, value)
	}
	if value.(bool) {
		return append(command, flag)
	}
	return command
}

// conversations follows the native conversation of every running agent
// session, including while the GUI is closed or in Terminal mode, and
// notices /new and /resume in the agent. One loop serves every session, so
// the processes it lists cost the same however many there are.
type conversations struct {
	mu       sync.Mutex
	sessions map[*session.Session]*conversation
	running  bool
}

// conversation is what the daemon keeps of one session's conversation.
type conversation struct {
	outputs uint64 // the session's output count when it was last checked
	chat    transcript.Cache
}

// trackConversation follows s's conversation for as long as it runs.
func (d *Daemon) trackConversation(s *session.Session) {
	if !transcript.Supported(s.Info().Agent) {
		return
	}
	c := &d.conversations
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions == nil {
		c.sessions = map[*session.Session]*conversation{}
	}
	c.sessions[s] = &conversation{}
	if !c.running {
		c.running = true
		go d.followConversations()
	}
}

// chatCache is the cache of s's conversation, or nil once it is not followed.
func (c *conversations) chatCache(s *session.Session) *transcript.Cache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conv, ok := c.sessions[s]; ok {
		return &conv.chat
	}
	return nil
}

// due drops the sessions that ended and returns the ones worth a look: not
// yet bound, or with output since the last look. A conversation changes only
// through the agent, and the agent shows it. It reports false, and stops the
// loop, once nothing is left to follow.
func (c *conversations) due() ([]*session.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var due []*session.Session
	for s, conv := range c.sessions {
		select {
		case <-s.Done():
			delete(c.sessions, s)
			continue
		default:
		}
		outputs := s.Outputs()
		if s.TranscriptPath() == "" || outputs != conv.outputs {
			conv.outputs = outputs
			due = append(due, s)
		}
	}
	if len(c.sessions) == 0 {
		c.running = false
		return nil, false
	}
	return due, true
}

func (d *Daemon) followConversations() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		due, ok := d.conversations.due()
		if !ok {
			return
		}
		if len(due) > 0 {
			d.checkConversations(due)
		}
		<-ticker.C
	}
}

// checkConversations binds each session to the log its process has open for
// writing, all of them from one process listing.
func (d *Daemon) checkConversations(sessions []*session.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	pids := make([]int, len(sessions))
	for i, s := range sessions {
		pids[i] = s.PID()
	}
	// Without a listing, every session falls back to its bound log.
	logs, _ := transcript.OpenLogs(ctx, pids)
	for _, s := range sessions {
		info := s.Info()
		id, path, err := transcript.Identify(info.Agent, info.Project, logs[s.PID()])
		if id == "" {
			// No writer found: keep the bound log while it is still the same
			// conversation, and only search the tree when it is not.
			if bound := s.TranscriptPath(); bound != "" && transcript.Verify(info, bound) == nil {
				continue
			}
			if info.ConversationID == "" {
				continue
			}
			path, err = transcript.Locate(ctx, info, s.TranscriptEnv())
			id = info.ConversationID
		}
		if err == nil && path != "" && (id != info.ConversationID || path != s.TranscriptPath()) {
			if err := d.bindConversation(s, id, path); err != nil {
				d.log.Warn("save conversation", "session", s.ID(), "err", err)
			}
		}
	}
}

func (d *Daemon) bindConversation(s *session.Session, id, path string) error {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	live, ok := d.sessions.get(s.ID())
	if !ok || live != s {
		return nil
	}
	previous, ok := d.saved[s.ID()]
	if !ok {
		return nil
	}
	next := previous
	next.Info.ConversationID = id
	next.TranscriptPath = path
	d.saved[s.ID()] = next
	if err := d.saveSessionsLocked(); err != nil {
		d.saved[s.ID()] = previous
		return err
	}
	s.SetConversation(id, path)
	return nil
}

// chatForSession always reads the attached process's conversation. A client
// cannot silently select unrelated history while typing into this PTY.
func (d *Daemon) chatForSession(ctx context.Context, s *session.Session) (protocol.Chat, error) {
	info := s.Info()
	if path := s.TranscriptPath(); path != "" {
		if cache := d.conversations.chatCache(s); cache != nil {
			return cache.ReadBound(info, path)
		}
		return transcript.ReadBound(info, path)
	}
	return transcript.Read(ctx, info, s.TranscriptEnv())
}

// claimedLocked is the conversations saved sessions other than id hold.
func (d *Daemon) claimedLocked(id string) map[string]bool {
	claimed := map[string]bool{}
	for other, entry := range d.saved {
		if other != id && entry.Info.ConversationID != "" {
			claimed[entry.Info.ConversationID] = true
		}
	}
	return claimed
}
