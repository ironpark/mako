package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/ironpark/mako/internal/transport"
	"google.golang.org/protobuf/proto"
)

var (
	sideQuestionTimeout = 45 * time.Second
	sideQuestionPoll    = 80 * time.Millisecond
	sideQuestionSettle  = 250 * time.Millisecond
)

const (
	maxSideQuestionBytes = 8 << 10
	maxSideContextBytes  = 24 << 10
)

// sideQuestionGuard serializes side-question requests per session so two
// concurrent asks never interleave PTY input or overlays.
type sideQuestionGuard struct {
	mu       sync.Mutex
	inFlight map[string]bool
}

func (g *sideQuestionGuard) acquire(sessionID string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight == nil {
		g.inFlight = map[string]bool{}
	}
	if g.inFlight[sessionID] {
		return nil, false
	}
	g.inFlight[sessionID] = true
	return func() {
		g.mu.Lock()
		delete(g.inFlight, sessionID)
		g.mu.Unlock()
	}, true
}

// testSideFallbackRunner lets unit tests intercept read-only one-shot fallback
// runs without spawning an external agent binary.
var testSideFallbackRunner func(ctx context.Context, agentName, project, prompt string) (string, error)

func (d *Daemon) handleAskSideQuestion(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSideQuestionAnswer, func(r *protocol.AskSideQuestion) (*protocol.SideQuestionAnswer, error) {
		return d.askSideQuestion(ctx, r)
	})
}

func (d *Daemon) askSideQuestion(ctx context.Context, req *protocol.AskSideQuestion) (*protocol.SideQuestionAnswer, error) {
	sessionID := strings.TrimSpace(req.GetSessionId())
	if sessionID == "" {
		return nil, errors.New("side question: missing session_id")
	}
	question := strings.TrimSpace(req.GetQuestion())
	if question == "" {
		return nil, errors.New("side question cannot be empty")
	}
	if len(question) > maxSideQuestionBytes {
		return nil, fmt.Errorf("side question exceeds %d bytes", maxSideQuestionBytes)
	}

	release, acquired := d.sideQuestions.acquire(sessionID)
	if !acquired {
		return nil, errors.New("a side question is already in progress for this session")
	}
	defer release()

	s, hasLive := d.sessions.get(sessionID)
	saved, hasSaved := d.savedSession(sessionID)
	if !hasLive && (!hasSaved || saved.Info == nil) {
		return nil, fmt.Errorf("unknown session %q", sessionID)
	}

	var info *protocol.SessionInfo
	if hasLive {
		info = s.Info()
	} else {
		info = proto.CloneOf(saved.Info)
	}

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, sideQuestionTimeout)
		defer cancel()
	}

	agentName := info.GetAgent()
	running := hasLive && info.GetState() == protocol.StateRunning

	// 1. Codex managed app-server sessions: use an ephemeral forked thread so
	// we never touch the terminal screen or interrupt an active turn.
	if running && agentName == "codex" {
		if b := d.codexBridge(sessionID); b != nil {
			ans, err := b.askSideQuestion(runCtx, question)
			if err == nil && strings.TrimSpace(ans) != "" {
				return &protocol.SideQuestionAnswer{
					Question: question,
					Answer:   strings.TrimSpace(ans),
					Mode:     protocol.SideQuestionModeAppServer,
				}, nil
			}
			if runCtx.Err() != nil {
				return nil, runCtx.Err()
			}
			d.log.Debug("codex app-server side question fell back to PTY/transcript", "session", sessionID, "err", err)
		}
	}

	// 2. Native terminal driver (/btw or /side overlay).
	if running {
		if sq, ok := agents.As[agent.SideQuestion](agentName); ok {
			ans, err := d.askSideQuestionNative(runCtx, s, sq, question)
			if err != nil {
				return nil, err
			}
			return &protocol.SideQuestionAnswer{
				Question: question,
				Answer:   ans,
				Mode:     protocol.SideQuestionModeNative,
			}, nil
		}
	}

	// 3. Fallback: read-only one-shot call over the session's transcript.
	ans, err := d.askSideQuestionFallback(runCtx, info, s, hasLive, saved, hasSaved, question)
	if err != nil {
		return nil, err
	}
	return &protocol.SideQuestionAnswer{
		Question: question,
		Answer:   ans,
		Mode:     protocol.SideQuestionModeFallback,
	}, nil
}

