//go:build unix

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func TestAgentCLIMsgAndDefaultEnv(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "envs.log")

	// Fake claude binary that records AGENT_MAKO_* env vars per run and stays alive.
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
printf 'session=%s|project=%s|mode=%s|parent=%s|node=%s|profile=%s|socket=%s|token=%s\n' \
  "$AGENT_MAKO_SESSION" "$AGENT_MAKO_PROJECT" "$AGENT_MAKO_MODE" \
  "$AGENT_MAKO_PARENT" "$AGENT_MAKO_NODE" "$AGENT_MAKO_PROFILE" "$AGENT_MAKO_SOCKET" "$MAKO_AGENT_MCP_TOKEN" >> "` + envOut + `"
sleep 30
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {
				Command: []string{fakeClaude},
				Profiles: []agentspec.Profile{
					{Name: "worker", Description: "Background worker"},
				},
			},
		},
	}
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
	d.listenHooks(t.Context(), run)
	if err := d.listenAgentMCP(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	// mako-msg, mako-plugin, and mako-project shims must exist in clipTools.
	for _, shim := range []string{"mako-msg", "mako-plugin", "mako-project"} {
		if _, err := os.Stat(filepath.Join(d.clipTools, shim)); err != nil {
			t.Fatalf("expected %s shim in %s: %v", shim, d.clipTools, err)
		}
	}

	parent, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.startSessionAs(t.Context(), newSessionID(), &protocol.OpenSession{Agent: "claude", Profile: "worker", Project: project}, parent.ID())
	if err != nil {
		t.Fatal(err)
	}

	// Verify AGENT_MAKO_* env vars were injected for both main and subagent.
	waitForWithin(t, 5*time.Second, "env log did not record both sessions", func() bool {
		b, _ := os.ReadFile(envOut)
		return strings.Contains(string(b), "session="+parent.ID()) && strings.Contains(string(b), "session="+child.ID())
	})
	rawEnvs, _ := os.ReadFile(envOut)
	envText := string(rawEnvs)
	if !strings.Contains(envText, "session="+parent.ID()+"|project="+project+"|mode=main|parent=|node=node|profile=|socket="+d.projectAgents.cliSocket+"|token=") {
		t.Fatalf("unexpected parent env line:\n%s", envText)
	}
	if !strings.Contains(envText, "session="+child.ID()+"|project="+project+"|mode=subagent|parent="+parent.ID()+"|node=node|profile=worker|socket="+d.projectAgents.cliSocket+"|token=") {
		t.Fatalf("unexpected child env line:\n%s", envText)
	}

	parentPeer := d.projectAgents.peer(parent.ID())
	childPeer := d.projectAgents.peer(child.ID())
	if parentPeer == nil || childPeer == nil {
		t.Fatalf("missing peers: parent=%v child=%v", parentPeer, childPeer)
	}

	runCLI := func(peer *agentPeer, cliArgs ...string) (int, agentCLIResponse, string) {
		t.Helper()
		t.Setenv("AGENT_MAKO_SESSION", peer.id)
		t.Setenv(agentMCPTokenEnv, peer.token)
		var out, errBuf bytes.Buffer
		code := runAgentMsg(append([]string{d.projectAgents.cliSocket}, cliArgs...), strings.NewReader(""), &out, &errBuf)
		var resp agentCLIResponse
		_ = json.Unmarshal(out.Bytes(), &resp)
		return code, resp, errBuf.String()
	}

	// 1. list and profiles work via CLI.
	if code, resp, stderr := runCLI(parentPeer, "list"); code != 0 || !resp.OK || resp.Agents == nil || resp.Agents.SelfID != parent.ID() {
		t.Fatalf("list failed: code=%d resp=%+v stderr=%q", code, resp, stderr)
	}
	if code, resp, stderr := runCLI(parentPeer, "profiles"); code != 0 || !resp.OK || resp.Profiles == nil || len(resp.Profiles.Presets) == 0 {
		t.Fatalf("profiles failed: code=%d resp=%+v stderr=%q", code, resp, stderr)
	}

	// 2. Concurrent `mako-msg wait` blocks until child sends a message, then returns and auto-acks it.
	waitDone := make(chan struct {
		code int
		resp agentCLIResponse
	}, 1)
	go func() {
		// Connect directly so we don't mutate process env concurrently with the sender below.
		conn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
		if err != nil {
			waitDone <- struct {
				code int
				resp agentCLIResponse
			}{code: 1}
			return
		}
		defer conn.Close()
		_ = json.NewEncoder(conn).Encode(agentCLIRequest{
			Session:    parentPeer.id,
			Token:      parentPeer.token,
			Op:         "wait",
			From:       child.ID(),
			TimeoutSec: 10,
			Ack:        true,
		})
		var resp agentCLIResponse
		_ = json.NewDecoder(conn).Decode(&resp)
		waitDone <- struct {
			code int
			resp agentCLIResponse
		}{code: 0, resp: resp}
	}()

	time.Sleep(100 * time.Millisecond)
	if code, resp, stderr := runCLI(childPeer, "send", parent.ID(), "all", "tests", "passed"); code != 0 || !resp.OK || resp.Sent == nil {
		t.Fatalf("send failed: code=%d resp=%+v stderr=%q", code, resp, stderr)
	}

	select {
	case got := <-waitDone:
		if got.code != 0 || !got.resp.OK || got.resp.Reason != "message" || len(got.resp.Messages) != 1 || got.resp.Messages[0].Text != "all tests passed" {
			t.Fatalf("unexpected wait result: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mako-msg wait timed out waiting for message")
	}

	// Auto-ack must have cleared the inbox.
	if code, resp, stderr := runCLI(parentPeer, "read"); code != 0 || !resp.OK || len(resp.Messages) != 0 {
		t.Fatalf("expected empty inbox after auto-ack wait: code=%d resp=%+v stderr=%q", code, resp, stderr)
	}

	// 3. `mako-msg wait --session <child>` wakes when child ends its turn.
	child.SetStatus(protocol.AgentWorking)
	turnDone := make(chan agentCLIResponse, 1)
	go func() {
		conn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
		if err != nil {
			turnDone <- agentCLIResponse{Error: err.Error()}
			return
		}
		defer conn.Close()
		_ = json.NewEncoder(conn).Encode(agentCLIRequest{
			Session:    parentPeer.id,
			Token:      parentPeer.token,
			Op:         "wait",
			WatchID:    child.ID(),
			TimeoutSec: 10,
			Ack:        true,
		})
		var resp agentCLIResponse
		_ = json.NewDecoder(conn).Decode(&resp)
		turnDone <- resp
	}()
	time.Sleep(100 * time.Millisecond)
	child.SetStatus(protocol.AgentDone)

	select {
	case resp := <-turnDone:
		if !resp.OK || resp.Reason != "turn_ended" || resp.SessionID != child.ID() {
			t.Fatalf("expected turn_ended for child %s, got %+v", child.ID(), resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mako-msg wait --session timed out")
	}

	// 4. `mako-msg wait --all-children` watches multiple children and reports whichever finishes first,
	//    and registers an active CLI waiter while blocked.
	child2, err := d.startSessionAs(t.Context(), newSessionID(), &protocol.OpenSession{Agent: "claude", Profile: "worker", Project: project}, parent.ID())
	if err != nil {
		t.Fatal(err)
	}
	child.SetStatus(protocol.AgentWorking)
	child2.SetStatus(protocol.AgentWorking)
	allDone := make(chan agentCLIResponse, 1)
	go func() {
		conn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
		if err != nil {
			allDone <- agentCLIResponse{Error: err.Error()}
			return
		}
		defer conn.Close()
		_ = json.NewEncoder(conn).Encode(agentCLIRequest{
			Session:       parentPeer.id,
			Token:         parentPeer.token,
			Op:            "wait",
			WatchChildren: true,
			TimeoutSec:    10,
			Ack:           true,
		})
		var resp agentCLIResponse
		_ = json.NewDecoder(conn).Decode(&resp)
		allDone <- resp
	}()
	waitForWithin(t, 3*time.Second, "cli waiter never registered", func() bool {
		return d.projectAgents.hasCLIWaiter(parent.ID())
	})
	child2.SetStatus(protocol.AgentDone)
	select {
	case resp := <-allDone:
		if !resp.OK || resp.Reason != "turn_ended" || resp.SessionID != child2.ID() {
			t.Fatalf("expected turn_ended for child2 %s, got %+v", child2.ID(), resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mako-msg wait --all-children timed out")
	}
	waitForWithin(t, 3*time.Second, "cli waiter not released", func() bool {
		return !d.projectAgents.hasCLIWaiter(parent.ID())
	})
}

func TestAgySidecarNativeDelivery(t *testing.T) {
	dir := t.TempDir()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.dir, d.ctx = dir, t.Context()
	if err := d.projectAgents.messages.load(dir); err != nil {
		t.Fatal(err)
	}
	run, err := config.RunDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.listenAgentCLI(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	tokenBytes, err := os.ReadFile(filepath.Join(run, sidecarTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	// Connect a mock agy sidecar subscriber to agent-cli.sock.
	received := make(chan sidecarPush, 1)
	sidecarConn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sidecarConn.Close()
	if err := json.NewEncoder(sidecarConn).Encode(agentCLIRequest{Op: "sidecar_subscribe", Token: strings.TrimSpace(string(tokenBytes))}); err != nil {
		t.Fatal(err)
	}
	go func() {
		dec := json.NewDecoder(bufio.NewReader(sidecarConn))
		enc := json.NewEncoder(sidecarConn)
		var push sidecarPush
		if err := dec.Decode(&push); err == nil {
			received <- push
			_ = enc.Encode(sidecarAck{ID: push.ID, OK: true})
		}
	}()
	waitForWithin(t, 3*time.Second, "sidecar never registered", func() bool {
		d.projectAgents.mu.Lock()
		defer d.projectAgents.mu.Unlock()
		return d.projectAgents.agySidecar != nil
	})

	// Start a dummy agy session (working, not at prompt!) with a conversation ID.
	s, err := session.Start(session.Spec{
		ID: "agy-main", NodeID: "test-node", Agent: "agy", Project: dir,
		Command: []string{"sh", "-c", "sleep 30"},
		Env:     map[string]string{"HOME": dir},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetConversation("conv-agy-42", "")
	s.SetStatus(protocol.AgentWorking) // even mid-turn, native push delivers immediately
	d.sessions.add(s)

	_, release := d.projectAgents.server(t.Context(), "", s.ID(), "")
	defer release()
	peer := d.projectAgents.activate(s)
	go d.deliverAgentMessages(peer)

	if _, err := d.projectAgents.messages.send("worker-1", s.ID(), "PR is ready for review", ""); err != nil {
		t.Fatal(err)
	}

	select {
	case push := <-received:
		if push.SessionID != "agy-main" || push.ConversationID != "conv-agy-42" || !strings.Contains(push.Text, "PR is ready for review") || push.Home != dir {
			t.Fatalf("unexpected sidecar push: %+v", push)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agy sidecar push")
	}

	waitForWithin(t, 3*time.Second, "inbox not cleared after native delivery", func() bool {
		inbox, _ := d.projectAgents.messages.pending(s.ID())
		return len(inbox) == 0
	})
}

func TestAgentPluginCLI(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project := t.TempDir()

	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {Command: []string{fakeClaude}},
		},
	}
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
	d.listenHooks(t.Context(), run)
	if err := d.listenAgentMCP(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	d.workspace.load(d.dir, d.config().NodeID)
	setTestWorkspaceSpaceAndProjects(t, d, project, t.TempDir())

	// Start plugin manager with a simple tool plugin that accepts typed params and stdin text.
	pDir := filepath.Join(d.dir, "plugins", "echo")
	if err := os.MkdirAll(pDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `id = "echo"
version = "0.1.0"
publisher = "local"
api = 1
entry = "main.js"
permissions = []

[[contributes.tool]]
id = "combine"
description = "Combine a title, repeat count, and body"
scope = "project"
[contributes.tool.params]
title = { type = "string", required = true, description = "Heading text" }
count = { type = "number", description = "Repeat count" }
body = { type = "string", description = "Body text" }
`
	mainJS := `export default {
  activate(mako) {
    mako.tools.register("combine", (args, ctx) => ({
      scope: ctx.scopeKey,
      title: args.title,
      count: args.count,
      body: args.body,
    }));
  },
};
`
	if err := os.WriteFile(filepath.Join(pDir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pDir, "main.js"), []byte(mainJS), 0o644); err != nil {
		t.Fatal(err)
	}
	d.startPlugins(t.Context(), d.dir)
	waitForWithin(t, 5*time.Second, "echo plugin did not become ready", func() bool {
		for _, p := range d.plugins.List() {
			if p.GetId() == "echo" && (p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning) {
				return true
			}
		}
		return false
	})

	sess, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	peer := d.projectAgents.peer(sess.ID())
	if peer == nil {
		t.Fatal("missing session peer")
	}

	runPluginCLI := func(stdin string, cliArgs ...string) (int, string, string) {
		t.Helper()
		t.Setenv("AGENT_MAKO_SESSION", peer.id)
		t.Setenv(agentMCPTokenEnv, peer.token)
		var out, errBuf bytes.Buffer
		code := runAgentPlugin(append([]string{d.projectAgents.cliSocket}, cliArgs...), strings.NewReader(stdin), &out, &errBuf)
		return code, out.String(), errBuf.String()
	}

	// 1. list shows echo combine
	code, out, stderr := runPluginCLI("", "list")
	if code != 0 || !strings.Contains(out, "echo") || !strings.Contains(out, "combine") {
		t.Fatalf("list failed: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// 2. help shows parameters
	code, out, stderr = runPluginCLI("", "help", "echo", "combine")
	if code != 0 || !strings.Contains(out, "--title <string, required>") || !strings.Contains(out, "--count <number>") {
		t.Fatalf("help failed: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// 3. call with flags, typed integer coercion, and --body - reading from stdin
	code, out, stderr = runPluginCLI("# Markdown Body\nLine 2", "call", "echo", "combine", "--title", "Hello", "--count", "3", "--body", "-")
	if code != 0 {
		t.Fatalf("call flags failed: code=%d out=%q stderr=%q", code, out, stderr)
	}
	var got struct {
		Scope string `json:"scope"`
		Title string `json:"title"`
		Count int    `json:"count"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json output %q: %v", out, err)
	}
	if got.Title != "Hello" || got.Count != 3 || got.Body != "# Markdown Body\nLine 2" || !strings.HasPrefix(got.Scope, "project:") {
		t.Fatalf("unexpected call result: %+v", got)
	}

	// 4. call with --json - reading JSON payload from stdin
	code, out, stderr = runPluginCLI(`{"title":"FromJSON","count":7,"body":"ok"}`, "call", "echo", "combine", "--json", "-")
	if code != 0 || !strings.Contains(out, `"FromJSON"`) || !strings.Contains(out, `"count": 7`) {
		t.Fatalf("call --json failed: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// 5. AGENT_MAKO_PLUGINS restricts visible and callable tools per environment.
	t.Setenv("AGENT_MAKO_PLUGINS", "other")
	code, out, stderr = runPluginCLI("", "list")
	if code != 0 || strings.Contains(out, "combine") || !strings.Contains(out, "No plugin tools available.") {
		t.Fatalf("list with AGENT_MAKO_PLUGINS=other should hide echo: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, _, stderr = runPluginCLI("", "call", "echo", "combine", "--title", "Blocked")
	if code == 0 || !strings.Contains(stderr, "not enabled in this environment") {
		t.Fatalf("call should be blocked by AGENT_MAKO_PLUGINS=other: code=%d stderr=%q", code, stderr)
	}

	t.Setenv("AGENT_MAKO_PLUGINS", "echo:combine")
	code, out, stderr = runPluginCLI("", "list")
	if code != 0 || !strings.Contains(out, "combine") {
		t.Fatalf("list with AGENT_MAKO_PLUGINS=echo:combine should show echo combine: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runPluginCLI("", "call", "echo", "combine", "--title", "Allowed", "--count", "1")
	if code != 0 || !strings.Contains(out, `"Allowed"`) {
		t.Fatalf("call with AGENT_MAKO_PLUGINS=echo:combine failed: code=%d out=%q stderr=%q", code, out, stderr)
	}

	t.Setenv("AGENT_MAKO_PLUGINS", "none")
	code, out, stderr = runPluginCLI("", "list")
	if code != 0 || strings.Contains(out, "combine") {
		t.Fatalf("list with AGENT_MAKO_PLUGINS=none should hide all tools: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestAgySidecarRejectsUnauthorized(t *testing.T) {
	dir := t.TempDir()
	d := New(config.Default(), slog.New(slog.DiscardHandler))
	d.dir, d.ctx = dir, t.Context()
	run, err := config.RunDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := d.listenAgentCLI(ctx, run); err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(run, sidecarTokenFile)
	fi, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar.token mode = %o, want 0600", fi.Mode().Perm())
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}

	// Register a valid sidecar first.
	validConn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer validConn.Close()
	if err := json.NewEncoder(validConn).Encode(agentCLIRequest{Op: "sidecar_subscribe", Token: strings.TrimSpace(string(tokenBytes))}); err != nil {
		t.Fatal(err)
	}
	var validClient *agySidecarClient
	waitForWithin(t, 2*time.Second, "valid sidecar never registered", func() bool {
		d.projectAgents.mu.Lock()
		defer d.projectAgents.mu.Unlock()
		validClient = d.projectAgents.agySidecar
		return validClient != nil
	})

	// Unauthorized callers (empty token and wrong token) must be rejected without
	// disturbing the active valid sidecar.
	for _, badToken := range []string{"", "deadbeef"} {
		badConn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(badConn).Encode(agentCLIRequest{Op: "sidecar_subscribe", Token: badToken}); err != nil {
			badConn.Close()
			t.Fatal(err)
		}
		var resp agentCLIResponse
		if err := json.NewDecoder(badConn).Decode(&resp); err != nil {
			badConn.Close()
			t.Fatalf("expected JSON rejection for token %q: %v", badToken, err)
		}
		badConn.Close()
		if resp.OK || !strings.Contains(resp.Error, "unauthorized") {
			t.Fatalf("unexpected response for token %q: %+v", badToken, resp)
		}
	}

	d.projectAgents.mu.Lock()
	stillValid := d.projectAgents.agySidecar == validClient
	d.projectAgents.mu.Unlock()
	if !stillValid {
		t.Fatal("unauthorized sidecar attempt displaced active valid sidecar")
	}

	// Shutting down ctx cleans up sidecar.token.
	cancel()
	waitForWithin(t, 2*time.Second, "sidecar.token not cleaned up on shutdown", func() bool {
		_, err := os.Stat(tokenPath)
		return os.IsNotExist(err)
	})
}

func TestAgySidecarEOFTeardown(t *testing.T) {
	dir := t.TempDir()
	d := New(config.Default(), slog.New(slog.DiscardHandler))
	d.dir, d.ctx = dir, t.Context()
	run, err := config.RunDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	if err := d.listenAgentCLI(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(filepath.Join(run, sidecarTokenFile))
	if err != nil {
		t.Fatal(err)
	}

	sidecarConn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(sidecarConn).Encode(agentCLIRequest{Op: "sidecar_subscribe", Token: strings.TrimSpace(string(tokenBytes))}); err != nil {
		t.Fatal(err)
	}
	waitForWithin(t, 2*time.Second, "sidecar never registered", func() bool {
		d.projectAgents.mu.Lock()
		defer d.projectAgents.mu.Unlock()
		return d.projectAgents.agySidecar != nil
	})

	// Closing the sidecar connection must clear d.projectAgents.agySidecar promptly
	// without waiting for ctx.Done().
	_ = sidecarConn.Close()
	waitForWithin(t, 2*time.Second, "sidecar not cleared on EOF", func() bool {
		d.projectAgents.mu.Lock()
		defer d.projectAgents.mu.Unlock()
		return d.projectAgents.agySidecar == nil
	})
}

type noopSidecarDeliverer struct{}

func (noopSidecarDeliverer) DeliverMessage(context.Context, string, string, string, map[string]string) error {
	return nil
}

func TestRunAgySidecarExitsWhenSocketGone(t *testing.T) {
	dir := t.TempDir()
	missingSock := filepath.Join(dir, "agent-cli.sock")

	errCh := make(chan error, 1)
	go func() {
		errCh <- runAgySidecarLoop(t.Context(), missingSock, noopSidecarDeliverer{}, io.Discard, sidecarRetryPolicy{
			initial:    15 * time.Millisecond,
			max:        30 * time.Millisecond,
			maxMissing: 80 * time.Millisecond,
		})
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error when sidecar socket is missing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAgySidecarLoop did not exit when socket stayed missing")
	}

	// Also verify unauthorized rejection exits immediately without waiting maxMissing.
	d := New(config.Default(), slog.New(slog.DiscardHandler))
	d.dir, d.ctx = dir, t.Context()
	run, err := config.RunDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	if err := d.listenAgentCLI(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	// Overwrite sidecar.token with an invalid token.
	if err := os.WriteFile(filepath.Join(run, sidecarTokenFile), []byte("wrong-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	authErrCh := make(chan error, 1)
	go func() {
		authErrCh <- runAgySidecarLoop(t.Context(), d.projectAgents.cliSocket, noopSidecarDeliverer{}, io.Discard, sidecarRetryPolicy{
			initial:    500 * time.Millisecond,
			max:        500 * time.Millisecond,
			maxMissing: 10 * time.Second,
		})
	}()
	select {
	case err := <-authErrCh:
		if !errors.Is(err, errSidecarUnauthorized) {
			t.Fatalf("expected errSidecarUnauthorized, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAgySidecarLoop did not exit promptly on auth rejection")
	}
}

func TestServeAgentCLIDoesNotLeakGoroutines(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project := t.TempDir()
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {Command: []string{fakeClaude}},
		},
	}
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

	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	peer := d.projectAgents.peer(s.ID())
	if peer == nil {
		t.Fatal("expected agent peer")
	}

	callList := func() {
		t.Helper()
		conn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
		if err != nil {
			t.Fatalf("dial agent-cli: %v", err)
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(agentCLIRequest{
			Session: peer.id,
			Token:   peer.token,
			Op:      "list",
		}); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		var resp agentCLIResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !resp.OK {
			t.Fatalf("unexpected error response: %s", resp.Error)
		}
	}

	// Warm up before measuring baseline goroutines.
	callList()
	callList()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const calls = 40
	for range calls {
		callList()
	}

	waitForWithin(t, 3*time.Second, "serveAgentCLI leaked goroutines across completed requests", func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= before+8
	})
}

func TestAgentProjectCLIAddLocalAndGitURL(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	sessionDir := t.TempDir()
	defaultStore := filepath.Join(t.TempDir(), "default-projects")
	secondStore := filepath.Join(t.TempDir(), "work-projects")

	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		NodeID:      "node",
		ProjectDirs: []string{defaultStore, secondStore},
		Agents: map[string]agentspec.Agent{
			"claude": {Command: []string{fakeClaude}},
		},
	}
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
	d.listenHooks(t.Context(), run)
	if err := d.listenAgentMCP(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	d.workspace.load(d.dir, d.config().NodeID)

	sess, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: sessionDir})
	if err != nil {
		t.Fatal(err)
	}
	peer := d.projectAgents.peer(sess.ID())
	if peer == nil {
		t.Fatal("missing session peer")
	}

	runProjectCLI := func(cliArgs ...string) (int, string, string) {
		t.Helper()
		t.Setenv("AGENT_MAKO_SESSION", peer.id)
		t.Setenv(agentMCPTokenEnv, peer.token)
		var out, errBuf bytes.Buffer
		code := runAgentProject(append([]string{d.projectAgents.cliSocket}, cliArgs...), strings.NewReader(""), &out, &errBuf)
		return code, out.String(), errBuf.String()
	}

	// 1. dirs lists configured default project storage locations.
	code, out, stderr := runProjectCLI("dirs", "--json")
	if code != 0 {
		t.Fatalf("dirs failed: code=%d stderr=%q", code, stderr)
	}
	var dirs []string
	if err := json.Unmarshal([]byte(out), &dirs); err != nil || len(dirs) != 2 || dirs[0] != defaultStore {
		t.Fatalf("unexpected dirs output %q (err=%v)", out, err)
	}

	// 2. add local directory registers it into workspace CRDT.
	localProj := filepath.Join(t.TempDir(), "my-local-app")
	if err := os.MkdirAll(localProj, 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runProjectCLI("add", localProj, "--name", "Local App", "--json")
	if code != 0 {
		t.Fatalf("add local failed: code=%d stderr=%q", code, stderr)
	}
	var addedLocal agentProjectAddResult
	if err := json.Unmarshal([]byte(out), &addedLocal); err != nil {
		t.Fatalf("invalid json %q: %v", out, err)
	}
	if addedLocal.Name != "Local App" || addedLocal.Cloned || addedLocal.AlreadyExisted {
		t.Fatalf("unexpected addedLocal: %+v", addedLocal)
	}

	// 3. Re-adding the same local directory deduplicates cleanly.
	code, out, stderr = runProjectCLI("add", localProj, "--json")
	if code != 0 {
		t.Fatalf("re-add local failed: code=%d stderr=%q", code, stderr)
	}
	var addedAgain agentProjectAddResult
	if err := json.Unmarshal([]byte(out), &addedAgain); err != nil || !addedAgain.AlreadyExisted || addedAgain.ID != addedLocal.ID {
		t.Fatalf("expected AlreadyExisted with id %s, got %+v (err=%v)", addedLocal.ID, addedAgain, err)
	}

	// 4. list shows the registered project.
	code, out, stderr = runProjectCLI("list", "--json")
	if code != 0 {
		t.Fatalf("list failed: code=%d stderr=%q", code, stderr)
	}
	var listed []agentProjectSummary
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 || listed[0].ID != addedLocal.ID {
		t.Fatalf("unexpected list output: %q (err=%v)", out, err)
	}

	// 5. add with a Git URL clones into the first configured project_dirs and registers it.
	if _, err := exec.LookPath("git"); err == nil {
		bare := filepath.Join(t.TempDir(), "cli-repo.git")
		if o, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
			t.Fatalf("git init --bare: %v: %s", err, o)
		}
		seed := filepath.Join(t.TempDir(), "seed")
		if err := os.MkdirAll(seed, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", seed}, args...)...)
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
				"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			)
			if o, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, o)
			}
		}
		runGit("init")
		if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("cli\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "README.md")
		runGit("commit", "-m", "init")
		runGit("remote", "add", "origin", bare)
		runGit("push", "-u", "origin", "HEAD")

		code, out, stderr = runProjectCLI("add", "file://"+bare, "--json")
		if code != 0 {
			t.Fatalf("add git URL failed: code=%d stderr=%q", code, stderr)
		}
		var addedGit agentProjectAddResult
		if err := json.Unmarshal([]byte(out), &addedGit); err != nil {
			t.Fatalf("invalid json %q: %v", out, err)
		}
		if !addedGit.Cloned || filepath.Base(addedGit.Path) != "cli-repo" {
			t.Fatalf("unexpected addedGit: %+v", addedGit)
		}
		if _, err := os.Stat(filepath.Join(defaultStore, "cli-repo", "README.md")); err != nil {
			t.Fatalf("expected clone inside defaultStore: %v", err)
		}
	}
}
