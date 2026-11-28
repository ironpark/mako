package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/pkg/fsutil"
)

const (
	agentMsgArg     = "agent-msg"
	agentPluginArg  = "agent-plugin"
	agentProjectArg = "agent-project"
	agySidecarArg   = "agy-sidecar"
	agentCLISock    = "agent-cli.sock"
)

// writeAgentMsgTool writes the `mako-msg` helper script into dir (alongside
// clipboard shims, which are prepended to every session's PATH).
func writeAgentMsgTool(dir, program, socket string) error {
	if dir == "" || program == "" || socket == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@echo off\r\n\"%s\" %s \"%s\" %%*\r\n", program, agentMsgArg, socket)
		return os.WriteFile(filepath.Join(dir, "mako-msg.cmd"), []byte(script), 0o700)
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentMsgArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-msg"), []byte(script), 0o700)
}

// writeAgentPluginTool writes the `mako-plugin` helper script into dir
// (alongside `mako-msg`) so agents and shells inside a Mako session can
// discover and call plugin tools via CLI without bloating MCP schemas.
func writeAgentPluginTool(dir, program, socket string) error {
	if dir == "" || program == "" || socket == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@echo off\r\n\"%s\" %s \"%s\" %%*\r\n", program, agentPluginArg, socket)
		return os.WriteFile(filepath.Join(dir, "mako-plugin.cmd"), []byte(script), 0o700)
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentPluginArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-plugin"), []byte(script), 0o700)
}

// writeAgentProjectTool writes the `mako-project` helper script into dir
// (alongside `mako-msg` and `mako-plugin`) so agents inside a Mako session can
// list default project locations, list projects, and add local or git URL projects.
func writeAgentProjectTool(dir, program, socket string) error {
	if dir == "" || program == "" || socket == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@echo off\r\n\"%s\" %s \"%s\" %%*\r\n", program, agentProjectArg, socket)
		return os.WriteFile(filepath.Join(dir, "mako-project.cmd"), []byte(script), 0o700)
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentProjectArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-project"), []byte(script), 0o700)
}

type agentCLIRequest struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	Op      string `json:"op"` // wait, read, send, list, profiles, plugin_list, plugin_call, sidecar_subscribe

	// wait / read options:
	AfterID       uint64   `json:"after_id,omitempty"`
	AckThrough    uint64   `json:"ack_through,omitempty"`
	From          string   `json:"from,omitempty"`
	WatchID       string   `json:"watch_session_id,omitempty"`
	WatchIDs      []string `json:"watch_session_ids,omitempty"`
	WatchChildren bool     `json:"watch_children,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	TimeoutSec    int      `json:"timeout_seconds,omitempty"`
	NoTimeout     bool     `json:"no_timeout,omitempty"`
	Ack           bool     `json:"ack,omitempty"`

	// send options:
	To   string `json:"to,omitempty"`
	Text string `json:"text,omitempty"`

	// profiles options:
	Node string `json:"node,omitempty"`

	// plugin_list / plugin_call options:
	PluginID string          `json:"plugin_id,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`

	// project_add options:
	Target  string `json:"target,omitempty"`
	Name    string `json:"name,omitempty"`
	BaseDir string `json:"base_dir,omitempty"`
	Folder  string `json:"folder,omitempty"`
	Space   string `json:"space,omitempty"`
}

