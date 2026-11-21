//go:build unix

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// texts are the blocks' texts, joined, each a tool's name for a call.
func texts(blocks []*protocol.LiveBlock) string {
	var out []string
	for _, b := range blocks {
		out = append(out, b.GetText()+b.GetTool())
	}
	return strings.Join(out, "|")
}

// A step's pieces join into its blocks; a new step starts again; a log
// that grows drops the blocks before the one streaming, and all of a step
// that ended; a step that ended and was never logged goes in time.
func TestLiveBlocksLeaveAsTheLogTakesThem(t *testing.T) {
	var h liveHub
	k := liveKey("s", "")
	look := func(size int64) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.chats[k].look(size)
	}
	if !h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Hel"}}}) {
		t.Fatal("the first post is not said to be")
	}
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "lo"}, {Kind: "tool", Index: 1, Tool: "Bash"}}})
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "input", Index: 1, Text: `{"command":"ls`}, {Kind: "input", Index: 1, Text: strings.Repeat("x", 2*liveArgs)}}})
	if got := h.blocks(k); len(got) != 2 || got[0].GetText() != "Hello" || got[1].GetTool() != "Bash" || len(got[1].GetText()) > 2*liveArgs {
		t.Fatalf("blocks %q", texts(got))
	}
	if look(0) {
		t.Fatal("a log that did not grow")
	}
	if !look(10) || texts(h.blocks(k))[:len(`{"command":"ls`)] != `{"command":"ls` || len(h.blocks(k)) != 1 {
		t.Fatalf("after the log grew: %q", texts(h.blocks(k)))
	}
	h.take(livePost{Session: "s", Step: "t:0", End: true})
	look(20)
	if got := h.blocks(k); len(got) != 0 {
		t.Fatalf("a step ended and logged left %q", texts(got))
	}
	if h.take(livePost{Session: "s", Agent: "sub", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Sub"}}}) {
		t.Fatal("a subagent's post is the session's first")
	}
	if got := texts(h.blocks(liveKey("s", "sub"))); got != "Sub" || len(h.blocks(k)) != 0 {
		t.Fatalf("a subagent's reply %q, the session's %q", got, texts(h.blocks(k)))
	}
	h.take(livePost{Session: "s", Step: "t:1", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Next"}}, End: true})
	if got := texts(h.blocks(k)); got != "Next" {
		t.Fatalf("a new step: %q", got)
	}
	h.chats[k].ended = time.Now().Add(-liveStale - time.Second)
	look(20)
	if len(h.blocks(k)) != 0 {
		t.Fatal("a step never logged stayed")
	}
	h.forget("s")
	if len(h.chats) != 0 || h.blocks(k) != nil {
		t.Fatal("a session closed left its replies")
	}
}

// Two followers of one conversation hear the same: what streams, and that
// the log grew, which one does not take from the other.
func TestFollowersHearAlike(t *testing.T) {
	var h liveHub
	k := liveKey("s", "")
	var size atomic.Int64
	a, stopA := h.follow(k, size.Load)
	defer stopA()
	b, stopB := h.follow(k, size.Load)
	defer stopB()
	hear := func(feed <-chan *protocol.ChatLive, what string, ok func(*protocol.ChatLive) bool) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l := <-feed:
				if ok(l) {
					return
				}
			case <-deadline:
				t.Fatalf("no %s", what)
			}
		}
	}
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Hi"}}, End: true})
	for _, feed := range []<-chan *protocol.ChatLive{a, b} {
		hear(feed, "the text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Hi" })
	}
	size.Store(10)
	for _, feed := range []<-chan *protocol.ChatLive{a, b} {
		hear(feed, "word of the log", func(l *protocol.ChatLive) bool { return l.GetLogged() && len(l.GetBlocks()) == 0 })
	}
	stopA()
	stopB()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chats[k].stop != nil {
		t.Fatal("the look-out outlived its followers")
	}
}

