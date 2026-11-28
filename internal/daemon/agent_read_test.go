//go:build unix

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// A conversation groups into turns at each user message: what came before
// the first is a turn begun before the read, and the last is in progress
// while the agent works on it or a call of it is unanswered.
func TestAgentTurnsGroupAtUserMessages(t *testing.T) {
	msgs := []*protocol.ChatMessage{
		{Role: "tool", Tool: "Read", Detail: "/a.go", Result: "package a"},
		{Role: "assistant", Text: "Read it."},
		{Role: "user", Text: "Fix the tests"},
		{Role: "assistant", Text: "  "},
		{Role: "assistant", Text: "Running them."},
		{Role: "tool", Tool: "Bash", Kind: "command", Detail: "go test ./...", Result: "FAIL", Error: true},
		{Role: "user", Text: "Again"},
		{Role: "tool", Tool: "Bash", Detail: "go test ./...", Pending: true},
	}
	turns := agentTurns(msgs, false)
	if len(turns) != 3 {
		t.Fatalf("turns: %+v", turns)
	}
	if turns[0].Message != "" || len(turns[0].Parts) != 2 || turns[0].State != turnComplete {
		t.Fatalf("leading turn: %+v", turns[0])
	}
	second := turns[1]
	if second.Message != "Fix the tests" || len(second.Parts) != 2 || second.State != turnComplete {
		t.Fatalf("second turn: %+v", second)
	}
	if p := second.Parts[1]; p.Kind != partToolCall || p.Tool != "Bash" || p.Call != "command" || p.Detail != "go test ./..." || p.Result != "FAIL" || !p.Error {
		t.Fatalf("tool part: %+v", p)
	}
	if turns[2].State != turnInProgress {
		t.Fatal("a turn with an unanswered call is not in progress")
	}
	if agentTurns(msgs[:7], true)[2].State != turnInProgress {
		t.Fatal("the turn of an agent at work is not in progress")
	}
	if agentTurns(nil, true) != nil {
		t.Fatal("an empty conversation has turns")
	}
}

// Long texts are clipped, a turn keeps its latest parts, and the turns kept
// fit the budget, the latest always among them.
func TestAgentTurnsAreBounded(t *testing.T) {
	long := strings.Repeat("x", readAgentText*2)
	msgs := []*protocol.ChatMessage{{Role: "user", Text: long}}
	for i := range readAgentParts + 5 {
		msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: fmt.Sprintf("part %d", i)})
	}
	turns := agentTurns(msgs, false)
	if got := len([]rune(turns[0].Message)); got != readAgentText {
		t.Fatalf("message clipped to %d runes", got)
	}
	if len(turns[0].Parts) != readAgentParts || turns[0].OmittedParts != 5 || turns[0].Parts[readAgentParts-1].Text != fmt.Sprintf("part %d", readAgentParts+4) {
		t.Fatalf("parts kept: %d, omitted %d", len(turns[0].Parts), turns[0].OmittedParts)
	}

	big := func(n int) agentTurn {
		return agentTurn{Message: "m", Parts: []agentPart{{Kind: partText, Text: strings.Repeat("y", n)}, {Kind: partText, Text: "last"}}}
	}
	kept, dropped := fitTurns([]agentTurn{big(600), big(600), big(100)}, 700)
	if len(kept) != 1 || dropped != 2 || kept[0].Parts[0].Text != strings.Repeat("y", 100) {
		t.Fatalf("kept %d, dropped %d", len(kept), dropped)
	}
	kept, dropped = fitTurns([]agentTurn{big(2000)}, 1000)
	if len(kept) != 1 || dropped != 0 || len(kept[0].Parts) != 1 || kept[0].Parts[0].Text != "last" || kept[0].OmittedParts != 1 {
		t.Fatalf("an oversized latest turn was not cut to its latest parts: %+v", kept)
	}
}