type agentCLIResponse struct {
	OK          bool                     `json:"ok"`
	Error       string                   `json:"error,omitempty"`
	Reason      string                   `json:"reason,omitempty"` // message, turn_ended, idle, waiting, exited, gone, timeout
	SessionID   string                   `json:"session_id,omitempty"`
	Messages    []agentMessage           `json:"messages,omitempty"`
	NextAfterID uint64                   `json:"next_after_id,omitempty"`
	Sent        *agentMessage            `json:"sent,omitempty"`
	Agents      *listAgentsResult        `json:"agents,omitempty"`
	Profiles    *listAgentProfilesResult `json:"profiles,omitempty"`
	Tools       []plugin.ContributedTool `json:"tools,omitempty"`
	Output      json.RawMessage          `json:"output,omitempty"`
	Projects    []agentProjectSummary    `json:"projects,omitempty"`
	ProjectDirs []string                 `json:"project_dirs,omitempty"`
	Added       *agentProjectAddResult   `json:"added,omitempty"`
	Status      string                   `json:"status,omitempty"`
	State       string                   `json:"state,omitempty"`
}

func (d *Daemon) listenAgentCLI(ctx context.Context, dir string) error {
	path := filepath.Join(dir, agentCLISock)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("agent CLI: %w", err)
	}
	ln = transport.OwnListener(ln, path)

	var tokBuf [32]byte
	if _, err := rand.Read(tokBuf[:]); err != nil {
		_ = ln.Close()
		return fmt.Errorf("agent CLI token: %w", err)
	}
	tok := hex.EncodeToString(tokBuf[:])
	tokenPath := filepath.Join(dir, sidecarTokenFile)
	if err := fsutil.Write(tokenPath, []byte(tok)); err != nil {
		_ = ln.Close()
		return fmt.Errorf("agent CLI token: %w", err)
	}

	d.projectAgents.mu.Lock()
	d.projectAgents.cliSocket = path
	d.projectAgents.sidecarToken = tok
	d.projectAgents.mu.Unlock()

	if d.clipTools != "" && d.hookBinary != "" {
		_ = writeAgentMsgTool(d.clipTools, d.hookBinary, path)
		_ = writeAgentPluginTool(d.clipTools, d.hookBinary, path)
		_ = writeAgentProjectTool(d.clipTools, d.hookBinary, path)
	}
	context.AfterFunc(ctx, func() {
		_ = ln.Close()
		_ = os.Remove(tokenPath)
		d.projectAgents.mu.Lock()
		if d.projectAgents.sidecarToken == tok {
			d.projectAgents.sidecarToken = ""
		}
		d.projectAgents.mu.Unlock()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serveAgentCLI(ctx, conn)
		}
	}()
	return nil
}