// A front-end follows a session on another node: what the agent's mod
// posts there streams to it, and the log growing says so.
func TestAChatIsFollowedAcrossNodes(t *testing.T) {
	a := meshDaemon(t, "live-a")
	b := meshDaemon(t, "live-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	addSessionOf(t, b, "agent", "claude")
	s, _ := b.sessions.get("agent")
	log := filepath.Join(t.TempDir(), "c.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.SetConversation("c", log)
	if b.liveSocket == "" {
		t.Fatal("no live socket")
	}
	post := func(body string, want int) {
		t.Helper()
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", b.liveSocket)
		}}}
		resp, err := hc.Post("http://mako/live", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("post answered %s", resp.Status)
		}
	}
	waitFor(t, "the other node", func() bool { return lists(t.Context(), dialDaemon(t, a), "agent") })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got := make(chan *protocol.ChatLive, 16)
	go func() {
		_ = dialDaemon(t, a).WatchChat(ctx, &protocol.WatchChat{NodeId: b.cfg.NodeID, SessionId: "agent"}, func(l *protocol.ChatLive) { got <- l })
	}()
	next := func(what string, ok func(*protocol.ChatLive) bool) {
		t.Helper()
		for {
			select {
			case l := <-got:
				if ok(l) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %s", what)
			}
		}
	}
	next("first word", func(*protocol.ChatLive) bool { return true })
	post(`{"session":"agent","token":"forged","step":"t:0","pieces":[{"k":"text","i":0,"t":"Forged"}]}`, http.StatusForbidden)
	token := b.liveToken("agent")
	post(`{"session":"agent","token":"`+token+`","step":"t:0","pieces":[{"k":"text","i":0,"t":"Thinking about it"}]}`, http.StatusNoContent)
	next("the streamed text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Thinking about it" })
	post(`{"session":"agent","token":"`+token+`","step":"t:0","end":true}`, http.StatusNoContent)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant"}` + "\n")
	f.Close()
	next("word of the log", func(l *protocol.ChatLive) bool { return l.GetLogged() && len(l.GetBlocks()) == 0 })
}

