package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transcript"
)

// savedSession keeps launch inputs, not resolved commands containing temporary
// MCP paths. Profiles and extensions are resolved again when the user attaches.
type savedSession struct {
	Info           protocol.SessionInfo `json:"info"`
	Request        protocol.OpenSession `json:"request"`
	TranscriptPath string               `json:"transcript_path,omitempty"`
}

type sessionFile struct {
	NodeID   string                  `json:"node_id"`
	Sessions map[string]savedSession `json:"sessions"`
}

// loadSessions restores metadata only. No agent process is started here.
func (d *Daemon) loadSessions(dir string) error {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	path := filepath.Join(dir, "sessions.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	saved := sessionFile{NodeID: d.cfg.NodeID}
	if err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("read saved sessions: %w", err)
		}
		if saved.NodeID != d.cfg.NodeID {
			return fmt.Errorf("saved sessions belong to node %q", saved.NodeID)
		}
	}
	if saved.Sessions == nil {
		saved.Sessions = map[string]savedSession{}
	}
	for id, entry := range saved.Sessions {
		entry.Info.ID, entry.Info.NodeID = id, d.cfg.NodeID
		entry.Info.State, entry.Info.Busy, entry.Info.ExitCode = protocol.StateStopped, false, nil
		saved.Sessions[id] = entry
	}
	d.saved, d.sessionPath = saved.Sessions, path
	return nil
}

// saveSessionsLocked atomically replaces the node's private launch history.
func (d *Daemon) saveSessionsLocked() error {
	if d.sessionPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(sessionFile{NodeID: d.cfg.NodeID, Sessions: d.saved}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.sessionPath), ".sessions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), d.sessionPath)
}

