package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sandbox"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/skills"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/ironpark/mako/pkg/fsutil"
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
	ParentID       string
	ParentNodeID   string
	PluginID       string
	PinnedRepo     *string
	PinnedRemote   *string
	// Sandbox is the sandbox the session ran in, which it runs in again
	// whatever its agent's configuration says now: its agent's settings
	// and conversations are in its sandbox home, and those of what it
	// starts are in its sandbox too.
	Sandbox string
}

// savedJSON is a savedSession as sessions.json has it. The messages are in
// protojson with their proto names, as encoding/json wrote them before:
// times are RFC 3339 either way.
type savedJSON struct {
	Info           json.RawMessage `json:"info"`
	Request        json.RawMessage `json:"request"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	ParentID       string          `json:"parent_session,omitempty"`
	ParentNodeID   string          `json:"parent_node,omitempty"`
	PluginID       string          `json:"plugin_id,omitempty"`
	PinnedRepo     *string         `json:"pinned_repo,omitempty"`
	PinnedRemote   *string         `json:"pinned_remote,omitempty"`
	Sandbox        string          `json:"sandbox,omitempty"`
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
	return json.Marshal(savedJSON{
		Info: info, Request: req, TranscriptPath: e.TranscriptPath,
		ParentID: e.ParentID, ParentNodeID: e.ParentNodeID, PluginID: e.PluginID,
		PinnedRepo: e.PinnedRepo, PinnedRemote: e.PinnedRemote,
		Sandbox: e.Sandbox,
	})
}

func (e *savedSession) UnmarshalJSON(data []byte) error {
	var raw savedJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Info, e.Request, e.TranscriptPath = &protocol.SessionInfo{}, &protocol.OpenSession{}, raw.TranscriptPath
	e.ParentID = raw.ParentID
	e.ParentNodeID = raw.ParentNodeID
	e.PluginID = raw.PluginID
	e.PinnedRepo = raw.PinnedRepo
	e.PinnedRemote = raw.PinnedRemote
	e.Sandbox = raw.Sandbox
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

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (d *Daemon) savedSession(id string) (savedSession, bool) {
	d.sessionMu.Lock()
	saved, ok := d.saved[id]
	d.sessionMu.Unlock()
	return saved, ok
}

// pinnedGit returns the git repository root and remote pinned when session id
// started, and whether the session has a pinned scope. An empty pinned remote
// is distinct from unpinned (legacy/synthetic SessionInfo) so editing
// .git/config mid-session cannot move a session into another repo: scope.
func (d *Daemon) pinnedGit(id string) (repo, remote string, pinned bool) {
	if id == "" {
		return "", "", false
	}
	d.sessionMu.Lock()
	saved, ok := d.saved[id]
	d.sessionMu.Unlock()
	if !ok || saved.PinnedRemote == nil {
		return "", "", false
	}
	if saved.PinnedRepo != nil {
		repo = *saved.PinnedRepo
	}
	return repo, *saved.PinnedRemote, true
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
	nodeID := d.config().NodeID
	saved := sessionFile{NodeID: nodeID}
	if err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("read saved sessions: %w", err)
		}
		if saved.NodeID != nodeID {
			return fmt.Errorf("saved sessions belong to node %q", saved.NodeID)
		}
	}
	if saved.Sessions == nil {
		saved.Sessions = map[string]savedSession{}
	}
	for id, entry := range saved.Sessions {
		entryNode := nodeID
		if config.IsSSHNodeID(entry.Request.GetNodeId()) {
			entryNode = entry.Request.GetNodeId()
		}
		entry.Info.Id, entry.Info.NodeId = id, entryNode
		entry.Info.State, entry.Info.Busy, entry.Info.ExitCode = protocol.StateStopped, false, nil
		entry.Info.Status, entry.Info.StatusAt, entry.Info.Approval, entry.Info.AuthError = "", nil, nil, nil
		if !config.IsSSHNodeID(entryNode) {
			entry.Info.Caps = agents.Caps(entry.Info.GetAgent())
		}
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
	data, err := json.MarshalIndent(sessionFile{NodeID: d.config().NodeID, Sessions: d.saved}, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.Write(d.sessionPath, data)
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
	// Each says the session that spawned it, which only the daemon keeps.
	for _, info := range out {
		saved := d.saved[info.GetId()]
		info.ParentSessionId = saved.ParentID
		info.ParentNodeId = saved.ParentNodeID
	}
	d.sessionMu.Unlock()
	sortByStart(out)
	// The repositories are read from disk, outside the lock, once a project
	// in a while: the mesh's state is listed every second.
	for _, info := range out {
		d.withHead(info)
	}
	return out
}

// withHead fills in info's git as its project's repository has it: its
// branch, the repository, its owner and remote, and its stamp. A
// front-end places a session by them, in its project's section.
func (d *Daemon) withHead(info *protocol.SessionInfo) *protocol.SessionInfo {
	if config.IsSSHNodeID(info.GetNodeId()) {
		if d.ssh != nil {
			h := d.ssh.gitHead(info.GetNodeId(), info.GetProject())
			info.Branch, info.Repo, info.Owner, info.Remote, info.GitStamp = h.branch, h.repo, h.owner, h.remote, h.stamp
		}
		return info
	}
	h := d.heads.of(info.GetProject())
	info.Branch, info.Repo, info.Owner, info.Remote, info.GitStamp = h.branch, h.repo, h.owner, h.remote, h.stamp
	return info
}

// headsFor is how long a project's branch is taken as read.
const headsFor = 3 * time.Second

// heads keeps each project's branch, repository and its owner, and the
// stamp of its repository, as last read.
type heads struct {
	mu    sync.Mutex
	known map[string]projectHead
}

type projectHead struct {
	branch, repo, owner, remote string
	stamp                       string // gitstat.Stamp's
	at                          time.Time
}

// of is project's branch, repository, its origin's owner and repository,
// and its repository's stamp, read again once headsFor has passed.
func (h *heads) of(project string) projectHead {
	h.mu.Lock()
	p, ok := h.known[project]
	h.mu.Unlock()
	if ok && time.Since(p.at) < headsFor {
		return p
	}
	p.branch, p.repo, p.owner, p.remote = gitstat.Head(project)
	p.stamp = gitstat.Stamp(project)
	p.at = time.Now()
	h.mu.Lock()
	if h.known == nil {
		h.known = map[string]projectHead{}
	}
	h.known[project] = p
	h.mu.Unlock()
	return p
}

func (d *Daemon) openSession(ctx context.Context, req *protocol.OpenSession) (*session.Session, error) {
	parent, name, prompt := req.GetParentSessionId(), req.GetName(), req.GetPrompt()
	req = proto.CloneOf(req)
	req.ParentSessionId, req.Name, req.Prompt = "", "", ""
	if parent != "" {
		if _, ok := d.sessions.get(parent); !ok {
			return nil, fmt.Errorf("unknown session %q", parent)
		}
		if d.supportsAgentMCP(req.GetAgent(), req.GetProfile()) {
			// Told of its parent as an agent spawn_agent starts is.
			prompt = projectTaskPrompt(parent, prompt)
		} else if prompt != "" {
			prompt = fmt.Sprintf("[Mako project task: parent session %s.]\n\n%s", parent, prompt)
		}
	}
	s, err := d.startSessionAs(ctx, newSessionID(), req, parent)
	if err != nil {
		return nil, err
	}
	if name != "" {
		if _, err := d.renameSession(s.ID(), name); err != nil {
			return nil, fmt.Errorf("session %s started but could not be named: %w", s.ID(), err)
		}
	}
	if prompt != "" {
		go d.deliverInitialPrompt(s, prompt)
	}
	return s, nil
}

const (
	initialPromptPoll       = 100 * time.Millisecond
	initialPromptIdleSettle = 400 * time.Millisecond
	initialPromptPasteQuiet = 800 * time.Millisecond
	initialPromptQuiet      = 1500 * time.Millisecond
	initialPromptShellQuiet = 400 * time.Millisecond
	initialPromptTimeout    = 2 * time.Minute
)

// deliverInitialPrompt waits until a newly started session is ready at its
// prompt, then types prompt and Enter into its terminal.
func (d *Daemon) deliverInitialPrompt(s *session.Session, prompt string) {
	var ctxDone <-chan struct{}
	if d.ctx != nil {
		ctxDone = d.ctx.Done()
	}
	deadline := time.NewTimer(initialPromptTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(initialPromptPoll)
	defer tick.Stop()
	for {
		if d.initialPromptReady(s, time.Now()) {
			if err := s.Send(prompt, []byte{'\r'}); err != nil {
				d.log.Warn("initial prompt: not typed", "session", s.ID(), "err", err)
			}
			return
		}
		select {
		case <-s.Done():
			return
		case <-ctxDone:
			return
		case <-deadline.C:
			d.log.Warn("initial prompt: timed out waiting for prompt", "session", s.ID())
			return
		case <-tick.C:
		}
	}
}

// initialPromptReady reports whether a newly started session is at its prompt
// and quiet enough to receive its first prompt.
func (d *Daemon) initialPromptReady(s *session.Session, now time.Time) bool {
	lastOut := s.LastOutput()
	if lastOut.IsZero() {
		return false
	}
	if s.Agent() == agentspec.ShellName {
		return now.Sub(lastOut) >= initialPromptShellQuiet
	}
	if at, since := s.AtPrompt(); at {
		return now.Sub(since) >= initialPromptIdleSettle && now.Sub(lastOut) >= initialPromptIdleSettle
	}
	if status := s.Info().GetStatus(); status == protocol.AgentWaiting || protocol.AtWork(status) {
		return false
	}
	if s.BracketedPaste() {
		return now.Sub(lastOut) >= initialPromptPasteQuiet
	}
	return now.Sub(lastOut) >= initialPromptQuiet
}

// projectTaskPrompt is prompt as an agent session parent spawned is
// given it: headed by who its parent is, and how to report.
func projectTaskPrompt(parent, prompt string) string {
	return projectTaskPromptFromNode(parent, "", prompt)
}

func projectTaskPromptFromNode(parent, parentNodeName, prompt string) string {
	who := parent
	if name := typable(parentNodeName); name != "" {
		who = fmt.Sprintf("%s (on %s)", parent, name)
	}
	return fmt.Sprintf("[Mako project task: parent session %s. Use the mako MCP tools to coordinate and send_message to report results to the parent.]\n\n%s", who, prompt)
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
	if nodeID := saved.Request.GetNodeId(); config.IsSSHNodeID(nodeID) {
		if !d.isDirectSSH(nodeID) {
			return nil, fmt.Errorf("SSH host %q is no longer available in direct SSH mode", nodeID)
		}
		return d.startDirectSSHSession(ctx, id, saved.Request, saved.ParentID)
	}
	return d.startSession(ctx, id, saved.Request)
}

// startSession resolves the launch, and the conversation it continues,
// without holding the session lock: that can mean reading the agent's logs
// or asking its binary for help. Activation itself is serialized, so
// simultaneous windows cannot launch a restored session twice; the later one
// gets the session the first started. Failed starts retain the saved entry.
func (d *Daemon) startSession(ctx context.Context, id string, req *protocol.OpenSession) (_ *session.Session, err error) {
	return d.startSessionAs(ctx, id, req, "")
}

func (d *Daemon) startSessionAs(ctx context.Context, id string, req *protocol.OpenSession, parent string) (_ *session.Session, err error) {
	return d.startSessionWithParent(ctx, id, req, parent, "")
}

func (d *Daemon) startSessionWithParent(ctx context.Context, id string, req *protocol.OpenSession, parent, parentNode string) (_ *session.Session, err error) {
	// The request may be a saved entry's, which this must not change.
	req = proto.CloneOf(req)
	env := req.GetEnv()
	if len(req.GetDevices()) > 0 {
		// The session holds its devices until it ends, or fails to start.
		defer func() {
			if err != nil {
				d.releaseSession(id)
			}
		}()
		lent, err := d.deviceEnv(ctx, id, req.GetDevices())
		if err != nil {
			return nil, err
		}
		env = maps.Clone(env)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, lent)
	}
	launch, err := agentspec.ResolveWith(req.GetAgent(), req.GetProfile(), req.GetArgs(), env, agentspec.ResolveOptions{
		Model:  req.GetModel(),
		Effort: req.GetEffort(),
		Yolo:   req.Yolo,
	}, d.agents())
	if err != nil {
		return nil, err
	}
	// A session runs in the sandbox it started in, whatever its agent's
	// configuration says now: its agent's settings and conversation are
	// in the home of that sandbox, or of none. One that a session in the
	// sandbox starts, or that rewinds one, runs in it too.
	d.sessionMu.Lock()
	prior, resumed := d.saved[id]
	inherited := cmp.Or(d.saved[cmp.Or(prior.ParentID, parent)].Sandbox, d.sandboxSeeds[id])
	delete(d.sandboxSeeds, id)
	d.sessionMu.Unlock()
	if resumed {
		launch.Sandbox = prior.Sandbox
	}
	launch.Sandbox = agentspec.InheritedSandbox(inherited, launch.Sandbox)
	if launch.Sandbox != "" {
		if err := sandbox.Supported(); err != nil {
			return nil, fmt.Errorf("agent %q runs in the workspace sandbox: %w", agentspec.Target(req.GetAgent(), req.GetProfile()), err)
		}
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
	// Windows takes a folder in whatever case it is typed, which the
	// projects' paths, and the window's, are not in.
	if real, err := filepath.EvalSymlinks(project); runtime.GOOS == "windows" && err == nil && strings.EqualFold(real, project) {
		project = real
	}
	req.Project = project
	// A sandbox that would be refused is refused before its home, or a
	// worktree, is made for it.
	if launch.Sandbox != "" {
		if err := d.checkSandbox(id, req.GetAgent(), launch.Sandbox, sandboxRoots(project, d.projectDirs(project))); err != nil {
			return nil, err
		}
	}
	sandboxHome, err := d.withSandboxHome(req.GetAgent(), &launch)
	if err != nil {
		return nil, err
	}
	// A session of its own worktree gets it made once, when it first
	// starts; the saved request then names it. If startSessionWithParent
	// fails before the session starts, roll back the freshly created
	// worktree so failed launches (e.g. timed-out routines) do not leak.
	var createdWorktree *protocol.Worktree
	startedOK := false
	if w := req.GetWorktree(); w != nil && w.GetPath() == "" {
		if w.GetBase() == "" {
			w.Base = gitstat.Branch(project)
		}
		wtCtx, cancel := context.WithTimeout(ctx, time.Minute)
		repo, path, branch, err := gitstat.AddWorktree(wtCtx, project, w.GetBranch(), cmp.Or(w.GetStart(), w.GetBase()))
		cancel()
		if err != nil {
			return nil, err
		}
		w.Repo, w.Path, w.Branch = repo, path, branch
		req.Project = w.GetPath()
		createdWorktree = proto.CloneOf(w)
		defer func() {
			if !startedOK && createdWorktree != nil {
				rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer rmCancel()
				if _, rmErr := gitstat.RemoveWorktree(rmCtx, createdWorktree); rmErr != nil {
					d.log.Warn("failed to roll back worktree after session start error", "path", createdWorktree.GetPath(), "err", rmErr)
				}
			}
		}()
	}
	// A project that gives its folders gives them to an agent started in
	// one: worked out at each start, so a resumed session has its
	// project's folders as they are now.
	extraDirs := d.projectDirs(req.GetProject())
	launch.Command = withProjectDirs(req.GetAgent(), launch.Command, extraDirs)

	d.sessionMu.Lock()
	previous, existed := d.saved[id]
	var pluginID string
	var pinnedRepo, pinnedRemote *string
	var parentProject string
	var parentPinnedRepo, parentPinnedRemote *string
	if existed {
		parent = previous.ParentID
		parentNode = previous.ParentNodeID
		pluginID = previous.PluginID
		pinnedRepo = cloneStringPtr(previous.PinnedRepo)
		pinnedRemote = cloneStringPtr(previous.PinnedRemote)
	} else if parent != "" && parentNode == "" {
		if parentSaved, ok := d.saved[parent]; ok && parentSaved.PinnedRemote != nil {
			parentProject = parentSaved.Info.GetProject()
			parentPinnedRepo = cloneStringPtr(parentSaved.PinnedRepo)
			parentPinnedRemote = cloneStringPtr(parentSaved.PinnedRemote)
		}
	}
	var claimed map[string]bool // only an older saved session recovers one
	if existed && previous.Info.GetConversationId() == "" {
		claimed = d.claimedLocked(id)
	}
	d.sessionMu.Unlock()

	if pinnedRemote == nil && parentPinnedRemote != nil && parentProject != "" && cleanPath(parentProject) == cleanPath(req.GetProject()) {
		pinnedRepo = parentPinnedRepo
		pinnedRemote = parentPinnedRemote
	}
	if pinnedRepo == nil || pinnedRemote == nil {
		_, repo, _, remote := gitstat.Head(req.GetProject())
		if pinnedRepo == nil {
			pinnedRepo = &repo
		}
		if pinnedRemote == nil {
			pinnedRemote = &remote
		}
	}

	// Populate standard Mako session environment variables for all agent runs.
	mode := "main"
	if parent != "" {
		mode = "subagent"
	}
	launch.SetEnv("AGENT_MAKO_SESSION", id)
	launch.SetEnv("AGENT_MAKO_PROJECT", req.GetProject())
	launch.SetEnv("AGENT_MAKO_MODE", mode)
	if nodeID := d.config().NodeID; nodeID != "" {
		launch.SetEnv("AGENT_MAKO_NODE", nodeID)
	}
	if req.GetProfile() != "" {
		launch.SetEnv("AGENT_MAKO_PROFILE", req.GetProfile())
	}
	if parent != "" {
		launch.SetEnv("AGENT_MAKO_PARENT", parent)
	}
	if parentNode != "" {
		launch.SetEnv("AGENT_MAKO_PARENT_NODE", parentNode)
	}

	conversationID, transcriptPath, err := d.conversationFor(ctx, req, &launch, previous, existed, claimed)
	if err != nil {
		return nil, err
	}

	// The session's MCP servers are resolved, and its agent probed for
	// live replies, without the session lock, which every other start and
	// every listing waits on: they can take most of a minute. Only one
	// start of a session does so at a time, as a second would take the
	// first's agent peer from it; a later one waits, and gets the session
	// the first started.
	if s, err := d.claimStart(ctx, id); s != nil || err != nil {
		return s, err
	}
	defer d.endStart(id)
	if launch.Shown == nil {
		launch.Shown = slices.Clone(launch.Command)
	}
	cleanup := d.withContextServers(ctx, &launch, id, parent)
	if parentNode != "" {
		d.projectAgents.setParentNode(id, parentNode)
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}
	d.withLive(ctx, &launch.Launch, req.GetAgent(), id)
	if d.clipTools != "" {
		launch.SetEnv("PATH", d.clipTools+string(os.PathListSeparator)+cmp.Or(launch.Env["PATH"], os.Getenv("PATH")))
	}
	if err := skills.EnsureAgentSkills(req.GetAgent(), launch.Env); err != nil {
		d.log.Warn("failed to sync mako built-in skills", "agent", req.GetAgent(), "err", err)
	}
	d.trustLaunchDirs(req.GetAgent(), req.GetProject(), extraDirs, &launch.Launch)
	var box *sandbox.Spec
	if launch.Sandbox != "" {
		spec, removeTemp, err := d.newSandbox(id, req.GetAgent(), launch.Sandbox, sandboxRoots(req.GetProject(), extraDirs), sandboxHome, &launch.Launch)
		if err != nil {
			cleanup()
			return nil, err
		}
		box = spec
		prevCleanup := cleanup
		cleanup = func() {
			prevCleanup()
			removeTemp()
		}
		if req.GetAgent() == "codex" && len(launch.Command) > 0 && isNative("codex", launch.Command[0]) {
			launch.Command = codex.WithoutOwnSandboxTopLevel(launch.Command)
			d.withCodexSocket(box, id)
		}
	}
	var codexBridge *codexBridge
	if req.GetAgent() == "codex" {
		var stopCodex func()
		codexBridge, stopCodex = d.prepareCodexAppServer(ctx, id, req.GetProject(), &launch, box, &conversationID, &transcriptPath)
		prevCleanup := cleanup
		cleanup = func() {
			stopCodex()
			prevCleanup()
		}
	}
	if box != nil {
		wrapped, err := sandbox.Command(*box, launch.Command)
		if err != nil {
			cleanup()
			return nil, err
		}
		launch.Command = wrapped
	}

	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	previous, ok := d.saved[id]
	if ok != existed {
		cleanup()
		return nil, fmt.Errorf("session %q changed while it was starting", id)
	}
	// A conversation recovered for an older saved session is only its own
	// while no other session took it in the meantime.
	if existed && previous.Info.GetConversationId() == "" && d.claimedLocked(id)[conversationID] {
		cleanup()
		return nil, fmt.Errorf("conversation %s already belongs to another session", conversationID)
	}
	s, err := session.Start(session.Spec{ID: id, NodeID: d.config().NodeID, Agent: req.GetAgent(), Profile: req.GetProfile(),
		Project: req.GetProject(), Command: launch.Command, Shown: launch.Shown, Env: launch.Env, Cols: uint16(req.GetCols()), Rows: uint16(req.GetRows()), Worktree: req.GetWorktree(), OnChange: d.pokeStatus})
	if err != nil {
		cleanup()
		return nil, err
	}
	s.SetConversation(conversationID, transcriptPath)
	if existed {
		s.SetName(previous.Info.GetName())
		s.SetTitle(previous.Info.GetTitle())
	}
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	info := s.Info()
	// Commands can embed ephemeral MCP config and secrets; only persist inputs.
	info.Command = nil
	d.saved[id] = savedSession{
		Info: info, Request: req, TranscriptPath: transcriptPath,
		ParentID: parent, ParentNodeID: parentNode, PluginID: pluginID,
		PinnedRepo: pinnedRepo, PinnedRemote: pinnedRemote,
		Sandbox: launch.Sandbox,
	}
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
	d.seedDynamicModels(s)
	if codexBridge != nil {
		d.attachCodexBridge(s, codexBridge)
	} else if fmr, ok := agents.As[agent.FastModeReader](req.GetAgent()); ok {
		if fast, ok := fmr.InitialFastMode(launch.Launch); ok {
			s.ReportFastMode(fast)
			s.Report(&protocol.Usage{FastMode: proto.Bool(fast)})
		}
	}
	if peer := d.projectAgents.activate(s); peer != nil {
		go d.deliverAgentMessages(peer)
	}
	d.trackConversation(s)
	if parent != "" && parentNode == "" {
		d.reportProjectAgent(s, parent)
		go d.followProjectAgent(s, parent)
	}
	d.log.Info("session started", "id", id, "agent", req.GetAgent(), "project", req.GetProject())
	startedOK = true
	go func() { <-s.Done(); d.live.resetSuggestion(id); cleanup(); d.releaseSession(id) }()
	return s, nil
}

// claimStart makes this the one start of session id under way, waiting
// out another's first. It returns the session another start started, nil
// when this one is to start it.
func (d *Daemon) claimStart(ctx context.Context, id string) (*session.Session, error) {
	for {
		d.sessionMu.Lock()
		if s, ok := d.sessions.get(id); ok {
			d.sessionMu.Unlock()
			return s, nil
		}
		other, busy := d.starting[id]
		if !busy {
			if d.starting == nil {
				d.starting = map[string]chan struct{}{}
			}
			d.starting[id] = make(chan struct{})
			d.sessionMu.Unlock()
			return nil, nil
		}
		d.sessionMu.Unlock()
		select {
		case <-other:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// endStart ends the start claimStart claimed, letting the next go on.
// The caller must not hold sessionMu.
func (d *Daemon) endStart(id string) {
	d.sessionMu.Lock()
	close(d.starting[id])
	delete(d.starting, id)
	d.sessionMu.Unlock()
}

// conversationFor binds a native agent's command to the conversation the
// session should resume, returning the ID and log path to persist. claimed
// is the conversations other sessions hold.
func (d *Daemon) conversationFor(ctx context.Context, req *protocol.OpenSession, launch *agentspec.Launch, previous savedSession, existed bool, claimed map[string]bool) (string, string, error) {
	conversationID, transcriptPath := previous.Info.GetConversationId(), previous.TranscriptPath
	var entry agents.Entry
	ok := len(launch.Command) > 0
	if ok {
		entry, ok = agents.Native(req.GetAgent(), launch.Command[0])
	}
	driver := entry.Driver
	native, launches := driver.(agent.Launcher)
	_, logs := driver.(agent.Logs)
	ok = ok && launches && logs
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
	launch.Shown = slices.Clone(launch.Command)
	if hooks, ok := driver.(agent.Hooks); ok && d.hookCommand != "" && hasFeature(ctx, launch.Command, driver) {
		status := ""
		if _, ok := driver.(agent.StatusLine); ok && d.statusCommand != "" {
			status = d.statusCommand + " " + agentspec.CommandLine([]string{req.GetAgent()})
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
	root := entry.Info.GetProject()
	wtRepo := entry.Info.GetWorktree().GetRepo()
	if live {
		info := s.Info()
		root = info.GetProject()
		if r := info.GetWorktree().GetRepo(); r != "" {
			wtRepo = r
		}
		_ = s.Close()
		d.sessions.remove(id)
	}
	d.dropCheckpoints(id, root, wtRepo)
	d.live.forget(id)
	d.loggedCommands.forget(id)
	if err := d.projectAgents.messages.remove(id); err != nil {
		d.log.Warn("agent inbox could not be removed", "session", id, "err", err)
	}
	return nil
}

// sleepSession stops the program of session id while its agent rests,
// keeping the session, stopped, to resume its conversation when it is next
// attached. It refuses a session that is not resting (restless).
func (d *Daemon) sleepSession(id string) (*protocol.SessionInfo, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	s, live := d.sessions.get(id)
	entry, kept := d.saved[id]
	if !live || !kept {
		return nil, fmt.Errorf("session %q is not running", id)
	}
	info := s.Info()
	if why := restless(s, info, d.childAtWorkLocked(id)); why != "" {
		return nil, fmt.Errorf("session %q is not resting: %s", id, why)
	}
	next := entry
	next.Info = proto.CloneOf(info)
	next.Info.Command = nil // as a start saves it: no ephemeral paths or secrets
	next.Info.State, next.Info.Busy, next.Info.Idle, next.Info.ExitCode = protocol.StateStopped, false, false, nil
	next.Info.Tasks, next.Info.Dialog, next.Info.AuthError = nil, nil, nil
	d.saved[id] = next
	if err := d.saveSessionsLocked(); err != nil {
		d.saved[id] = entry
		return nil, err
	}
	d.sessions.remove(id)
	_ = s.Close()
	d.live.forget(id)
	d.log.Info("session asleep", "id", id, "agent", info.GetAgent())
	return proto.CloneOf(next.Info), nil
}

// restless says why a session's agent is not resting, "" when it is: it
// must have a conversation to resume, sit at its prompt with nobody
// attached, and have nothing at work or asked of the user. child says
// whether a session it started is at work.
func restless(s *session.Session, info *protocol.SessionInfo, child bool) string {
	switch {
	case info.GetState() != protocol.StateRunning:
		return "not running"
	case info.GetConversationId() == "" || !resumable(info):
		return "no conversation to resume"
	case protocol.AtWork(info.GetStatus()) || info.GetBusy():
		return "at work"
	case info.GetApproval() != nil || info.GetDialog() != nil || info.GetStatus() == protocol.AgentWaiting:
		return "waiting on the user"
	case slices.ContainsFunc(info.GetTasks(), (*protocol.BackgroundTask).GetRunning):
		return "a background task runs"
	case s.SubagentsWorking():
		return "a subagent works"
	case child:
		return "a session it started works"
	case s.Watched():
		return "attached"
	}
	return ""
}

// childAtWorkLocked reports whether a session that session id started
// runs and is at work. The caller holds sessionMu.
func (d *Daemon) childAtWorkLocked(id string) bool {
	for child, entry := range d.saved {
		if entry.ParentID != id {
			continue
		}
		if s, ok := d.sessions.get(child); ok {
			if info := s.Info(); info.GetState() == protocol.StateRunning && (protocol.AtWork(info.GetStatus()) || info.GetBusy()) {
				return true
			}
		}
	}
	return false
}

// resumable reports whether session info's agent resumes a conversation
// when it starts again: one mako drives natively, run as its own program,
// not through a wrapper of the user's.
func resumable(info *protocol.SessionInfo) bool {
	if len(info.GetCommand()) == 0 {
		return false
	}
	entry, ok := agents.Native(info.GetAgent(), info.GetCommand()[0])
	_, launches := entry.Driver.(agent.Launcher)
	_, logs := entry.Driver.(agent.Logs)
	return ok && launches && logs
}
