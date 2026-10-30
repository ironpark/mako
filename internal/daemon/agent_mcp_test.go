//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func projectMCPDaemon(t *testing.T) (*Daemon, *session.Session) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp, d.hookBinary = t.TempDir(), t.Context(), nil, "/bin/makod"
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	if err := d.projectAgents.messages.load(d.dir); err != nil {
		t.Fatal(err)
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.listenAgentMCP(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return d, s
}

func projectMCPClient(t *testing.T, d *Daemon, s *session.Session) *sdk.ClientSession {
	return projectMCPClientVersion(t, d, s, "")
}

func projectMCPClientVersion(t *testing.T, d *Daemon, s *session.Session, version string) *sdk.ClientSession {
	t.Helper()
	peer := d.projectAgents.peer(s.ID())
	if peer == nil {
		t.Fatal("session has no builtin MCP server")
	}
	conn, err := net.Dial("unix", d.projectAgents.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := json.NewEncoder(conn).Encode(agentMCPHello{Session: s.ID(), Token: peer.token}); err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.IOTransport{Reader: conn, Writer: conn}, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callProjectTool[T any](t *testing.T, cs *sdk.ClientSession, name string, args any) T {
	t.Helper()
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("%s: %+v, %v", name, result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: %s: %v", name, data, err)
	}
	return out
}

func TestProjectMCPSpawnMessageAndStop(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("tools: %v, %v", tools, err)
	}
	if !strings.Contains(client.InitializeResult().Instructions, parent.ID()) {
		t.Fatal("server did not identify the caller")
	}
	childInfo := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{Prompt: "Review the tests", Name: "Reviewer"})
	parent.SetStatus(protocol.AgentDone)
	if parent.Info().GetStatus() != protocol.AgentWorking {
		t.Fatal("spawn returned before the child was included in parent activity")
	}
	if childInfo.ParentID != parent.ID() || childInfo.Name != "Reviewer" || !childInfo.Messaging {
		t.Fatalf("child: %+v", childInfo)
	}
	child, ok := d.sessions.get(childInfo.ID)
	if !ok || child.Info().GetProject() != parent.Info().GetProject() {
		t.Fatal("child left its parent's project")
	}
	d.sessionMu.Lock()
	saved := d.saved[child.ID()]
	d.sessionMu.Unlock()
	if !strings.Contains(strings.Join(saved.Request.GetArgs(), " "), "Review the tests") || saved.ParentID != parent.ID() {
		t.Fatal("task or parent was not saved")
	}
	childClient := projectMCPClient(t, d, child)
	message := callProjectTool[agentMessage](t, client, "send_message", sendMessageArgs{To: child.ID(), Text: "Check cancellation too"})
	read := callProjectTool[readMessagesResult](t, childClient, "read_messages", readMessagesArgs{})
	if len(read.Messages) != 1 || read.Messages[0].ID != message.ID || read.Messages[0].From != parent.ID() {
		t.Fatalf("inbox: %+v", read)
	}
	callProjectTool[agentMessage](t, childClient, "send_message", sendMessageArgs{To: parent.ID(), Text: "Review complete"})
	read = callProjectTool[readMessagesResult](t, client, "read_messages", readMessagesArgs{})
	if len(read.Messages) != 1 || read.Messages[0].Text != "Review complete" {
		t.Fatalf("reply: %+v", read)
	}
	listed := callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
	if len(listed.Agents) != 2 || listed.SelfID != parent.ID() || len(listed.Available) == 0 {
		t.Fatalf("list: %+v", listed)
	}
	parent.SetStatus(protocol.AgentDone)
	waitFor(t, "child did not keep its parent working", func() bool { return parent.Info().GetStatus() == protocol.AgentWorking })
	child.SetStatus(protocol.AgentDone)
	waitFor(t, "finished child did not release its parent", func() bool { return parent.Info().GetStatus() == protocol.AgentDone })
	stopped := callProjectTool[stopAgentResult](t, client, "stop_agent", stopAgentArgs{SessionID: child.ID()})
	if !stopped.Stopped {
		t.Fatal("child was not stopped")
	}
	if _, ok := d.sessions.get(child.ID()); ok {
		t.Fatal("child remains registered")
	}
	d.projectAgents.messages.mu.Lock()
	_, retained := d.projectAgents.messages.data.Inboxes[child.ID()]
	d.projectAgents.messages.mu.Unlock()
	if retained {
		t.Fatal("deleted session retained its inbox")
	}
}

func TestProjectMCPDiscoverAndSpawnDescribedProfile(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	path := filepath.Join(t.TempDir(), "config.json")
	d.FollowAgents(path)
	specialist := d.cfg.Agents["claude"]
	profileLogs := t.TempDir()
	specialist.Profiles = []agentspec.Profile{
		{Name: "reviewer", Description: "Go 동시성 검토\n취소와 경쟁 상태 분석", Args: []string{"--model", "review-model"},
			Env: map[string]string{"PROFILE_SECRET": "do-not-advertise", "CODEX_HOME": profileLogs}, ContextServers: new([]string{"mako"})},
		{Name: "isolated", Description: "No MCP tools", ContextServers: new([]string{})},
		{Name: "legacy"}, // Existing profiles without descriptions still work.
	}
	write := func(description string) {
		t.Helper()
		specialist.Profiles[0].Description = description
		cfg := *d.cfg
		cfg.Agents = map[string]agentspec.Agent{"claude": d.cfg.Agents["claude"], "specialist": specialist}
		cfg.ContextServers = map[string]mcp.Server{"mako": {Enabled: new(false)}}
		body, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(specialist.Profiles[0].Description)
	listed := callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
	i := slices.IndexFunc(listed.Available, func(a availableAgent) bool { return a.Name == "specialist" })
	if i < 0 {
		t.Fatalf("no specialist: %+v", listed.Available)
	}
	choice := listed.Available[i]
	if choice.Spawnable || len(choice.Profiles) != 3 || !choice.Profiles[0].Spawnable || choice.Profiles[1].Spawnable || choice.Profiles[2].Spawnable ||
		choice.Profiles[0].Description != specialist.Profiles[0].Description || choice.Profiles[2].Description != "" {
		t.Fatalf("profile discovery: %+v", choice)
	}
	data, err := json.Marshal(listed)
	if err != nil || strings.Contains(string(data), "do-not-advertise") || strings.Contains(string(data), "review-model") {
		t.Fatalf("profile discovery exposed launch configuration: %s (%v)", data, err)
	}
	// The caller selects an agent/profile pair using the returned description.
	childInfo := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{
		Agent: choice.Name, Profile: choice.Profiles[0].Name, Prompt: "Review cancellation", Name: "Concurrency reviewer",
	})
	child, ok := d.sessions.get(childInfo.ID)
	if !ok || childInfo.Agent != "specialist" || childInfo.Profile != "reviewer" || !childInfo.Messaging {
		t.Fatalf("spawned profile: %+v", childInfo)
	}
	if !slices.Contains(child.Info().GetCommand(), "review-model") {
		t.Fatalf("profile flags were not applied: %q", child.Info().GetCommand())
	}
	if child.TranscriptEnv()["CODEX_HOME"] != profileLogs {
		t.Fatal("profile environment was not applied")
	}
	d.sessionMu.Lock()
	saved := d.saved[childInfo.ID]
	d.sessionMu.Unlock()
	if saved.Request.GetProfile() != "reviewer" || saved.ParentID != parent.ID() {
		t.Fatalf("profile not saved for restore: %+v", saved)
	}
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "spawn_agent", Arguments: spawnAgentArgs{
		Agent: choice.Name, Profile: "isolated", Prompt: "Cannot communicate",
	}})
	if err == nil && !result.IsError {
		t.Fatal("a non-spawnable profile was accepted")
	}
	write("Frontend accessibility reviews")
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	listed = callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
	i = slices.IndexFunc(listed.Available, func(a availableAgent) bool { return a.Name == "specialist" })
	if i < 0 || listed.Available[i].Profiles[0].Description != "Frontend accessibility reviews" {
		t.Fatal("profile description did not refresh on an existing MCP connection")
	}
}