func (d *Daemon) serveAgentCLI(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req agentCLIRequest
	if err := json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	writeResp := func(r agentCLIResponse) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(conn).Encode(r)
	}

	if req.Op == "sidecar_subscribe" {
		d.projectAgents.mu.Lock()
		expected := d.projectAgents.sidecarToken
		d.projectAgents.mu.Unlock()
		if expected == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(expected)) != 1 {
			writeResp(agentCLIResponse{Error: "unauthorized sidecar subscription"})
			return
		}
		d.serveAgySidecar(ctx, conn)
		return
	}

	peer := d.projectAgents.byToken(req.Token)
	if peer == nil || peer.id != req.Session {
		writeResp(agentCLIResponse{Error: "invalid or expired session credentials"})
		return
	}
	select {
	case <-peer.ready:
	case <-peer.ctx.Done():
		writeResp(agentCLIResponse{Error: "session ended"})
		return
	case <-ctx.Done():
		return
	}

	reqCtx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	stopCancel := context.AfterFunc(ctx, cancel)
	defer stopCancel()

	switch req.Op {
	case "read":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 100 {
			limit = 100
		}
		msgs, err := d.projectAgents.messages.read(reqCtx, peer.id, req.AfterID, req.AckThrough, limit, 0)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		next := max(req.AfterID, req.AckThrough)
		if len(msgs) > 0 {
			next = msgs[len(msgs)-1].ID
		}
		writeResp(agentCLIResponse{OK: true, Messages: msgs, NextAfterID: next})

	case "send":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		if err := checkAgentText(req.Text); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		msg, err := d.sendAgentMessage(reqCtx, peer, req.To, req.Text)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Sent: &msg})

	case "list":
		out, err := d.listProjectAgents(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Agents: &out})

	case "profiles":
		out, err := d.listAgentProfiles(peer, listAgentProfilesArgs{Node: req.Node})
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Profiles: &out})

	case "wait":
		resp := d.cliWait(reqCtx, peer, req)
		writeResp(resp)

	case "plugin_list":
		s, err := d.projectCaller(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		var tools []plugin.ContributedTool
		if d.plugins != nil {
			scopeCtx := d.sessionScopeContext(s.ID(), s.Info())
			for _, tl := range d.plugins.Tools() {
				if req.PluginID != "" && tl.PluginID != req.PluginID {
					continue
				}
				if !toolUsableInScope(tl.Scope, scopeCtx) {
					continue
				}
				tools = append(tools, tl)
			}
		}
		writeResp(agentCLIResponse{OK: true, Tools: tools})

	case "plugin_call":
		s, err := d.projectCaller(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		if d.plugins == nil {
			writeResp(agentCLIResponse{Error: "plugins are not enabled on this node"})
			return
		}
		if req.PluginID == "" || req.ToolID == "" {
			writeResp(agentCLIResponse{Error: "plugin_id and tool_id are required"})
			return
		}
		scopeCtx := d.sessionScopeContext(s.ID(), s.Info())
		out, err := d.plugins.CallToolScoped(reqCtx, req.PluginID, req.ToolID, req.Args, scopeCtx)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		rawOut := json.RawMessage(out)
		if !json.Valid(rawOut) {
			rawOut, _ = json.Marshal(out)
		}
		writeResp(agentCLIResponse{OK: true, Output: rawOut})

	case "project_list":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Projects: d.listWorkspaceProjects()})

	case "project_dirs":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, ProjectDirs: d.projectStorageDirs()})

	case "project_add":
		s, err := d.projectCaller(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		// Adding a project clones it wherever the caller says, which
		// for a session in the sandbox would be outside it.
		if d.sandboxed(peer.id) {
			writeResp(agentCLIResponse{Error: "a sandboxed session cannot add projects"})
			return
		}
		res, err := d.addProjectFromCLI(reqCtx, s.Info().GetProject(), req.Target, req.Name, req.BaseDir, req.Folder, req.Space)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Added: &res})

	default:
		writeResp(agentCLIResponse{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

func (d *Daemon) cliWait(ctx context.Context, peer *agentPeer, req agentCLIRequest) agentCLIResponse {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return agentCLIResponse{Error: err.Error()}
	}
	defer d.projectAgents.addCLIWaiter(peer.id)()

	if !req.NoTimeout {
		sec := req.TimeoutSec
		if sec <= 0 {
			sec = 600
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}

	watchIDs := slices.Clone(req.WatchIDs)
	if req.WatchID != "" && !slices.Contains(watchIDs, req.WatchID) {
		watchIDs = append(watchIDs, req.WatchID)
	}
	scope := d.projectScope(caller.Info())
	if req.WatchChildren {
		d.sessionMu.Lock()
		for id, saved := range d.saved {
			if saved.ParentID == peer.id && saved.ParentNodeID == "" && !slices.Contains(watchIDs, id) {
				if s, ok := d.sessions.get(id); ok && s.Info().GetState() == protocol.StateRunning && d.inProject(scope, s.Info()) {
					watchIDs = append(watchIDs, id)
				}
			}
		}
		d.sessionMu.Unlock()
		selfNodeID := d.config().NodeID
		for _, remote := range d.remoteAgents(scope) {
			if remote.GetParentSessionId() == peer.id &&
				(remote.GetParentNodeId() == "" || remote.GetParentNodeId() == selfNodeID) &&
				remote.GetState() == protocol.StateRunning &&
				!slices.Contains(watchIDs, remote.GetId()) {
				watchIDs = append(watchIDs, remote.GetId())
			}
		}
	}

	type watchedTarget struct {
		id           string
		target       *waited
		initialTurns uint64
		worked       bool
		lastCh       <-chan struct{}
	}
	var targets []*watchedTarget
	for _, wid := range watchIDs {
		wid = strings.TrimSpace(wid)
		if wid == "" {
			continue
		}
		if wid == peer.id {
			return agentCLIResponse{Error: "an agent cannot wait on itself"}
		}
		var wt *waited
		if s, ok := d.sessions.get(wid); ok && d.inProject(scope, s.Info()) {
			wt = &waited{info: s.Info, changed: s.Changed}
		} else if d.remoteAgent(scope, wid) != nil {
			idCopy := wid
			wt = &waited{info: func() *protocol.SessionInfo { return d.remoteAgent(scope, idCopy) }}
		} else {
			return agentCLIResponse{Error: errNotInProject.Error()}
		}
		release := d.projectAgents.waitOn(wid)
		defer release()
		targets = append(targets, &watchedTarget{
			id:           wid,
			target:       wt,
			initialTurns: wt.info().GetTurns(),
		})
	}

	var recheck <-chan time.Time
	var ticker *time.Ticker
	if len(targets) > 0 {
		ticker = time.NewTicker(waitRecheck)
		defer ticker.Stop()
		recheck = ticker.C
	}

	// Fan-in change notifications for multiple watched sessions into one channel.
	var anySessChanged chan struct{}
	if len(targets) > 0 {
		anySessChanged = make(chan struct{}, 1)
	}

	for {
		inbox, changed := d.projectAgents.messages.pending(peer.id)
		var matched []agentMessage
		for _, m := range inbox {
			if m.ID <= req.AfterID {
				continue
			}
			if req.From != "" && m.From != req.From {
				continue
			}
			matched = append(matched, m)
		}
		if len(matched) > 0 {
			next := matched[len(matched)-1].ID
			if req.Ack {
				_, _ = d.projectAgents.messages.read(ctx, peer.id, next, next, 1, 0)
			}
			return agentCLIResponse{
				OK:          true,
				Reason:      "message",
				Messages:    matched,
				NextAfterID: next,
			}
		}

		var singleChanged <-chan struct{}
		for _, wt := range targets {
			if len(targets) == 1 && wt.target.changed != nil {
				singleChanged = wt.target.changed()
			} else if wt.target.changed != nil {
				if ch := wt.target.changed(); ch != wt.lastCh {
					wt.lastCh = ch
					go func() {
						select {
						case <-ch:
							select {
							case anySessChanged <- struct{}{}:
							default:
							}
						case <-ctx.Done():
						}
					}()
				}
			}
			info := wt.target.info()
			status := info.GetStatus()
			wt.worked = wt.worked || protocol.AtWork(status)
			switch {
			case info == nil:
				return agentCLIResponse{OK: true, Reason: "gone", SessionID: wt.id}
			case info.GetState() == protocol.StateExited:
				return agentCLIResponse{OK: true, Reason: "exited", SessionID: wt.id, Status: status, State: info.GetState()}
			case status == protocol.AgentWaiting:
				return agentCLIResponse{OK: true, Reason: "waiting", SessionID: wt.id, Status: status, State: info.GetState()}
			case info.GetTurns() > wt.initialTurns || (wt.initialTurns == 0 && wt.worked && !protocol.AtWork(status)):
				return agentCLIResponse{OK: true, Reason: "turn_ended", SessionID: wt.id, Status: status, State: info.GetState()}
			}
		}

		var sessWake <-chan struct{}
		if len(targets) == 1 {
			sessWake = singleChanged
		} else if len(targets) > 1 {
			sessWake = anySessChanged
		}

		select {
		case <-changed:
		case <-sessWake:
		case <-recheck:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return agentCLIResponse{OK: true, Reason: "timeout", Messages: []agentMessage{}}
			}
			return agentCLIResponse{Error: ctx.Err().Error()}
		}
	}
}