// askSideQuestionNative runs a native side-question slash command in the
// running session's PTY only when the screen and session state allow it,
// reads the answer from the overlay, and closes the overlay cleanly.
func (d *Daemon) askSideQuestionNative(ctx context.Context, s *session.Session, sq agent.SideQuestion, question string) (string, error) {
	info := s.Info()
	if info.GetState() != protocol.StateRunning {
		return "", errors.New("session is not running")
	}
	if info.GetApproval() != nil {
		return "", errors.New("cannot ask a side question while an approval is pending")
	}
	if info.GetDialog() != nil {
		return "", errors.New("cannot ask a side question while a dialog is open")
	}
	if info.GetStatus() == protocol.AgentWaiting {
		return "", errors.New("cannot ask a side question while the agent is waiting for input")
	}
	if info.GetStatus() == protocol.AgentCompacting {
		return "", errors.New("cannot ask a side question while the agent is compacting the conversation")
	}

	screen, title, ok := s.ScreenState()
	if !ok {
		return "", errors.New("session is not running")
	}
	if canAsk, reason := sq.SideQuestionCanAsk(screen, title); !canAsk {
		if reason == "" {
			reason = "the agent cannot accept a side question right now"
		}
		return "", errors.New(reason)
	}

	cmd := sq.SideQuestionCommand(question)
	if cmd == "" {
		return "", errors.New("side question cannot be empty")
	}

	if err := s.SendDirect(cmd, []byte{'\r'}); err != nil {
		return "", err
	}

	dismissIfOpen := func() {
		cur, _, ok := s.ScreenState()
		if !ok {
			return
		}
		if keys := sq.SideQuestionDismiss(cur); len(keys) > 0 {
			_, _ = s.Write(keys)
		}
	}

	tick := time.NewTicker(sideQuestionPoll)
	defer tick.Stop()

	var (
		seenOverlay    bool
		lastDoneAnswer string
		doneSince      time.Time
	)

	for {
		select {
		case <-ctx.Done():
			dismissIfOpen()
			return "", ctx.Err()
		case <-s.Done():
			return "", errors.New("session exited before answering the side question")
		case <-tick.C:
		}

		curScreen, _, ok := s.ScreenState()
		if !ok {
			return "", errors.New("session exited before answering the side question")
		}

		ans, state := sq.SideQuestionParse(curScreen)
		if state != agent.SideQuestionNone {
			seenOverlay = true
		}

		if state == agent.SideQuestionDone && strings.TrimSpace(ans) != "" {
			trimmed := strings.TrimSpace(ans)
			now := time.Now()
			if trimmed != lastDoneAnswer {
				lastDoneAnswer = trimmed
				doneSince = now
			}
			if sideQuestionSettle <= 0 || now.Sub(doneSince) >= sideQuestionSettle {
				if keys := sq.SideQuestionDismiss(curScreen); len(keys) > 0 {
					_, _ = s.Write(keys)
				}
				return trimmed, nil
			}
			continue
		}

		lastDoneAnswer = ""
		doneSince = time.Time{}

		// If an approval or dialog popped up before the overlay ever appeared,
		// abort without sending Escape (which would affect the dialog/turn).
		if !seenOverlay {
			cur := s.Info()
			if cur.GetApproval() != nil || cur.GetDialog() != nil || cur.GetStatus() == protocol.AgentWaiting {
				return "", errors.New("side question cancelled because the agent is waiting for input")
			}
		}
	}
}

