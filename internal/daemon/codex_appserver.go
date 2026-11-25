package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
)

const (
	codexServerBootTimeout = 10 * time.Second
	codexRPCTimeout        = 10 * time.Second
)

// codexBridge owns a session's managed `codex app-server` subprocess and its
// WebSocket JSON-RPC observer/controller connection.
type codexBridge struct {
	sessionID    string
	cmd          *exec.Cmd
	exited       chan struct{}
	client       *codex.AppServerClient
	cleanupFiles func()

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	mu           sync.Mutex
	tid          string
	pendingPath  string // rollout path reported by app-server, bound once written on disk
	subscribed   bool
	bound        bool            // true once ConversationId + verified rollout path are bound
	ignored      map[string]bool // ephemeral / subagent thread IDs to ignore
	abandoned    map[string]bool // previous top-level thread IDs switched away from
	initUsage    *protocol.Usage
	lastSeq      int64
	activeTurn   string
	itemIndices  map[string]int32
	nextBlock    int32
	thinkingSeen map[string]bool
}

func newCodexBridge(parent context.Context, sessionID string, client *codex.AppServerClient, cmd *exec.Cmd, exited chan struct{}, cleanupFiles func()) *codexBridge {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &codexBridge{
		sessionID:    sessionID,
		cmd:          cmd,
		exited:       exited,
		client:       client,
		cleanupFiles: cleanupFiles,
		ctx:          ctx,
		cancel:       cancel,
		ignored:      map[string]bool{},
		abandoned:    map[string]bool{},
		itemIndices:  map[string]int32{},
		thinkingSeen: map[string]bool{},
	}
}

func (b *codexBridge) threadID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tid
}

func (b *codexBridge) setThread(id string, subscribed bool) {
	b.mu.Lock()
	b.tid = id
	b.subscribed = subscribed
	b.mu.Unlock()
}

func (b *codexBridge) seqFor(emittedAtMs int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	ts := time.Now().UnixNano()
	if emittedAtMs > 0 {
		ts = emittedAtMs * int64(time.Millisecond)
	}
	seq := max(ts, b.lastSeq+1)
	b.lastSeq = seq
	return seq
}

func (b *codexBridge) resetTurnLocked(turnID string) {
	b.activeTurn = turnID
	b.itemIndices = map[string]int32{}
	b.nextBlock = 0
	b.thinkingSeen = map[string]bool{}
}

func (b *codexBridge) blockIndexLocked(itemID string) int32 {
	if idx, ok := b.itemIndices[itemID]; ok {
		return idx
	}
	idx := b.nextBlock
	b.nextBlock++
	b.itemIndices[itemID] = idx
	return idx
}

// Close tears down the WebSocket client, terminates the managed app-server
// process group, and removes socket/token files. Safe to call multiple times.
func (b *codexBridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		b.cancel()
		if b.client != nil {
			_ = b.client.Close()
		}
		stopCodexServerCmd(b.cmd, b.exited)
		if b.cleanupFiles != nil {
			b.cleanupFiles()
		}
	})
}

// deliverMessage injects text into the active Codex thread via `thread/queue/add`
// (which immediately starts a turn if idle and auto-drains FIFO after the current
// turn if busy), falling back to `turn/start` on `-32600` (e.g. ephemeral thread
// or server without queue support).
func (b *codexBridge) deliverMessage(ctx context.Context, text string) error {
	if ctx == nil {
		ctx = b.ctx
	}
	tctx, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()

	tid := b.threadID()
	if tid == "" && b.client != nil {
		// On a brand-new session where the TUI creates the thread, ThreadLoadedList
		// can discover the active thread even before the thread/started notification
		// has been processed by runCodexBridge.
		if res, err := b.client.ThreadLoadedList(tctx); err == nil && len(res.Data) == 1 && res.Data[0] != "" {
			b.mu.Lock()
			if b.tid == "" && !b.ignored[res.Data[0]] {
				b.tid = res.Data[0]
			}
			tid = b.tid
			b.mu.Unlock()
		}
	}
	if tid == "" {
		return errors.New("codex app-server: no active thread")
	}

	input := []codex.UserInput{{Type: "text", Text: text}}
	_, err := b.client.ThreadQueueAdd(tctx, codex.ThreadQueueAddParams{
		ThreadID:            tid,
		ClientUserMessageID: conversationUUID(),
		Input:               input,
	})
	if err == nil {
		return nil
	}
	if codex.IsInvalidRequest(err) {
		_, err = b.client.TurnStart(tctx, codex.TurnStartParams{
			ThreadID: tid,
			Input:    input,
		})
	}
	return err
}