func (d *Daemon) listSessions() []protocol.SessionInfo {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	out := d.sessions.list()
	live := map[string]bool{}
	for _, info := range out {
		live[info.ID] = true
	}
	for id, entry := range d.saved {
		if !live[id] {
			out = append(out, entry.Info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// startSessionLocked serializes activation, so simultaneous windows cannot
// launch the same restored session twice. Failed starts retain the saved entry.
func (d *Daemon) startSessionLocked(ctx context.Context, id string, req protocol.OpenSession) (*session.Session, error) {
	launch, err := agentspec.Resolve(req.Agent, req.Profile, req.Args, req.Env, d.agents())
	if err != nil {
		return nil, err
	}
	if req.Project == "" {
		req.Project, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	project, err := filepath.Abs(req.Project)
	if err != nil {
		return nil, err
	}
	req.Project = project

	previous, existed := d.saved[id]
	conversationID, transcriptPath, err := d.conversationFor(ctx, id, req, &launch, previous, existed)
	if err != nil {
		return nil, err
	}
	command, cleanup := d.withContextServers(ctx, launch, id)
	s, err := session.Start(session.Spec{ID: id, NodeID: d.cfg.NodeID, Agent: req.Agent, Profile: req.Profile,
		Project: req.Project, Command: command, Env: launch.Env, Cols: req.Cols, Rows: req.Rows})
	if err != nil {
		cleanup()
		return nil, err
	}
	s.SetConversation(conversationID, transcriptPath)
	if existed {
		s.SetName(previous.Info.Name)
	}
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	info := s.Info()
	// Commands can embed ephemeral MCP config and secrets; only persist inputs.
	info.Command = nil
	d.saved[id] = savedSession{Info: info, Request: req, TranscriptPath: transcriptPath}
	if err := d.saveSessionsLocked(); err != nil {
		if existed {
			d.saved[id] = previous
		} else {
			delete(d.saved, id)
		}
		_ = s.Close()
		cleanup()
		return nil, fmt.Errorf("save session: %w", err)
	}
	d.sessions.add(s)
	go d.trackConversation(s)
	d.log.Info("session started", "id", id, "agent", req.Agent, "project", req.Project)
	go func() { <-s.Done(); cleanup() }()
	return s, nil
}

// conversationFor binds a native agent's command to the conversation the
// session should resume, returning the ID and log path to persist.
func (d *Daemon) conversationFor(ctx context.Context, id string, req protocol.OpenSession, launch *agentspec.Launch, previous savedSession, existed bool) (string, string, error) {
	conversationID, transcriptPath := previous.Info.ConversationID, previous.TranscriptPath
	native := nativeAgent(req.Agent, launch.Command)
	if existed && conversationID != "" && !native {
		return "", "", fmt.Errorf("cannot resume %s through a custom wrapper; configure the native %s command", conversationID, req.Agent)
	}
	if !native {
		return conversationID, transcriptPath, nil
	}
	if conversationID == "" {
		conversationID = transcript.ArgumentID(req.Agent, launch.Command)
	}
	if existed && conversationID == "" {
		claimed := map[string]bool{}
		for other, entry := range d.saved {
			if other != id && entry.Info.ConversationID != "" {
				claimed[entry.Info.ConversationID] = true
			}
		}
		var err error
		conversationID, transcriptPath, err = transcript.Recover(ctx, previous.Info, session.TranscriptEnvFrom(launch.Env), claimed)
		if err != nil {
			return "", "", err
		}
	}
	if existed && conversationID != "" {
		// Resolve profile paths again, but keep the original native identity.
		lookup := previous.Info
		lookup.ConversationID = conversationID
		if transcriptPath == "" {
			var err error
			transcriptPath, err = transcript.Locate(ctx, lookup, session.TranscriptEnvFrom(launch.Env))
			if err != nil {
				return "", "", err
			}
		}
		if transcriptPath != "" {
			if err := transcript.Verify(lookup, transcriptPath); err != nil {
				return "", "", fmt.Errorf("cannot resume saved conversation: %w", err)
			}
			launch.Command = resumeCommand(req.Agent, launch.Command, conversationID)
		} else if req.Agent == "claude" {
			// A newly opened Claude window may not have written its first turn yet.
			launch.Command = append(launch.Command, "--session-id", conversationID)
		} else {
			return "", "", fmt.Errorf("saved conversation %s is unavailable; refusing to start a new conversation", conversationID)
		}
	} else if req.Agent == "claude" && conversationID == "" && !hasConversationSelector(launch.Command) {
		conversationID = conversationUUID()
		launch.Command = append(launch.Command, "--session-id", conversationID)
	}
	if req.Agent == "codex" {
		launch.Command = isolatedCodex(ctx, launch.Command)
	}
	return conversationID, transcriptPath, nil
}

func (d *Daemon) openSession(ctx context.Context, req protocol.OpenSession) (*session.Session, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	return d.startSessionLocked(ctx, newSessionID(), req)
}

func (d *Daemon) attachSession(ctx context.Context, id string) (*session.Session, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	if s, ok := d.sessions.get(id); ok {
		return s, nil
	}
	saved, ok := d.saved[id]
	if !ok {
		return nil, fmt.Errorf("unknown session %q", id)
	}
	return d.startSessionLocked(ctx, id, saved.Request)
}

func (d *Daemon) renameSession(id, name string) (protocol.SessionInfo, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	entry, ok := d.saved[id]
	s, live := d.sessions.get(id)
	if !ok && !live {
		return protocol.SessionInfo{}, fmt.Errorf("unknown session %q", id)
	}
	if ok {
		previous := entry
		entry.Info.Name = name
		d.saved[id] = entry
		if err := d.saveSessionsLocked(); err != nil {
			d.saved[id] = previous
			return protocol.SessionInfo{}, err
		}
	}
	if live {
		s.SetName(name)
		return s.Info(), nil
	}
	return entry.Info, nil
}

func (d *Daemon) closeSession(id string) error {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	entry, ok := d.saved[id]
	s, live := d.sessions.get(id)
	if !ok && !live {
		return fmt.Errorf("unknown session %q", id)
	}
	delete(d.saved, id)
	if err := d.saveSessionsLocked(); err != nil {
		if ok {
			d.saved[id] = entry
		}
		return err
	}
	if live {
		_ = s.Close()
		d.sessions.remove(id)
	}
	return nil
}
