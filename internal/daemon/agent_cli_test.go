//go:build unix

package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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

	// mako-msg shim must exist in clipTools.
	if _, err := os.Stat(filepath.Join(d.clipTools, "mako-msg")); err != nil {
		t.Fatalf("expected mako-msg shim in %s: %v", d.clipTools, err)
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

	// Connect a mock agy sidecar subscriber to agent-cli.sock.
	received := make(chan sidecarPush, 1)
	sidecarConn, err := net.DialTimeout("unix", d.projectAgents.cliSocket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sidecarConn.Close()
	if err := json.NewEncoder(sidecarConn).Encode(agentCLIRequest{Op: "sidecar_subscribe"}); err != nil {
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