func (d *Daemon) askSideQuestionFallback(ctx context.Context, info *protocol.SessionInfo, s *session.Session, hasLive bool, saved savedSession, hasSaved bool, question string) (string, error) {
	var messages []*protocol.ChatMessage
	if hasLive {
		if chat, err := d.chatForSession(ctx, s, transcript.Span{}); err == nil && chat != nil {
			messages = chat.GetMessages()
		}
	} else if hasSaved {
		if chat, err := d.chatForSaved(ctx, info, saved, transcript.Span{}); err == nil && chat != nil {
			messages = chat.GetMessages()
		}
	}

	prompt := formatSideQuestionPrompt(messages, question, maxSideContextBytes)

	if testSideFallbackRunner != nil {
		out, err := testSideFallbackRunner(ctx, info.GetAgent(), info.GetProject(), prompt)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}

	fallbackAgent := d.pickSideFallbackAgent(info.GetAgent())
	if fallbackAgent == "" {
		return "", &protocol.CodedError{
			Code:    protocol.CodeUnsupported,
			Message: fmt.Sprintf("agent %q has no native side question command and no read-only fallback agent is available", info.GetAgent()),
		}
	}

	res, err := d.runAgentPromptWith(ctx, &protocol.RunAgentPrompt{
		Agent:   fallbackAgent,
		Project: info.GetProject(),
		Prompt:  prompt,
		Format:  "text",
	}, oneShotOptions{
		stripYolo:        true,
		noTools:          true,
		skipTemplateGate: true,
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res.GetOutput())
	if out == "" {
		return "", errors.New("side question fallback returned an empty answer")
	}
	return out, nil
}

// pickSideFallbackAgent chooses a OneShot-capable agent installed on this node
// to answer a read-only transcript side question, preferring preferredAgent.
func (d *Daemon) pickSideFallbackAgent(preferredAgent string) string {
	configured := d.agents()
	candidates := make([]string, 0, 4)
	if preferredAgent != "" {
		candidates = append(candidates, preferredAgent)
	}
	for _, name := range []string{"claude", "codex", "agy"} {
		if !slices.Contains(candidates, name) {
			candidates = append(candidates, name)
		}
	}
	for _, name := range candidates {
		if _, ok := agents.As[agent.OneShot](name); !ok {
			continue
		}
		launch, err := agentspec.Resolve(name, "", nil, nil, configured)
		if err != nil || len(launch.Command) == 0 {
			continue
		}
		if _, ok := agents.Native(name, launch.Command[0]); !ok {
			continue
		}
		if _, err := agent.Find(launch.Command[0]); err != nil {
			continue
		}
		return name
	}
	return ""
}

// formatSideQuestionPrompt builds a budgeted, read-only prompt from a session's
// recent transcript messages so a one-shot model call can answer a side
// question without running tools or modifying the conversation history.
func formatSideQuestionPrompt(messages []*protocol.ChatMessage, question string, maxContextBytes int) string {
	var b strings.Builder
	b.WriteString("You are answering a brief side question (/btw) about an ongoing coding agent session.\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Use ONLY the read-only conversation context provided below.\n")
	b.WriteString("- Do NOT invoke tools, run commands, read additional files, or modify anything.\n")
	b.WriteString("- Answer concisely and directly. If the context does not contain the answer, say so briefly.\n\n")

	if contextSlice := formatSideContext(messages, maxContextBytes); contextSlice != "" {
		b.WriteString("<session_transcript>\n")
		b.WriteString(contextSlice)
		b.WriteString("\n</session_transcript>\n\n")
	} else {
		b.WriteString("<session_transcript>\n(No conversation messages recorded yet.)\n</session_transcript>\n\n")
	}

	b.WriteString("Side question:\n")
	b.WriteString(strings.TrimSpace(question))
	return b.String()
}

// formatSideContext serializes the most recent transcript messages within
// maxBytes, keeping full user/assistant text (clipped per message) and compact
// summaries of tool calls. Shared with read-only side chats.
func formatSideContext(messages []*protocol.ChatMessage, maxBytes int) string {
	if len(messages) == 0 || maxBytes <= 0 {
		return ""
	}
	var entries []string
	total := 0
	for _, msg := range slices.Backward(messages) {
		entry := formatSideMessage(msg)
		if entry == "" {
			continue
		}
		cost := len(entry) + 2
		if total+cost > maxBytes {
			if len(entries) == 0 && maxBytes > 64 {
				entries = append(entries, agent.Clip(entry, maxBytes-4))
			}
			break
		}
		entries = append(entries, entry)
		total += cost
	}
	slices.Reverse(entries)
	return strings.Join(entries, "\n\n")
}

func formatSideMessage(msg *protocol.ChatMessage) string {
	if msg == nil {
		return ""
	}
	switch msg.GetRole() {
	case "user":
		text := strings.TrimSpace(msg.GetText())
		if text == "" {
			return ""
		}
		return "User: " + agent.Clip(text, 2000)
	case "assistant":
		text := strings.TrimSpace(msg.GetText())
		if text == "" {
			return ""
		}
		return "Assistant: " + agent.Clip(text, 3000)
	case "tool":
		name := msg.GetTool()
		if name == "" {
			name = msg.GetKind()
		}
		if name == "" {
			name = "tool"
		}
		detail := agent.OneLine(msg.GetDetail(), 160)
		var b strings.Builder
		b.WriteString("Tool [")
		b.WriteString(name)
		b.WriteString("]")
		if detail != "" {
			b.WriteString(" ")
			b.WriteString(detail)
		}
		if res := strings.TrimSpace(msg.GetResult()); res != "" {
			b.WriteString("\nResult: ")
			b.WriteString(agent.Clip(res, 600))
		} else if msg.GetOutput() {
			if out := strings.TrimSpace(msg.GetText()); out != "" {
				b.WriteString("\nOutput: ")
				b.WriteString(agent.Clip(out, 600))
			}
		}
		return b.String()
	default:
		return ""
	}
}

func (b *codexBridge) sideWaiter(threadID string) *codexSideWaiter {
	if b == nil || threadID == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sideWaiters[threadID]
}

func (b *codexBridge) failSideWaitersLocked(err error) {
	for id, w := range b.sideWaiters {
		w.finish(err)
		delete(b.sideWaiters, id)
	}
}

// askSideQuestion forks the live Codex thread as an ephemeral read-only side
// thread in codex app-server and runs a single side-question turn on it
// without interrupting or mutating the active main thread.
func (b *codexBridge) askSideQuestion(ctx context.Context, question string) (string, error) {
	if b == nil {
		return "", errors.New("codex app-server: unavailable")
	}
	if ctx == nil {
		ctx = b.ctx
	}
	client := b.currentClient()
	if client == nil {
		return "", errors.New("codex app-server: disconnected")
	}
	tid, err := b.resolveThreadID(ctx, client)
	if err != nil {
		return "", err
	}

	ephemeral := true
	forked, err := client.ThreadFork(ctx, codex.ThreadForkParams{
		ThreadID:              tid,
		Ephemeral:             &ephemeral,
		ExcludeTurns:          true,
		ApprovalPolicy:        "never",
		Sandbox:               "read-only",
		DeveloperInstructions: "Answer the user's brief side question (/btw) using the existing conversation context. Do not run tools or modify files.",
	})
	if err != nil {
		return "", fmt.Errorf("codex side fork: %w", err)
	}
	sideTID := forked.Thread.ID
	if sideTID == "" {
		return "", errors.New("codex side fork: empty thread id")
	}

	waiter := newCodexSideWaiter()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return "", errors.New("codex app-server: disconnected")
	}
	b.ensureInitLocked()
	if b.sideWaiters == nil {
		b.sideWaiters = map[string]*codexSideWaiter{}
	}
	b.ignored[sideTID] = true
	b.sideWaiters[sideTID] = waiter
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.sideWaiters, sideTID)
		b.mu.Unlock()
	}()

	if _, err := client.TurnStart(ctx, codex.TurnStartParams{
		ThreadID: sideTID,
		Input:    []codex.UserInput{{Type: "text", Text: question}},
	}); err != nil {
		return "", fmt.Errorf("codex side turn/start: %w", err)
	}

	select {
	case <-waiter.done:
		ans, err := waiter.result()
		if err != nil {
			return "", err
		}
		if ans == "" {
			return "", errors.New("codex side turn returned an empty answer")
		}
		return ans, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