// read_agent reads a sibling's session and latest turns, and nothing of an
// agent outside the project.
func TestProjectMCPReadAgent(t *testing.T) {
	d, caller := projectMCPDaemon(t)
	client := projectMCPClient(t, d, caller)
	sibling, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: caller.Info().GetProject()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.renameSession(sibling.ID(), "Reviewer"); err != nil {
		t.Fatal(err)
	}
	waitConversation(t, sibling)
	at := time.Now().UTC()
	line := func(v map[string]any) string {
		at = at.Add(time.Second)
		v["timestamp"] = at.Format(time.RFC3339Nano)
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	assistant := func(content ...map[string]any) string {
		return line(map[string]any{"type": "assistant", "message": map[string]any{"id": fmt.Sprintf("m%d", at.UnixNano()), "role": "assistant", "content": content}})
	}
	log := assistant(map[string]any{"type": "text", "text": "Looking at the tests."}, map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}}) +
		assistant(map[string]any{"type": "text", "text": "All tests pass."}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Now review the docs"}}) +
		assistant(map[string]any{"type": "text", "text": "Reading the docs."})
	f, err := os.OpenFile(sibling.TranscriptPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(log); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sibling.SetStatus(protocol.AgentWorking)

	var read readAgentResult
	waitFor(t, "the sibling's latest turn was not read", func() bool {
		read = callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID()})
		n := len(read.Turns)
		return n > 0 && read.Turns[n-1].Message == "Now review the docs"
	})
	s := read.Session
	if s.ID != sibling.ID() || s.Agent != "claude" || s.Name != "Reviewer" || s.Status != protocol.AgentWorking || !s.Messaging || s.Folder != caller.Info().GetProject() || s.Node != "" {
		t.Fatalf("session: %+v", s)
	}
	if len(read.Turns) != 2 || read.ChatUnavailable != "" {
		t.Fatalf("turns: %+v (%s)", read.Turns, read.ChatUnavailable)
	}
	first, last := read.Turns[0], read.Turns[1]
	if first.Message != "original conversation" || first.State != turnComplete || len(first.Parts) != 3 {
		t.Fatalf("first turn: %+v", first)
	}
	if p := first.Parts[1]; p.Kind != partToolCall || p.Tool != "Bash" || !strings.Contains(p.Detail, "go test") || p.Result != "ok" {
		t.Fatalf("tool call: %+v", p)
	}
	if last.State != turnInProgress || len(last.Parts) != 1 || last.Parts[0].Text != "Reading the docs." {
		t.Fatalf("last turn: %+v", last)
	}

	one := 1
	latest := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &one})
	if len(latest.Turns) != 1 || latest.EarlierTurns != 1 || latest.Turns[0].Message != "Now review the docs" {
		t.Fatalf("turns=1: %+v", latest)
	}
	none := 0
	alone := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &none})
	if len(alone.Turns) != 0 || alone.Session.ID != sibling.ID() || alone.Session.Usage != nil {
		t.Fatalf("turns=0 read the conversation: %+v", alone)
	}

	sibling.SetStatus(protocol.AgentIdle)
	if done := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID()}); done.Turns[len(done.Turns)-1].State != turnComplete {
		t.Fatal("the last turn of an idle agent is still in progress")
	}

	tooMany := readAgentMaxTurns + 1
	negative := -1
	for _, args := range []any{
		readAgentArgs{SessionID: other.ID()},
		readAgentArgs{SessionID: "no-such-session"},
		readAgentArgs{SessionID: ""},
		readAgentArgs{SessionID: sibling.ID(), Turns: &tooMany},
		readAgentArgs{SessionID: sibling.ID(), Turns: &negative},
	} {
		result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "read_agent", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatalf("read_agent accepted %+v", args)
		}
	}
}

// What an agent waits on reads from its approval, its questions, or the
// dialog in its terminal.
func TestAgentWaitingOf(t *testing.T) {
	if agentWaitingOf(&protocol.SessionInfo{}) != nil {
		t.Fatal("an agent waiting on nothing waits")
	}
	w := agentWaitingOf(&protocol.SessionInfo{Approval: &protocol.Approval{Tool: "Bash", Input: `{"command": "rm -rf build"}`}})
	if w == nil || w.Kind != "approval" || w.Tool != "Bash" || !strings.Contains(w.Detail, "rm -rf build") {
		t.Fatalf("approval: %+v", w)
	}
	w = agentWaitingOf(&protocol.SessionInfo{Approval: &protocol.Approval{Tool: "Bash", Detail: "go test ./...", Input: `{"secret": "hidden"}`}})
	if w == nil || w.Kind != "approval" || w.Detail != "go test ./..." {
		t.Fatalf("approval detail precedence: %+v", w)
	}
	w = agentWaitingOf(&protocol.SessionInfo{Approval: &protocol.Approval{Tool: "AskUserQuestion", Questions: []*protocol.Question{{Question: "Which database?"}}}})
	if w == nil || w.Kind != "question" || len(w.Questions) != 1 || w.Questions[0] != "Which database?" {
		t.Fatalf("question: %+v", w)
	}
	w = agentWaitingOf(&protocol.SessionInfo{Dialog: &protocol.ScreenDialog{Text: "Trust this folder?", Options: []string{"Yes", "No"}}})
	if w == nil || w.Kind != "dialog" || len(w.Options) != 2 {
		t.Fatalf("dialog: %+v", w)
	}
}

