package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// savedSession keeps launch inputs, not resolved commands containing temporary
// MCP paths. Profiles and extensions are resolved again when the user attaches.
// Its messages are shared by every copy of the entry: a change clones them.
type savedSession struct {
	Info           *protocol.SessionInfo
	Request        *protocol.OpenSession
	TranscriptPath string
}

// savedJSON is a savedSession as sessions.json has it. The messages are in
// protojson with their proto names, as encoding/json wrote them before:
// times are RFC 3339 either way.
type savedJSON struct {
	Info           json.RawMessage `json:"info"`
	Request        json.RawMessage `json:"request"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
}

var (
	savedMarshal   = protojson.MarshalOptions{UseProtoNames: true}
	savedUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func (e savedSession) MarshalJSON() ([]byte, error) {
	info, err := savedMarshal.Marshal(e.Info)
	if err != nil {
		return nil, err
	}
	req, err := savedMarshal.Marshal(e.Request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(savedJSON{Info: info, Request: req, TranscriptPath: e.TranscriptPath})
}

func (e *savedSession) UnmarshalJSON(data []byte) error {
	var raw savedJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Info, e.Request, e.TranscriptPath = &protocol.SessionInfo{}, &protocol.OpenSession{}, raw.TranscriptPath
	if len(raw.Info) > 0 {
		if err := savedUnmarshal.Unmarshal(raw.Info, e.Info); err != nil {
			return fmt.Errorf("info: %w", err)
		}
	}
	if len(raw.Request) > 0 {
		if err := savedUnmarshal.Unmarshal(raw.Request, e.Request); err != nil {
			return fmt.Errorf("request: %w", err)
		}
	}
	return nil
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
		entry.Info.Id, entry.Info.NodeId = id, d.cfg.NodeID
		entry.Info.State, entry.Info.Busy, entry.Info.ExitCode = protocol.StateStopped, false, nil
		entry.Info.Caps = agents.Caps(entry.Info.GetAgent())
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
	return writeAtomic(d.sessionPath, data)
}

// writeAtomic replaces the file at path with data, so a crash leaves the
// old file or the new, never half of one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
	return os.Rename(tmp.Name(), path)
}

func (d *Daemon) listSessions() []*protocol.SessionInfo {
	d.sessionMu.Lock()
	out := d.sessions.list()
	live := map[string]bool{}
	for _, info := range out {
		live[info.GetId()] = true
	}
	for id, entry := range d.saved {
		if !live[id] {
			out = append(out, proto.CloneOf(entry.Info)) // the listing fills in its git
		}
	}
	d.sessionMu.Unlock()
	sortByStart(out)
	// The repositories are read from disk, outside the lock, once a project
	// in a while: the mesh's state is listed every second.
	for i, info := range out {
		out[i].Branch, out[i].Repo, out[i].Owner = d.heads.of(info.GetProject())
	}
	return out
}

// headsFor is how long a project's branch is taken as read.
const headsFor = 3 * time.Second

// heads keeps each project's branch, repository and its owner as last
// read.
type heads struct {
	mu    sync.Mutex
	known map[string]projectHead
}

type projectHead struct {
	branch, repo, owner string
	at                  time.Time
}

// of is project's branch, repository and owner, read again once headsFor
// has passed.
func (h *heads) of(project string) (branch, repo, owner string) {
	h.mu.Lock()
	p, ok := h.known[project]
	h.mu.Unlock()
	if ok && time.Since(p.at) < headsFor {
		return p.branch, p.repo, p.owner
	}
	p.branch, p.repo, p.owner = gitstat.Head(project)
	p.at = time.Now()
	h.mu.Lock()
	if h.known == nil {
		h.known = map[string]projectHead{}
	}
	h.known[project] = p
	h.mu.Unlock()
	return p.branch, p.repo, p.owner
}

func (d *Daemon) openSession(ctx context.Context, req *protocol.OpenSession) (*session.Session, error) {
	return d.startSession(ctx, newSessionID(), req)
}

func (d *Daemon) attachSession(ctx context.Context, id string) (*session.Session, error) {
	d.sessionMu.Lock()
	s, live := d.sessions.get(id)
	saved, ok := d.saved[id]
	d.sessionMu.Unlock()
	switch {
	case live:
		return s, nil
	case !ok:
		return nil, fmt.Errorf("unknown session %q", id)
	}
	return d.startSession(ctx, id, saved.Request)
}

// startSession resolves the launch, and the conversation it continues,
// without holding the session lock: that can mean reading the agent's logs
// or asking its binary for help. Activation itself is serialized, so
// simultaneous windows cannot launch a restored session twice; the later one
// gets the session the first started. Failed starts retain the saved entry.
func (d *Daemon) startSession(ctx context.Context, id string, req *protocol.OpenSession) (*session.Session, error) {
	// The request may be a saved entry's, which this must not change.
	req = proto.CloneOf(req)
	launch, err := agentspec.Resolve(req.GetAgent(), req.GetProfile(), req.GetArgs(), req.GetEnv(), d.agents())
	if err != nil {
		return nil, err
	}
	if req.GetProject() == "" {
		req.Project, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	project, err := filepath.Abs(req.GetProject())
	if err != nil {
		return nil, err
	}
	req.Project = project
	// A session of its own worktree gets it made once, when it first
	// starts; the saved request then names it.
	if w := req.GetWorktree(); w != nil && w.GetPath() == "" {
		if w.GetBase() == "" {
			w.Base = gitstat.Branch(project)
		}
		wtCtx, cancel := context.WithTimeout(ctx, time.Minute)
		w.Repo, w.Path, err = gitstat.AddWorktree(wtCtx, project, w.GetBranch(), cmp.Or(w.GetStart(), w.GetBase()))
		cancel()
		if err != nil {
			return nil, err
		}
		req.Project = w.GetPath()
	}

	d.sessionMu.Lock()
	previous, existed := d.saved[id]
	var claimed map[string]bool // only an older saved session recovers one
	if existed && previous.Info.GetConversationId() == "" {
		claimed = d.claimedLocked(id)
	}
	d.sessionMu.Unlock()
	conversationID, transcriptPath, err := d.conversationFor(ctx, req, &launch, previous, existed, claimed)
	if err != nil {
		return nil, err
	}

	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	if s, ok := d.sessions.get(id); ok {
		return s, nil
	}
	previous, ok := d.saved[id]
	if ok != existed {
		return nil, fmt.Errorf("session %q changed while it was starting", id)
	}
	// A conversation recovered for an older saved session is only its own
	// while no other session took it in the meantime.
	if existed && previous.Info.GetConversationId() == "" && d.claimedLocked(id)[conversationID] {
		return nil, fmt.Errorf("conversation %s already belongs to another session", conversationID)
	}
	cleanup := d.withContextServers(ctx, &launch, id)
	s, err := session.Start(session.Spec{ID: id, NodeID: d.cfg.NodeID, Agent: req.GetAgent(), Profile: req.GetProfile(),
		Project: req.GetProject(), Command: launch.Command, Env: launch.Env, Cols: uint16(req.GetCols()), Rows: uint16(req.GetRows()), Worktree: req.GetWorktree()})
	if err != nil {
		cleanup()
		return nil, err
	}
	s.SetConversation(conversationID, transcriptPath)
	if existed {
		s.SetName(previous.Info.GetName())
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
	d.trackConversation(s)
	d.log.Info("session started", "id", id, "agent", req.GetAgent(), "project", req.GetProject())
	go func() { <-s.Done(); cleanup() }()
	return s, nil
}

// conversationFor binds a native agent's command to the conversation the
// session should resume, returning the ID and log path to persist. claimed
// is the conversations other sessions hold.
func (d *Daemon) conversationFor(ctx context.Context, req *protocol.OpenSession, launch *agentspec.Launch, previous savedSession, existed bool, claimed map[string]bool) (string, string, error) {
	conversationID, transcriptPath := previous.Info.GetConversationId(), previous.TranscriptPath
	entry, ok := agents.Lookup(req.GetAgent())
	driver := entry.Driver
	native, launches := driver.(agent.Launcher)
	_, logs := driver.(agent.Logs)
	ok = ok && launches && logs && len(launch.Command) > 0
	if ok {
		runs, _ := agents.ByProgram(launch.Command[0])
		ok = runs.Info.Name == entry.Info.Name
	}
	if existed && conversationID != "" && !ok {
		return "", "", fmt.Errorf("cannot resume %s through a custom wrapper; configure the native %s command", conversationID, req.GetAgent())
	}
	if !ok {
		return conversationID, transcriptPath, nil
	}
	// The launch has not started: the daemon's environment with its overrides.
	env := transcript.Env(os.Environ(), launch.Env)
	if conversationID == "" {
		conversationID = native.ArgumentID(launch.Command)
	}
	if existed && conversationID == "" {
		var err error
		conversationID, transcriptPath, err = transcript.Recover(ctx, previous.Info, env, claimed)
		if err != nil {
			return "", "", err
		}
	}
	if existed && conversationID != "" {
		// Resolve profile paths again, but keep the original native identity.
		lookup := proto.CloneOf(previous.Info)
		lookup.ConversationId = conversationID
		if transcriptPath == "" {
			var err error
			transcriptPath, err = transcript.Locate(ctx, lookup, env)
			if err != nil {
				return "", "", err
			}
		}
		if transcriptPath != "" {
			if err := transcript.Verify(lookup, transcriptPath); err != nil {
				return "", "", fmt.Errorf("cannot resume saved conversation: %w", err)
			}
			launch.Command = native.Resume(launch.Command, conversationID)
		} else if command, ok := native.Pin(launch.Command, conversationID); ok {
			// A newly opened window may not have written its first turn yet.
			launch.Command = command
		} else {
			return "", "", fmt.Errorf("saved conversation %s is unavailable; refusing to start a new conversation", conversationID)
		}
	} else if conversationID == "" && !native.Picks(launch.Command) {
		id := conversationUUID()
		if command, ok := native.Pin(launch.Command, id); ok {
			conversationID, launch.Command = id, command
		}
	}
	if flag := native.Isolate(); flag != "" {
		launch.Command = withOptionalFlag(ctx, launch.Command, flag)
	}
	if hooks, ok := driver.(agent.Hooks); ok && d.hookCommand != "" && hasFeature(ctx, launch.Command, driver) {
		status := ""
		if _, ok := driver.(agent.StatusLine); ok && d.statusCommand != "" {
			status = d.statusCommand + " " + agentspec.JoinArgs([]string{req.GetAgent()})
		}
		run, err := config.RunDir(d.dir)
		if err != nil {
			d.log.Warn("hooks: no directory for the agent's files", "err", err)
		}
		hooks.WithHooks(&launch.Launch, d.hookCommand, status, run)
	}
	return conversationID, transcriptPath, nil
}

func (d *Daemon) renameSession(id, name string) (*protocol.SessionInfo, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	entry, ok := d.saved[id]
	s, live := d.sessions.get(id)
	if !ok && !live {
		return nil, fmt.Errorf("unknown session %q", id)
	}
	if ok {
		previous := entry
		entry.Info = proto.CloneOf(entry.Info)
		entry.Info.Name = name
		d.saved[id] = entry
		if err := d.saveSessionsLocked(); err != nil {
			d.saved[id] = previous
			return nil, err
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