func TestLiveModMessagesPollAndAck(t *testing.T) {
	d := meshDaemon(t, "live-msg")
	addSessionOf(t, d, "claude-main", "claude")
	if d.liveSocket == "" {
		t.Fatal("no live socket")
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.liveSocket)
	}}}
	post := func(path string, body any) *http.Response {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := hc.Post("http://mako"+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Forged token and unknown session are rejected.
	r := post("/messages/poll", liveMessagesPollReq{Session: "claude-main", Token: "forged", WaitSeconds: 1})
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged poll status = %d, want 403", r.StatusCode)
	}
	r = post("/messages/poll", liveMessagesPollReq{Session: "missing", Token: "x", WaitSeconds: 1})
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session poll status = %d, want 404", r.StatusCode)
	}

	token := d.liveToken("claude-main")

	// Start a blocking long-poll from the mod; verify it wakes immediately on send
	// and registers an active waiter to suppress PTY fallback.
	pollCh := make(chan liveMessagesPollResp, 1)
	go func() {
		resp := post("/messages/poll", liveMessagesPollReq{
			Session:     "claude-main",
			Token:       token,
			AfterID:     0,
			WaitSeconds: 10,
		})
		defer resp.Body.Close()
		var out liveMessagesPollResp
		_ = json.NewDecoder(resp.Body).Decode(&out)
		pollCh <- out
	}()
	waitForWithin(t, 3*time.Second, "mod poll never registered active waiter", func() bool {
		return d.projectAgents.hasCLIWaiter("claude-main")
	})

	first, err := d.projectAgents.messages.send("worker-1", "claude-main", "build is green", "")
	if err != nil {
		t.Fatal(err)
	}
	var got liveMessagesPollResp
	select {
	case got = <-pollCh:
	case <-time.After(5 * time.Second):
		t.Fatal("mod long-poll timed out")
	}
	if !got.HasMessages || got.LastID != first.ID || got.Count != 1 || !strings.Contains(got.Prompt, "build is green") || !strings.Contains(got.Summary, "worker-1") {
		t.Fatalf("unexpected poll response: %+v", got)
	}
	// Grace window keeps PTY fallback suppressed between poll return and ack/next poll.
	if !d.projectAgents.hasCLIWaiter("claude-main") {
		t.Fatal("expected mod grace window to keep PTY fallback suppressed")
	}

	// Polling with after_id = first.ID waits for newer messages only.
	poll2Ch := make(chan liveMessagesPollResp, 1)
	go func() {
		resp := post("/messages/poll", liveMessagesPollReq{
			Session:     "claude-main",
			Token:       token,
			AfterID:     first.ID,
			WaitSeconds: 10,
		})
		defer resp.Body.Close()
		var out liveMessagesPollResp
		_ = json.NewDecoder(resp.Body).Decode(&out)
		poll2Ch <- out
	}()
	time.Sleep(100 * time.Millisecond)
	second, err := d.projectAgents.messages.send("worker-2", "claude-main", "lint passed", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got2 := <-poll2Ch:
		if !got2.HasMessages || got2.LastID != second.ID || !strings.Contains(got2.Prompt, "lint passed") {
			t.Fatalf("unexpected second poll response: %+v", got2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second mod long-poll timed out")
	}

	// Acking marks both messages delivered and empties the inbox.
	ackResp := post("/messages/ack", liveMessagesAckReq{
		Session:    "claude-main",
		Token:      token,
		AckThrough: second.ID,
	})
	ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack status = %d, want 204", ackResp.StatusCode)
	}
	if inbox, _ := d.projectAgents.messages.pending("claude-main"); len(inbox) != 0 {
		t.Fatalf("expected empty inbox after mod ack, got %+v", inbox)
	}

	// /mako/status reports project sessions and child status summary.
	mainSess, _ := d.sessions.get("claude-main")
	childSess, err := session.Start(session.Spec{
		ID: "claude-child", NodeID: d.cfg.NodeID, Agent: "claude", Project: mainSess.Info().GetProject(),
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childSess.Close() })
	d.sessions.add(childSess)
	childSess.SetStatus(protocol.AgentWorking)
	d.sessionMu.Lock()
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	d.saved["claude-child"] = savedSession{ParentID: "claude-main"}
	d.sessionMu.Unlock()

	statusResp := post("/mako/status", liveMakoStatusReq{Session: "claude-main", Token: token})
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("/mako/status status = %d", statusResp.StatusCode)
	}
	var statusOut liveMakoStatusResp
	if err := json.NewDecoder(statusResp.Body).Decode(&statusOut); err != nil {
		t.Fatal(err)
	}
	if statusOut.ChildStatus != "1 subagent working" || !strings.Contains(statusOut.Summary, "claude-child") || !strings.Contains(statusOut.Summary, "[child]") {
		t.Fatalf("unexpected /mako/status: %+v", statusOut)
	}
}

// GUI user prompts sent to Claude must stay on the PTY so Claude Code receives
// an authentic user turn instead of wrapping it in the mako-live plugin envelope.
func TestHandleSendKeepsClaudeUserPromptOnPTY(t *testing.T) {
	d := meshDaemon(t, "live-send")
	dir := t.TempDir()
	ptyOut := filepath.Join(dir, "pty.out")
	s, err := session.Start(session.Spec{
		ID: "claude-gui", NodeID: d.cfg.NodeID, Agent: "claude", Project: dir,
		Command: []string{"sh", "-c", `stty -echo; printf '\033[?2004h'; while IFS= read -r line; do printf '%s\n' "$line" >> pty.out; done`},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	waitFor(t, "pty never enabled bracketed paste", func() bool {
		snap, err := s.Snapshot(true)
		return err == nil && strings.Contains(snap.GetText(), "\x1b[?2004h")
	})

	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.liveSocket)
	}}}
	token := d.liveToken("claude-gui")

	// Even while mako-live mod is actively long-polling for inter-agent messages,
	// GUI user prompts go directly to PTY without being routed to the mod.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		b, _ := json.Marshal(liveMessagesPollReq{Session: "claude-gui", Token: token, WaitSeconds: 5})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://mako/messages/poll", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	waitForWithin(t, 3*time.Second, "mod never registered waiter", func() bool {
		return d.projectAgents.hasCLIWaiter("claude-gui")
	})

	st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleSend(t.Context(), st, env)
	}, protocol.TypeSend, &protocol.Send{SessionId: "claude-gui", Text: "buildx installed", Keys: []byte("\r")})
	env, err := st.Recv()
	if err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("handleSend: %v, %v", env, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handleSend err: %v", err)
	}
	waitFor(t, "user prompt never reached PTY", func() bool {
		b, _ := os.ReadFile(ptyOut)
		return strings.Contains(string(b), "buildx installed")
	})
}