// A pending tool call earlier in a turn with more than readAgentParts parts
// still marks the turn in_progress even after older parts are omitted.
func TestAgentTurnsPendingPreservedBeforePartTrim(t *testing.T) {
	msgs := []*protocol.ChatMessage{
		{Role: "user", Text: "long turn"},
		{Role: "tool", Tool: "Bash", Detail: "long-running", Pending: true},
	}
	for i := range readAgentParts {
		msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: fmt.Sprintf("step %d", i)})
	}
	turns := agentTurns(msgs, false)
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if len(turns[0].Parts) != readAgentParts || turns[0].OmittedParts != 1 {
		t.Fatalf("parts = %d, omitted = %d", len(turns[0].Parts), turns[0].OmittedParts)
	}
	if turns[0].State != turnInProgress {
		t.Fatalf("state = %q, want %q when an omitted earlier tool call is pending", turns[0].State, turnInProgress)
	}
}

// Changing .git/config mid-session (e.g., git remote set-url origin <victim>)
// cannot move a running session (or a child it spawns) into another repo:'s
// scope for read_agent, list_agents, send_message, or spawn_agent.
func TestGitRemoteSpoofingCannotJoinAnotherRepoScope(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	d, _ := projectMCPDaemon(t)
	victimDir := t.TempDir()
	runGit(victimDir, "init", "-q", "-b", "main")
	runGit(victimDir, "remote", "add", "origin", "https://github.com/acme/victim.git")

	attackerDir := t.TempDir()
	runGit(attackerDir, "init", "-q", "-b", "main")
	runGit(attackerDir, "remote", "add", "origin", "https://github.com/acme/attacker.git")

	plainDir := t.TempDir() // not a git repo at session start

	victim, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: victimDir})
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: attackerDir})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: plainDir})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, victim)

	// Spoof origin in attackerDir and initialize a spoofed git repo in plainDir mid-session.
	runGit(attackerDir, "remote", "set-url", "origin", "https://github.com/acme/victim.git")
	runGit(plainDir, "init", "-q", "-b", "main")
	runGit(plainDir, "remote", "add", "origin", "https://github.com/acme/victim.git")

	// Expire the daemon's heads cache so d.heads.of re-reads the modified .git/config.
	d.heads.mu.Lock()
	d.heads.known = nil
	d.heads.mu.Unlock()
	if got := d.withHead(attacker.Info()).GetRemote(); got != "github.com/acme/victim" {
		t.Fatalf("precondition: expected refreshed remote github.com/acme/victim, got %q", got)
	}
	if got := d.withHead(plain.Info()).GetRemote(); got != "github.com/acme/victim" {
		t.Fatalf("precondition: expected refreshed plain remote github.com/acme/victim, got %q", got)
	}

	// Verify shareScopes and projectScope keep the pinned scopes.
	if got := d.projectScope(attacker.Info()); got != repoScopePrefix+"github.com/acme/attacker" {
		t.Fatalf("attacker scope drifted to %q", got)
	}
	if got := d.projectScope(plain.Info()); got != folderScope(plain.Info()) {
		t.Fatalf("plain scope drifted to %q", got)
	}
	listedInfos := d.listSessions()
	d.shareScopes(listedInfos)
	for _, info := range listedInfos {
		if info.GetId() == attacker.ID() && info.GetAgentScope() != repoScopePrefix+"github.com/acme/attacker" {
			t.Fatalf("attacker AgentScope drifted to %q", info.GetAgentScope())
		}
		if info.GetId() == plain.ID() && info.GetAgentScope() != "" {
			t.Fatalf("plain AgentScope drifted to %q", info.GetAgentScope())
		}
	}

	// Neither attacker nor plain can list, read, message, or spawn into victim's scope.
	for _, tc := range []struct {
		name string
		sess *protocol.SessionInfo
		cs   *sdk.ClientSession
	}{
		{"attacker", attacker.Info(), projectMCPClient(t, d, attacker)},
		{"plain", plain.Info(), projectMCPClient(t, d, plain)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := callProjectTool[listAgentsResult](t, tc.cs, "list_agents", struct{}{})
			if slices.ContainsFunc(listed.Agents, func(a agentSummary) bool { return a.ID == victim.ID() }) {
				t.Fatalf("spoofed session listed victim: %+v", listed.Agents)
			}
			for _, call := range []sdk.CallToolParams{
				{Name: "read_agent", Arguments: readAgentArgs{SessionID: victim.ID()}},
				{Name: "send_message", Arguments: sendMessageArgs{To: victim.ID(), Text: "spoofed"}},
				{Name: "spawn_agent", Arguments: spawnAgentArgs{Folder: victimDir, Prompt: "escape"}},
			} {
				res, err := tc.cs.CallTool(t.Context(), &call)
				if err == nil && !res.IsError {
					t.Fatalf("spoofed session succeeded with %+v", call)
				}
			}
			// Spawning a child in its own folder inherits the pinned scope and still cannot read victim.
			childSum := callProjectTool[agentSummary](t, tc.cs, "spawn_agent", spawnAgentArgs{Prompt: "try reading victim"})
			child, ok := d.sessions.get(childSum.ID)
			if !ok {
				t.Fatal("spawned child missing")
			}
			childClient := projectMCPClient(t, d, child)
			res, err := childClient.CallTool(t.Context(), &sdk.CallToolParams{Name: "read_agent", Arguments: readAgentArgs{SessionID: victim.ID()}})
			if err == nil && !res.IsError {
				t.Fatal("child spawned after .git/config edit escaped into victim scope")
			}
		})
	}

	// Pinned remotes persist across daemon reload.
	restored := New(d.config(), nil)
	if err := restored.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	if got := restored.projectScope(restored.saved[attacker.ID()].Info); got != repoScopePrefix+"github.com/acme/attacker" {
		t.Fatalf("restored attacker scope = %q", got)
	}
	if got := restored.projectScope(restored.saved[plain.ID()].Info); got != folderScope(restored.saved[plain.ID()].Info) {
		t.Fatalf("restored plain scope = %q", got)
	}
}