func (d *Daemon) supportsCodexAppServer(ctx context.Context, program string) bool {
	if program == "" {
		return false
	}
	return probed(ctx, program, []string{"app-server", "--help"}, "--listen", 3*time.Second, func(out string) bool {
		return strings.Contains(out, "--listen")
	}) && probed(ctx, program, []string{"--help"}, "--remote", 3*time.Second, func(out string) bool {
		return strings.Contains(out, "--remote")
	})
}

// prepareCodexAppServer starts a per-session `codex app-server` for native Codex
// sessions when supported by the installed CLI, establishes Mako's JSON-RPC client,
// pre-creates or resumes the thread, and rewrites launch.Command to `codex --remote`.
// If unsupported or startup fails, launch is left untouched so Mako falls back cleanly
// to direct PTY execution.
func (d *Daemon) prepareCodexAppServer(
	ctx context.Context,
	sessionID, project string,
	launch *agentspec.Launch,
	conversationID, transcriptPath *string,
) (*codexBridge, func()) {
	noop := func() {}
	if len(launch.Command) == 0 {
		return nil, noop
	}
	if _, native := agents.Native("codex", launch.Command[0]); !native {
		return nil, noop
	}
	split := codex.SplitRemoteLaunch(launch.Command)
	if split.SkipRemote {
		return nil, noop
	}
	if !d.supportsCodexAppServer(ctx, split.Program) {
		return nil, noop
	}

	runDir, err := config.RunDir(d.dir)
	if err != nil {
		d.log.Warn("codex app-server: no run dir; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}

	tr, err := newCodexAppServerTransport(runDir, sessionID)
	if err != nil {
		d.log.Warn("codex app-server: transport setup failed; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}

	serverArgs := make([]string, 0, 3+len(tr.extraServerArgs)+len(split.ServerArgs))
	serverArgs = append(serverArgs, "app-server", "--listen", tr.listenArg)
	serverArgs = append(serverArgs, tr.extraServerArgs...)
	serverArgs = append(serverArgs, split.ServerArgs...)

	cmd := exec.Command(split.Program, serverArgs...)
	cmd.Dir = project
	cmd.Stdout = io.Discard
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		tr.cleanupFiles()
		d.log.Warn("codex app-server: stderr pipe failed; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}
	serverEnv := maps.Clone(launch.Env)
	if serverEnv == nil {
		serverEnv = map[string]string{}
	}
	// Hooks configured via -c run inside codex app-server; pass MAKO_SESSION so
	// `mako hook` routes events back to this session.
	serverEnv["MAKO_SESSION"] = sessionID
	cmd.Env = mergeEnvSlice(os.Environ(), serverEnv)
	configureCodexServerCmd(cmd)

	if err := safeexec.Start(cmd); err != nil {
		tr.cleanupFiles()
		d.log.Warn("codex app-server: start failed; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	bootCtx, bootCancel := context.WithTimeout(ctx, codexServerBootTimeout)
	defer bootCancel()

	client, remoteAddr, err := tr.waitReady(bootCtx, exited, stderrPipe)
	if err != nil {
		stopCodexServerCmd(cmd, exited)
		tr.cleanupFiles()
		d.log.Warn("codex app-server: not ready; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}

	bridge := newCodexBridge(d.ctx, sessionID, client, cmd, exited, tr.cleanupFiles)
	fail := func(msg string, err error) (*codexBridge, func()) {
		d.log.Warn("codex app-server: "+msg+"; falling back to direct PTY", "session", sessionID, "err", err)
		bridge.Close()
		return nil, noop
	}

	if _, err := client.Initialize(bootCtx, "mako", config.Version, true); err != nil {
		return fail("initialize failed", err)
	}

	initUsage := &protocol.Usage{}
	if rl, err := client.RateLimitsRead(bootCtx); err == nil {
		applyRateLimits(initUsage, rl.RateLimits)
	}

	cwd := cmp.Or(split.Cwd, project)
	targetID := cmp.Or(*conversationID, split.ResumeID)
	var threadID, threadPath string
	if targetID != "" {
		res, err := client.ThreadResume(bootCtx, codex.ThreadResumeParams{
			ThreadID:       targetID,
			ExcludeTurns:   true,
			Cwd:            cwd,
			Model:          split.Model,
			ApprovalPolicy: split.ApprovalPolicy,
			Sandbox:        split.Sandbox,
		})
		if err != nil {
			return fail("thread/resume failed", err)
		}
		threadID = cmp.Or(res.Thread.ID, targetID)
		if threadID == "" {
			return fail("empty thread id", errors.New("server returned empty thread id"))
		}
		threadPath = res.Thread.Path
		applyThreadMetadata(initUsage, res.Model, res.ReasoningEffort, res.ApprovalPolicy, res.Sandbox)

		bridge.mu.Lock()
		bridge.tid = threadID
		bridge.pendingPath = threadPath
		bridge.subscribed = true
		if !initUsage.IsZero() {
			bridge.initUsage = initUsage
		}
		*conversationID = threadID
		if threadPath != "" && *transcriptPath == "" {
			if _, err := os.Stat(threadPath); err == nil {
				*transcriptPath = threadPath
			}
		}
		bridge.bound = *transcriptPath != ""
		bridge.mu.Unlock()
	} else {
		// Brand-new session: let the connected `--remote` TUI bootstrap its own
		// thread on this managed app-server rather than pre-creating an unmaterialized
		// 0-turn thread and passing `resume <id>`, which Codex rejects with
		// `no rollout found for thread id ... (code -32600)`.
		// The bridge receives `thread/started` as soon as the TUI creates the thread
		// and subscribes via `thread/resume` once the rollout materializes.
		bridge.mu.Lock()
		if !initUsage.IsZero() {
			bridge.initUsage = initUsage
		}
		bridge.mu.Unlock()
	}

	// Build the rewritten `--remote` TUI command:
	// - Resumed session: `codex --remote <addr> [auth...] resume [TUI_FLAGS...] [--] <threadID> [PROMPT]`
	// - New session:     `codex --remote <addr> [auth...] [TUI_FLAGS...] [--] [PROMPT]`
	// Place `--` before positional args when a prompt is present so prompts starting
	// with `-` (e.g. `-fix-me`) are never misparsed as CLI flags.
	tuiCmd := make([]string, 0, 6+len(tr.extraTUIArgs)+len(split.TUIArgs))
	tuiCmd = append(tuiCmd, split.Program, "--remote", remoteAddr)
	tuiCmd = append(tuiCmd, tr.extraTUIArgs...)
	if targetID != "" {
		tuiCmd = append(tuiCmd, "resume")
		tuiCmd = append(tuiCmd, split.TUIArgs...)
		if split.HasPrompt {
			tuiCmd = append(tuiCmd, "--", threadID, split.Prompt)
		} else {
			tuiCmd = append(tuiCmd, threadID)
		}
	} else {
		tuiCmd = append(tuiCmd, split.TUIArgs...)
		if split.HasPrompt {
			tuiCmd = append(tuiCmd, "--", split.Prompt)
		}
	}
	launch.Command = tuiCmd
	for k, v := range tr.extraTUIEnv {
		launch.SetEnv(k, v)
	}

	d.log.Info("codex app-server ready", "session", sessionID, "thread", threadID, "remote", remoteAddr)
	return bridge, bridge.Close
}

func (d *Daemon) attachCodexBridge(s *session.Session, b *codexBridge) {
	if s == nil || b == nil {
		return
	}
	d.codexMu.Lock()
	if d.codexBridges == nil {
		d.codexBridges = map[string]*codexBridge{}
	}
	d.codexBridges[s.ID()] = b
	d.codexMu.Unlock()

	if b.initUsage != nil && !b.initUsage.IsZero() {
		s.Report(b.initUsage)
	}
	go d.runCodexBridge(s, b)
}

func (d *Daemon) codexBridge(sessionID string) *codexBridge {
	d.codexMu.Lock()
	defer d.codexMu.Unlock()
	return d.codexBridges[sessionID]
}

func (d *Daemon) removeCodexBridge(sessionID string, b *codexBridge) {
	d.codexMu.Lock()
	if d.codexBridges[sessionID] == b {
		delete(d.codexBridges, sessionID)
	}
	d.codexMu.Unlock()
}

func (d *Daemon) closeAllCodexBridges() {
	d.codexMu.Lock()
	bridges := make([]*codexBridge, 0, len(d.codexBridges))
	for id, b := range d.codexBridges {
		bridges = append(bridges, b)
		delete(d.codexBridges, id)
	}
	d.codexMu.Unlock()
	for _, b := range bridges {
		b.Close()
	}
}

func (d *Daemon) deliverViaCodexBridge(ctx context.Context, sessionID, text string) bool {
	b := d.codexBridge(sessionID)
	if b == nil {
		return false
	}
	if err := b.deliverMessage(ctx, text); err != nil {
		d.log.Warn("codex app-server delivery failed; falling back to PTY", "session", sessionID, "err", err)
		return false
	}
	return true
}

func (d *Daemon) runCodexBridge(s *session.Session, b *codexBridge) {
	defer d.removeCodexBridge(s.ID(), b)
	notifs := b.client.Notifications()
	var daemonDone <-chan struct{}
	if d.ctx != nil {
		daemonDone = d.ctx.Done()
	}
	for {
		select {
		case n, ok := <-notifs:
			if !ok {
				return
			}
			d.handleCodexNotification(s, b, n)
		case <-b.ctx.Done():
			return
		case <-s.Done():
			b.Close()
			return
		case <-daemonDone:
			b.Close()
			return
		}
	}
}

func (d *Daemon) handleCodexNotification(s *session.Session, b *codexBridge, n codex.Notification) {
	switch n.Method {
	case "thread/started":
		var p codex.ThreadStartedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.Thread.ID == "" {
			return
		}
		if p.Thread.Ephemeral || p.Thread.IsSubagent() {
			b.mu.Lock()
			b.ignored[p.Thread.ID] = true
			b.mu.Unlock()
			return
		}
		b.mu.Lock()
		if p.Thread.ID == b.tid {
			if p.Thread.Path != "" && b.pendingPath == "" {
				b.pendingPath = p.Thread.Path
			}
			b.mu.Unlock()
			d.maybeBindCodexThread(s, b)
			return
		}
		// User switched to a new top-level conversation inside the TUI (e.g. /new).
		if b.tid != "" {
			b.abandoned[b.tid] = true
		}
		delete(b.abandoned, p.Thread.ID)
		b.tid = p.Thread.ID
		b.pendingPath = p.Thread.Path
		b.subscribed = false
		b.bound = false
		b.mu.Unlock()
		d.maybeBindCodexThread(s, b)

	case "thread/status/changed":
		var p codex.ThreadStatusChangedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID == "" {
			return
		}
		b.mu.Lock()
		curTID := b.tid
		subscribed := b.subscribed
		ignored := b.ignored[p.ThreadID]
		abandoned := b.abandoned[p.ThreadID]
		b.mu.Unlock()

		if ignored {
			return
		}

		// Detect in-TUI /resume to another top-level thread, verifying it is neither
		// ephemeral nor a subagent before switching, and never re-switching back to
		// an abandoned previous thread merely because it went idle.
		if p.ThreadID != curTID {
			if p.Status.Type != "active" && (p.Status.Type != "idle" || abandoned) {
				return
			}
			if !d.maybeSwitchCodexThread(s, b, p.ThreadID, p.Status.Type) {
				return
			}
			curTID = p.ThreadID
			b.mu.Lock()
			subscribed = b.subscribed
			b.mu.Unlock()
		}

		if p.ThreadID == curTID && (p.Status.Type == "active" || p.Status.Type == "idle") {
			if !subscribed {
				d.trySubscribeCodexThread(s, b, p.ThreadID)
			}
			d.maybeBindCodexThread(s, b)
		}
		if p.ThreadID != b.threadID() {
			return
		}

		seq := b.seqFor(n.EmittedAtMs)
		switch p.Status.Type {
		case "active":
			if slices.Contains(p.Status.ActiveFlags, "waitingOnApproval") || slices.Contains(p.Status.ActiveFlags, "waitingOnUserInput") {
				s.ReportToolStatus(protocol.AgentWaiting, seq)
			} else {
				// Use ReportToolStatus so mid-turn active updates never withdraw a
				// live PermissionRequest approval dialog.
				s.ReportToolStatus(protocol.AgentWorking, seq)
			}
		case "idle":
			// Clear AgentWaiting once approval/input resolves without clobbering AgentDone.
			if s.Info().GetStatus() == protocol.AgentWaiting {
				s.ReportStatus(protocol.AgentIdle, seq)
			}
		case "systemError":
			if s.Info().GetStatus() != protocol.AgentDone {
				s.ReportStatus(protocol.AgentIdle, seq)
			}
		}

	case "turn/started":
		var p codex.TurnNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		step := cmp.Or(p.Turn.ID, "turn")
		b.mu.Lock()
		b.resetTurnLocked(step)
		b.mu.Unlock()
		d.maybeBindCodexThread(s, b)
		s.ReportStatus(protocol.AgentWorking, b.seqFor(n.EmittedAtMs))
		d.live.take(livePost{Session: s.ID(), Step: step})

	case "item/started":
		var p codex.ItemNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() || p.Item.ID == "" {
			return
		}
		b.mu.Lock()
		step := cmp.Or(p.TurnID, b.activeTurn, "turn")
		idx := b.blockIndexLocked(p.Item.ID)
		var pieces []livePiece
		switch p.Item.Type {
		case "reasoning":
			if !b.thinkingSeen[p.Item.ID] {
				b.thinkingSeen[p.Item.ID] = true
				pieces = append(pieces, livePiece{Kind: "thinking", Index: idx})
			}
		case "commandExecution":
			pieces = append(pieces, livePiece{Kind: "tool", Index: idx, Tool: "Bash"})
			if p.Item.Command != "" {
				pieces = append(pieces, livePiece{Kind: "input", Index: idx, Text: p.Item.Command})
			}
		case "fileChange":
			pieces = append(pieces, livePiece{Kind: "tool", Index: idx, Tool: "Edit"})
		case "mcpToolCall":
			pieces = append(pieces, livePiece{Kind: "tool", Index: idx, Tool: cmp.Or(p.Item.Tool, p.Item.Server, "mcp")})
		case "webSearch":
			pieces = append(pieces, livePiece{Kind: "tool", Index: idx, Tool: "WebSearch"})
			if p.Item.Query != "" {
				pieces = append(pieces, livePiece{Kind: "input", Index: idx, Text: p.Item.Query})
			}
		case "collabAgentToolCall":
			pieces = append(pieces, livePiece{Kind: "tool", Index: idx, Tool: cmp.Or(p.Item.Tool, "Agent")})
		}
		b.mu.Unlock()
		if len(pieces) > 0 {
			d.live.take(livePost{Session: s.ID(), Step: step, Pieces: pieces})
		}

	case "item/agentMessage/delta":
		var p codex.AgentMessageDeltaNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() || p.Delta == "" {
			return
		}
		b.mu.Lock()
		step := cmp.Or(p.TurnID, b.activeTurn, "turn")
		idx := b.blockIndexLocked(cmp.Or(p.ItemID, "msg"))
		b.mu.Unlock()
		d.live.take(livePost{
			Session: s.ID(),
			Step:    step,
			Pieces:  []livePiece{{Kind: "text", Index: idx, Text: p.Delta}},
		})

	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var p codex.ReasoningDeltaNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		itemID := cmp.Or(p.ItemID, "reasoning")
		b.mu.Lock()
		if b.thinkingSeen[itemID] {
			b.mu.Unlock()
			return
		}
		b.thinkingSeen[itemID] = true
		step := cmp.Or(p.TurnID, b.activeTurn, "turn")
		idx := b.blockIndexLocked(itemID)
		b.mu.Unlock()
		d.live.take(livePost{
			Session: s.ID(),
			Step:    step,
			Pieces:  []livePiece{{Kind: "thinking", Index: idx}},
		})

	case "turn/completed":
		var p codex.TurnNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		b.mu.Lock()
		step := cmp.Or(p.Turn.ID, b.activeTurn, "turn")
		b.mu.Unlock()
		d.live.take(livePost{Session: s.ID(), Step: step, End: true})
		d.maybeBindCodexThread(s, b)
		seq := b.seqFor(n.EmittedAtMs)
		if p.Turn.Status == "completed" {
			d.refreshActivity(s)
			s.ReportStatus(protocol.AgentDone, seq)
		} else {
			s.ReportStatus(protocol.AgentIdle, seq)
		}

	case "thread/tokenUsage/updated":
		var p codex.ThreadTokenUsageNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		u := tokenUsageToProto(p.TokenUsage)
		if !u.IsZero() {
			s.Report(u)
		}

	case "account/rateLimits/updated":
		var p codex.AccountRateLimitsUpdatedNotification
		if json.Unmarshal(n.Params, &p) != nil {
			return
		}
		u := &protocol.Usage{}
		applyRateLimits(u, p.RateLimits)
		if !u.IsZero() {
			s.Report(u)
		}
	}
}

func (d *Daemon) maybeSwitchCodexThread(s *session.Session, b *codexBridge, threadID, statusType string) bool {
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	res, err := b.client.ThreadRead(ctx, threadID, false)
	if err != nil {
		return false
	}
	if res.Thread.Ephemeral || res.Thread.IsSubagent() {
		b.mu.Lock()
		b.ignored[threadID] = true
		b.mu.Unlock()
		return false
	}
	b.mu.Lock()
	if b.abandoned[threadID] && statusType != "active" {
		b.mu.Unlock()
		return false
	}
	if b.tid != "" && b.tid != threadID {
		b.abandoned[b.tid] = true
	}
	delete(b.abandoned, threadID)
	b.tid = threadID
	b.pendingPath = res.Thread.Path
	b.subscribed = false
	b.bound = false
	b.mu.Unlock()
	return true
}

func (d *Daemon) trySubscribeCodexThread(s *session.Session, b *codexBridge, threadID string) {
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	res, err := b.client.ThreadResume(ctx, codex.ThreadResumeParams{
		ThreadID:     threadID,
		ExcludeTurns: true,
	})
	if err != nil {
		return
	}
	b.mu.Lock()
	if b.tid == threadID {
		b.subscribed = true
		if res.Thread.Path != "" {
			b.pendingPath = res.Thread.Path
		}
	}
	b.mu.Unlock()
	u := &protocol.Usage{}
	applyThreadMetadata(u, res.Model, res.ReasoningEffort, res.ApprovalPolicy, res.Sandbox)
	if !u.IsZero() && s != nil {
		s.Report(u)
	}
}

// maybeBindCodexThread binds the active thread's ConversationId and rollout path
// once its rollout file actually exists on disk and verifies.
func (d *Daemon) maybeBindCodexThread(s *session.Session, b *codexBridge) {
	b.mu.Lock()
	tid := b.tid
	path := b.pendingPath
	bound := b.bound
	b.mu.Unlock()
	if tid == "" || bound {
		return
	}
	if path == "" && b.client != nil {
		ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
		res, err := b.client.ThreadRead(ctx, tid, false)
		cancel()
		if err == nil && res.Thread.Path != "" {
			path = res.Thread.Path
			b.mu.Lock()
			if b.tid == tid {
				b.pendingPath = path
			}
			b.mu.Unlock()
		}
	}
	verified := verifiedTranscriptPath(s, tid, path)
	if verified == "" {
		return
	}
	if err := d.bindConversation(s, tid, verified); err == nil {
		b.mu.Lock()
		if b.tid == tid {
			b.bound = true
		}
		b.mu.Unlock()
	}
}

func verifiedTranscriptPath(s *session.Session, threadID, path string) string {
	if path == "" || s == nil {
		return ""
	}
	info := s.Info()
	info.ConversationId = threadID
	if transcript.Verify(info, path) == nil {
		return path
	}
	return ""
}

func tokenUsageToProto(tu codex.ThreadTokenUsage) *protocol.Usage {
	input := max(tu.Total.InputTokens-tu.Total.CachedInputTokens, 0)
	u := &protocol.Usage{
		Input:       input,
		CachedInput: tu.Total.CachedInputTokens,
		Output:      tu.Total.OutputTokens,
		Context:     tu.Last.InputTokens,
	}
	if tu.ModelContextWindow != nil && *tu.ModelContextWindow > 0 {
		u.Window = *tu.ModelContextWindow
	}
	return u
}

func applyRateLimits(u *protocol.Usage, snap codex.RateLimitSnapshot) {
	for _, w := range []*codex.RateLimitWindow{snap.Primary, snap.Secondary} {
		if w != nil && w.WindowDurationMins > 0 {
			u.SetLimit(&protocol.RateLimit{
				Minutes: w.WindowDurationMins,
				Used:    w.UsedPercent,
				Resets:  w.ResetsAt,
			})
		}
	}
}

func applyThreadMetadata(u *protocol.Usage, model, effort string, rawApproval, rawSandbox json.RawMessage) {
	if model != "" {
		u.Model = model
	}
	if effort != "" {
		u.Effort = effort
	}
	var approval string
	_ = json.Unmarshal(rawApproval, &approval)
	var sandbox struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(rawSandbox, &sandbox)
	switch {
	case (sandbox.Type == "dangerFullAccess" || sandbox.Type == "danger-full-access") && approval == "never":
		u.PermissionMode = "bypassPermissions"
	case sandbox.Type == "workspaceWrite" || sandbox.Type == "workspace-write":
		u.PermissionMode = "acceptEdits"
	case sandbox.Type == "readOnly" || sandbox.Type == "read-only":
		u.PermissionMode = "readOnly"
	case sandbox.Type != "":
		u.PermissionMode = "default"
	}
}

func mergeEnvSlice(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[k]; replaced {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