// An agy session streams its in-flight reply from the PTY screen buffer to
// WatchChat while working, clears atomically when transcript_full.jsonl catches
// up, and clears promptly when leaving AgentWorking.
func TestAgyScreenLiveStreamsAndClears(t *testing.T) {
	d := meshDaemon(t, "agy-live")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transcript_full.jsonl")
	if err := os.WriteFile(logPath, []byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nSay hello\n</USER_REQUEST>"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Shell process that renders agy-like frames triggered by commands written to stdin.
	script := `stty -echo
rule="────────────────────────────────────────────────────────────────────────────────"
div="────────────────────────────────────────────────────────────"
frame() {
  printf '\033[2J\033[H%s\n> %s\n\n%s\n%s\n%s\n>\n%s\n%s' "$div" "$1" "$2" "$3" "$rule" "$rule" "$4"
}
while IFS= read -r cmd; do
  case "$cmd" in
    spinner)
      frame "Say hello" "" "⣾  Generating..." "esc to cancel                          auto · Gemini 4 Hydrogen · medium"
      ;;
    part1)
      frame "Say hello" "  Hello from agy" "⣾  Generating..." "esc to cancel                          auto · Gemini 4 Hydrogen · medium"
      ;;
    part2)
      frame "Say hello" "  Hello from agy live stream!" "⠙  Generating..." "esc to cancel                          auto · Gemini 4 Hydrogen · medium"
      ;;
    turn2)
      frame "Second prompt" "  Second turn streaming" "⠹  Generating..." "esc to cancel                          auto · Gemini 4 Hydrogen · medium"
      ;;
    idle)
      frame "Second prompt" "  Second turn streaming" "" "? for shortcuts                        auto · Gemini 4 Hydrogen · medium"
      ;;
    reflow)
      frame "Second prompt" "  Reformatted second turn reply" "⠹  Generating..." "esc to cancel                          auto · Gemini 4 Hydrogen · medium"
      ;;
    perm)
      printf '\033[2J\033[H%s\n> Second prompt\n\n  Second turn streaming\n\n● Ran (date) (ctrl+o to expand)\n\nRequesting permission for:\n   date\n\n> 1. Yes\n  2. No\n\n  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\nesc to cancel                          accept-edits · Gemini 4 Hydrogen · medium' "$div"
      ;;
  esac
done`

	s, err := session.Start(session.Spec{
		ID:      "agy-1",
		NodeID:  d.cfg.NodeID,
		Agent:   "agy",
		Project: dir,
		Command: []string{"/bin/sh", "-c", script},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetConversation("conv-1", logPath)
	s.SetStatus(protocol.AgentWorking)
	d.sessions.add(s)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got := make(chan *protocol.ChatLive, 32)
	go func() {
		_ = dialDaemon(t, d).WatchChat(ctx, &protocol.WatchChat{SessionId: "agy-1"}, func(l *protocol.ChatLive) { got <- l })
	}()

	next := func(what string, ok func(*protocol.ChatLive) bool) {
		t.Helper()
		for {
			select {
			case l := <-got:
				if ok(l) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// Initial empty snapshot on connect.
	next("initial WatchChat message", func(l *protocol.ChatLive) bool { return len(l.GetBlocks()) == 0 })

	send := func(cmd string) {
		t.Helper()
		if _, err := s.Write([]byte(cmd + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	// Spinner-only frame must not emit a live text block.
	send("spinner")
	time.Sleep(250 * time.Millisecond)
	if blocks := d.live.blocks(liveKey("agy-1", "")); len(blocks) != 0 {
		t.Fatalf("spinner-only emitted blocks: %q", texts(blocks))
	}

	// First streamed chunk appears live.
	send("part1")
	next("part1 live text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Hello from agy" })

	// Monotonic growth updates the same live block.
	send("part2")
	next("part2 live text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Hello from agy live stream!" })

	// When transcript_full.jsonl writes the completed PLANNER_RESPONSE, WatchChat
	// signals Logged=true and clears the live block atomically (no duplicate).
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"Hello from agy live stream!"}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	next("log growth clears live block", func(l *protocol.ChatLive) bool {
		return l.GetLogged() && len(l.GetBlocks()) == 0
	})

	// Second turn: streams live, then clears promptly when status leaves AgentWorking.
	f, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nSecond prompt\n</USER_REQUEST>"}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	send("turn2")
	next("turn2 live text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Second turn streaming" })

	// Non-monotonic reflow replaces the block cleanly without duplication.
	send("reflow")
	next("reflowed live text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Reformatted second turn reply" })

	// A permission dialog appearing mid-stream clears live text even before
	// status transitions away from AgentWorking.
	send("perm")
	next("permission dialog clears live block", func(l *protocol.ChatLive) bool { return len(l.GetBlocks()) == 0 })

	// Back to streaming, then verify leaving AgentWorking clears promptly.
	send("turn2")
	next("turn2 resumed live text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Second turn streaming" })

	s.SetStatus(protocol.AgentDone)
	next("status change clears live block", func(l *protocol.ChatLive) bool { return len(l.GetBlocks()) == 0 })

	// While idle, even if screen still shows text, nothing is re-emitted.
	send("idle")
	time.Sleep(250 * time.Millisecond)
	if blocks := d.live.blocks(liveKey("agy-1", "")); len(blocks) != 0 {
		t.Fatalf("idle screen emitted blocks: %q", texts(blocks))
	}
}

func TestLastLoggedAssistantText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript_full.jsonl")
	logs, ok := agents.As[agent.Logs]("agy")
	if !ok {
		t.Fatal("agy driver missing Logs")
	}

	// Empty / missing file returns "".
	if got := lastLoggedAssistantText(path, logs); got != "" {
		t.Fatalf("missing file = %q, want empty", got)
	}

	// Turn 1 reply is returned until a newer USER_INPUT resets it.
	turn1 := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nFirst\n</USER_REQUEST>"}` + "\n" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"First reply"}` + "\n"
	if err := os.WriteFile(path, []byte(turn1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastLoggedAssistantText(path, logs); got != "First reply" {
		t.Fatalf("turn1 = %q, want %q", got, "First reply")
	}

	// New USER_INPUT clears lastLogged so identical replies on Turn 2 can stream.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nSecond\n</USER_REQUEST>"}` + "\n")
	f.Close()
	if got := lastLoggedAssistantText(path, logs); got != "" {
		t.Fatalf("after new USER_INPUT = %q, want empty", got)
	}

	// Large file (> screenLiveTailWindow) still finds the latest reply from the tail.
	pad := `{"step_index":3,"source":"MODEL","type":"GENERIC","status":"DONE","content":"` + strings.Repeat("x", screenLiveTailWindow+4096) + `"}` + "\n"
	tail := `{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"Tail reply"}` + "\n"
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(pad + tail)
	f.Close()
	if got := lastLoggedAssistantText(path, logs); got != "Tail reply" {
		t.Fatalf("large tail = %q, want %q", got, "Tail reply")
	}
}

// Rapid disconnect + reconnect on a screen-polled liveChat synchronously clears
// old screen blocks and fences out any in-flight poller from the cancelled
// generation so follower 2 never sees stale blocks or gets its stream clobbered.
func TestScreenLiveRapidDisconnectReconnectFencesStalePoller(t *testing.T) {
	var h liveHub
	k := liveKey("agy-race", "")
	var size atomic.Int64

	poller1Started := make(chan struct{})
	poller1Release := make(chan struct{})
	poller1Done := make(chan bool, 1)
	var gen1 atomic.Uint64

	// Follower 1 connects; poller 1 emits "Stale text" on tick 1, then blocks
	// inside poll() on tick 2 while follower 1 disconnects and follower 2 connects.
	var tick1 atomic.Int32
	feed1, stop1 := h.followWith(k, size.Load, func(gen uint64) func(func() int64) (bool, int64) {
		gen1.Store(gen)
		return func(sz func() int64) (bool, int64) {
			n := tick1.Add(1)
			if n == 1 {
				h.takeForGen(gen, livePost{
					Session: "agy-race",
					Step:    "screen:1",
					Pieces:  []livePiece{{Kind: "text", Index: 0, Text: "Stale text"}},
				})
				return true, sz()
			}
			if n == 2 {
				close(poller1Started)
				<-poller1Release
				ok := h.takeForGen(gen, livePost{
					Session: "agy-race",
					Step:    "screen:1",
					Pieces:  []livePiece{{Kind: "text", Index: 0, Text: " CORRUPT"}},
				})
				poller1Done <- ok
			}
			return true, sz()
		}
	})

	// Wait until follower 1 sees "Stale text" and poller 1 is parked mid-tick 2.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case l := <-feed1:
			if texts(l.GetBlocks()) == "Stale text" {
				goto parked
			}
		case <-deadline:
			t.Fatal("timed out waiting for follower 1 text")
		}
	}
parked:
	select {
	case <-poller1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for poller 1 tick 2")
	}

	// Disconnect follower 1 while poller 1 is still mid-poll, and immediately
	// reconnect follower 2 with a fresh poller.
	stop1()

	var tick2 atomic.Int32
	feed2, stop2 := h.followWith(k, size.Load, func(gen uint64) func(func() int64) (bool, int64) {
		return func(sz func() int64) (bool, int64) {
			switch tick2.Add(1) {
			case 1:
				h.takeForGen(gen, livePost{
					Session: "agy-race",
					Step:    "screen:1",
					Pieces:  []livePiece{{Kind: "text", Index: 0, Text: "Hello"}},
				})
			case 2:
				h.takeForGen(gen, livePost{
					Session: "agy-race",
					Step:    "screen:1",
					Pieces:  []livePiece{{Kind: "text", Index: 0, Text: " world!"}},
				})
			}
			return true, sz()
		}
	})
	defer stop2()

	// Follower 2's initial snapshot must be empty (not "Stale text").
	initLive := <-feed2
	if len(initLive.GetBlocks()) != 0 {
		t.Fatalf("follower 2 saw stale initial blocks: %q", texts(initLive.GetBlocks()))
	}

	// Wait for follower 2's first chunk, then release the stale poller 1 so it
	// races directly against follower 2's second monotonic delta.
	deadline = time.After(3 * time.Second)
	for {
		select {
		case l := <-feed2:
			if texts(l.GetBlocks()) == "Hello" {
				goto releaseStale
			}
		case <-deadline:
			t.Fatal("timed out waiting for follower 2 initial text")
		}
	}
releaseStale:
	close(poller1Release)
	if ok := <-poller1Done; ok {
		t.Fatalf("stale poller (gen %d) was allowed to mutate active chat", gen1.Load())
	}

	// Follower 2 must cleanly receive "Hello world!" without clobbering or corruption.
	deadline = time.After(3 * time.Second)
	for {
		select {
		case l := <-feed2:
			if got := texts(l.GetBlocks()); got == "Hello world!" {
				return
			} else if strings.Contains(got, "CORRUPT") || got == " world!" {
				t.Fatalf("follower 2 stream corrupted: %q", got)
			}
		case <-deadline:
			t.Fatal("timed out waiting for follower 2 delta")
		}
	}
}