// Local read_agent populates session.branch, reads stopped/asleep saved sessions
// (for turns=0 and turns>0), preserves JSON [] for 0-turn conversations, reports
// ChatUnavailable cleanly, and sets Truncated when the log window is truncated.
func TestProjectMCPReadAgentBranchStoppedZeroTurnsAndTruncated(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	d, _ := projectMCPDaemon(t)
	d.updateConfig(func(c *config.Config) {
		if c.Agents == nil {
			c.Agents = map[string]agentspec.Agent{}
		}
		c.Agents["plain"] = agentspec.Agent{Command: []string{"/bin/sh"}}
	})
	d.agentFile.mu.Lock()
	d.agentFile.read = false
	d.agentFile.mu.Unlock()

	repoDir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "feature/read-branch"}} {
		if out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	caller, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	client := projectMCPClient(t, d, caller)

	sibling, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, sibling)

	// 1. Local read_agent populates session.branch.
	none := 0
	liveRead := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &none})
	if liveRead.Session.Branch != "feature/read-branch" {
		t.Fatalf("local read_agent branch = %q, want feature/read-branch", liveRead.Session.Branch)
	}

	// 2. Stopped/asleep saved session is readable for turns=0 and turns>0, and via handleReadChat.
	sibling.SetStatus(protocol.AgentIdle)
	if _, err := d.sleepSession(sibling.ID()); err != nil {
		t.Fatalf("sleepSession: %v", err)
	}
	if _, live := d.sessions.get(sibling.ID()); live {
		t.Fatal("sleeping sibling is still live")
	}
	asleepZero := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &none})
	if asleepZero.Session.State != protocol.StateStopped || asleepZero.Session.Messaging || asleepZero.Session.Branch != "feature/read-branch" || len(asleepZero.Turns) != 0 {
		t.Fatalf("asleep turns=0: %+v", asleepZero)
	}
	asleepRead := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID()})
	if asleepRead.ChatUnavailable != "" || len(asleepRead.Turns) != 1 || asleepRead.Turns[0].Message != "original conversation" || asleepRead.Turns[0].State != turnComplete {
		t.Fatalf("asleep turns>0: %+v", asleepRead)
	}
	// Remote read_agent relies on handleReadChat, which also serves saved stopped sessions.
	chatResp, err := roundTripChat(t, d, &protocol.ReadChat{SessionId: sibling.ID()})
	if err != nil || !chatResp.GetSupported() || len(chatResp.GetMessages()) == 0 {
		t.Fatalf("handleReadChat on stopped session: %+v, %v", chatResp, err)
	}

	// A stopped saved session in another project is rejected.
	otherDir := t.TempDir()
	other, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: otherDir})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, other)
	other.SetStatus(protocol.AgentIdle)
	if _, err := d.sleepSession(other.ID()); err != nil {
		t.Fatal(err)
	}
	if res, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "read_agent", Arguments: readAgentArgs{SessionID: other.ID()}}); err == nil && !res.IsError {
		t.Fatal("read_agent read a stopped session from another project")
	}

	// 3. 0-turn conversation serializes "turns":[] (never null).
	zeroID := "zero-turn-session"
	zeroConv := "00000000-0000-4000-8000-000000000001"
	zeroLog := filepath.Join(t.TempDir(), zeroConv+".jsonl")
	metaLine := fmt.Sprintf(`{"type":"system","sessionId":%q,"cwd":%q,"timestamp":"2026-01-01T00:00:00Z"}`+"\n", zeroConv, repoDir)
	if err := os.WriteFile(zeroLog, []byte(metaLine), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sessionMu.Lock()
	d.saved[zeroID] = savedSession{
		Info: &protocol.SessionInfo{
			Id:             zeroID,
			NodeId:         d.config().NodeID,
			Agent:          "claude",
			Project:        repoDir,
			State:          protocol.StateStopped,
			ConversationId: zeroConv,
		},
		TranscriptPath: zeroLog,
	}
	d.sessionMu.Unlock()
	zeroRes, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "read_agent", Arguments: readAgentArgs{SessionID: zeroID}})
	if err != nil || zeroRes.IsError {
		t.Fatalf("zero-turn read_agent failed: %+v, %v", zeroRes, err)
	}
	rawJSON, err := json.Marshal(zeroRes.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawJSON), `"turns":[]`) || strings.Contains(string(rawJSON), `"turns":null`) {
		t.Fatalf("0-turn conversation did not serialize turns as []: %s", rawJSON)
	}

	// 4. ChatUnavailable cases: unsupported agent & no conversation saved yet.
	plainSess, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "plain", Project: repoDir, Args: []string{"-c", "exec cat"}})
	if err != nil {
		t.Fatal(err)
	}
	unsupportedRead := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: plainSess.ID()})
	if unsupportedRead.ChatUnavailable != "mako cannot read this agent's conversation" || len(unsupportedRead.Turns) != 0 {
		t.Fatalf("unsupported agent: %+v", unsupportedRead)
	}
	noConvID := "no-conv-saved"
	d.sessionMu.Lock()
	d.saved[noConvID] = savedSession{
		Info: &protocol.SessionInfo{
			Id:      noConvID,
			NodeId:  d.config().NodeID,
			Agent:   "claude",
			Project: repoDir,
			State:   protocol.StateStopped,
		},
	}
	d.sessionMu.Unlock()
	noConvRead := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: noConvID})
	if noConvRead.ChatUnavailable != "the agent has saved no conversation yet" || len(noConvRead.Turns) != 0 {
		t.Fatalf("no conversation saved: %+v", noConvRead)
	}

	// 5. Truncated / Start > 0 is propagated when the transcript exceeds the read window.
	truncID := "truncated-session"
	truncConv := "00000000-0000-4000-8000-000000000002"
	truncLog := filepath.Join(t.TempDir(), truncConv+".jsonl")
	var sb strings.Builder
	at := time.Now().UTC()
	for i := range 220 {
		at = at.Add(time.Second)
		rec, err := json.Marshal(map[string]any{
			"type":      "user",
			"sessionId": truncConv,
			"cwd":       repoDir,
			"timestamp": at.Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "user", "content": fmt.Sprintf("turn %d", i)},
		})
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(rec)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(truncLog, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sessionMu.Lock()
	d.saved[truncID] = savedSession{
		Info: &protocol.SessionInfo{
			Id:             truncID,
			NodeId:         d.config().NodeID,
			Agent:          "claude",
			Project:        repoDir,
			State:          protocol.StateStopped,
			ConversationId: truncConv,
		},
		TranscriptPath: truncLog,
	}
	d.sessionMu.Unlock()
	truncRead := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: truncID})
	if !truncRead.Truncated || len(truncRead.Turns) != readAgentTurns || truncRead.EarlierTurns == 0 {
		t.Fatalf("truncated read: truncated=%v turns=%d earlier=%d (%+v)", truncRead.Truncated, len(truncRead.Turns), truncRead.EarlierTurns, truncRead)
	}
}

func roundTripChat(t *testing.T, d *Daemon, req *protocol.ReadChat) (*protocol.Chat, error) {
	t.Helper()
	stream, done := ask(t, func(s *transport.Stream, env *protocol.Envelope) error {
		return d.handleReadChat(t.Context(), s, env)
	}, protocol.TypeReadChat, req)
	env, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return transport.Reply[*protocol.Chat](env)
}