func TestProjectMCPScopeAndOwnership(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	other, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: parent.Info().GetProject()})
	if err != nil {
		t.Fatal(err)
	}
	listed := callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
	if slices.ContainsFunc(listed.Agents, func(a agentSummary) bool { return a.ID == other.ID() }) {
		t.Fatal("another project's session was listed")
	}
	for _, req := range []sdk.CallToolParams{
		{Name: "send_message", Arguments: sendMessageArgs{To: other.ID(), Text: "foreign"}},
		{Name: "stop_agent", Arguments: stopAgentArgs{SessionID: sibling.ID()}},
		{Name: "send_message", Arguments: map[string]any{"to": sibling.ID(), "text": "forged", "from": other.ID()}},
		{Name: "spawn_agent", Arguments: map[string]any{"prompt": "escape", "project": other.Info().GetProject()}},
	} {
		result, err := client.CallTool(t.Context(), &req)
		if err == nil && !result.IsError {
			t.Fatalf("accepted out-of-scope request: %+v", req)
		}
	}
	callProjectTool[agentMessage](t, client, "send_message", sendMessageArgs{To: sibling.ID(), Text: "same project"})
}

func TestProjectMCPRejectsForgedCredentialsAndRevokesExitedSessions(t *testing.T) {
	d, s := projectMCPDaemon(t)
	conn, err := net.Dial("unix", d.projectAgents.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(agentMCPHello{Session: s.ID(), Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal("forged credentials were accepted")
	}
	client := projectMCPClient(t, d, s)
	peer := d.projectAgents.peer(s.ID())
	data, err := os.ReadFile(d.sessionPath)
	if err != nil || strings.Contains(string(data), peer.token) {
		t.Fatal("session credentials were saved with launch history")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "credential was not revoked", func() bool { return peer.ctx.Err() != nil })
	if _, err := client.ListTools(t.Context(), nil); err == nil {
		t.Fatal("exited session retained its MCP connection")
	}
}

func TestProjectMCPRespectsProfilesAndKeepsExternalServers(t *testing.T) {
	d, _ := projectMCPDaemon(t)
	d.cfg.ContextServers = map[string]mcp.Server{"custom": {Command: &mcp.Command{Path: "/bin/true"}}}
	d.agentFile.mu.Lock()
	d.agentFile.read = false
	d.agentFile.mu.Unlock()
	d.mcp = &mcp.Resolver{}
	run, err := config.RunDir(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		picks           *[]string
		builtin, custom bool
	}{
		{"default", nil, true, true},
		{"none", new([]string{}), false, false},
		{"builtin", new([]string{"mako"}), true, false},
		{"external", new([]string{"custom"}), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch := &agentspec.Launch{Launch: agent.Launch{Command: []string{"claude"}}, ContextServers: tc.picks}
			cleanup := d.withContextServers(t.Context(), launch, "config-"+tc.name, "")
			defer cleanup()
			if !tc.builtin && !tc.custom {
				if len(launch.Command) != 1 {
					t.Fatal("disabled MCP was injected")
				}
				return
			}
			body, err := os.ReadFile(filepath.Join(run, "mcp-config-"+tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(body, &config) != nil {
				t.Fatal("invalid MCP config")
			}
			if (config.Servers["mako"] != nil) != tc.builtin || (config.Servers["custom"] != nil) != tc.custom {
				t.Fatalf("servers %s", body)
			}
		})
	}
}

func TestProjectMCPStdioBridge(t *testing.T) {
	d, s := projectMCPDaemon(t)
	peer := d.projectAgents.peer(s.ID())
	t.Setenv("MAKO_SESSION", s.ID())
	t.Setenv(agentMCPTokenEnv, peer.token)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { err := runAgentMCP([]string{d.projectAgents.socket}, inR, outW); outW.Close(); done <- err }()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "bridge", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := callProjectTool[listAgentsResult](t, cs, "list_agents", struct{}{})
	if listed.SelfID != s.ID() {
		t.Fatalf("wrong bridge identity: %+v", listed)
	}
	cs.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio bridge did not stop on EOF")
	}
}

func TestProjectAgentParentSurvivesRestore(t *testing.T) {
	d, s := projectMCPDaemon(t)
	child, err := d.spawnProjectAgent(t.Context(), d.projectAgents.peer(s.ID()), spawnAgentArgs{Prompt: "review"})
	if err != nil {
		t.Fatal(err)
	}
	restored := New(d.cfg, slog.New(slog.DiscardHandler))
	if err := restored.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	if restored.saved[child.ID].ParentID != s.ID() {
		t.Fatal("parent was not persisted")
	}
}

func TestProjectMCPCancelledInboxWait(t *testing.T) {
	d, s := projectMCPDaemon(t)
	client := projectMCPClient(t, d, s)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.CallTool(ctx, &sdk.CallToolParams{Name: "read_messages", Arguments: readMessagesArgs{WaitSeconds: 30}}); err == nil {
		t.Fatal("cancelled read did not return")
	}
	callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
}

func TestProjectMCPNegotiatesLegacyStdioClients(t *testing.T) {
	d, s := projectMCPDaemon(t)
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			client := projectMCPClientVersion(t, d, s, version)
			if client.InitializeResult().ProtocolVersion != version {
				t.Fatal("unexpected protocol negotiation")
			}
			result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_agents"})
			if err != nil || result.IsError || len(result.Content) == 0 {
				t.Fatalf("legacy result: %+v %v", result, err)
			}
			text, ok := result.Content[0].(*sdk.TextContent)
			if !ok {
				t.Fatal("legacy client did not get a text result")
			}
			var listed listAgentsResult
			if json.Unmarshal([]byte(text.Text), &listed) != nil || listed.SelfID != s.ID() {
				t.Fatalf("legacy text: %s", text.Text)
			}
		})
	}
}

func TestProjectMCPStopsAnExitedChild(t *testing.T) {
	d, s := projectMCPDaemon(t)
	client := projectMCPClient(t, d, s)
	child := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{Prompt: "short task"})
	running, _ := d.sessions.get(child.ID)
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	stopped := callProjectTool[stopAgentResult](t, client, "stop_agent", stopAgentArgs{SessionID: child.ID})
	if !stopped.Stopped {
		t.Fatal("exited child could not be removed")
	}
}

func TestProjectMCPChildrenShareTheirParentsWorktreeScope(t *testing.T) {
	d, _ := projectMCPDaemon(t)
	repo, worktree := t.TempDir(), t.TempDir()
	parent, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: worktree, Worktree: &protocol.Worktree{Repo: repo, Path: worktree, Branch: "feature"}})
	if err != nil {
		t.Fatal(err)
	}
	peer := d.projectAgents.peer(parent.ID())
	child, err := d.spawnProjectAgent(t.Context(), peer, spawnAgentArgs{Prompt: "review"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.projectTarget(peer, child.ID)
	if err != nil || projectScope(s.Info()) != projectScope(parent.Info()) {
		t.Fatalf("worktree scope: %v", err)
	}
	if s.Info().GetProject() != worktree {
		t.Fatal("child changed the working directory")
	}
}
