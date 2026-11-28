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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
	"google.golang.org/protobuf/proto"
)

const (
	codexServerBootTimeout   = 10 * time.Second
	codexRPCTimeout          = 10 * time.Second
	codexStartupQueueTimeout = 60 * time.Second
	codexStartupMaxAttempts  = 3
	codexStderrTailMaxBytes  = 8192
	codexBGCommandMaxLen     = 1000
	codexTurnLookupTimeout   = 500 * time.Millisecond
)

var (
	codexBGPollInterval   = 5 * time.Second
	codexAbortGracePeriod = 3 * time.Second
)

// stderrTail retains the last codexStderrTailMaxBytes written to it so startup
// failures can inspect Codex's stderr (e.g. SQLite runtime initialization errors).
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > codexStderrTailMaxBytes {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-codexStderrTailMaxBytes:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *stderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func isCodexSQLiteStartupError(stderrText string) bool {
	return strings.Contains(strings.ToLower(stderrText), "failed to initialize sqlite state runtime")
}

func isCodexArchivedThreadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is archived") || strings.Contains(msg, "archived thread")
}

func isCodexNoArchivedRolloutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no archived rollout found")
}

func isCodexTurnBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") ||
		strings.Contains(msg, "in progress") ||
		strings.Contains(msg, "active turn") ||
		strings.Contains(msg, "turn already")
}

func threadSettingsHasServiceTier(rawParams json.RawMessage) bool {
	var env struct {
		ThreadSettings map[string]json.RawMessage `json:"threadSettings"`
	}
	if json.Unmarshal(rawParams, &env) != nil || env.ThreadSettings == nil {
		return false
	}
	_, ok := env.ThreadSettings["serviceTier"]
	return ok
}

func codexStateDirKey(env map[string]string) string {
	if h := strings.TrimSpace(env["CODEX_HOME"]); h != "" {
		return filepath.Clean(h)
	}
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return filepath.Clean(h)
	}
	home := strings.TrimSpace(env["HOME"])
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		return filepath.Clean(filepath.Join(home, ".codex"))
	}
	return ".codex"
}

// codexStartupCoordinator serializes `codex app-server` startup per CODEX_HOME
// so concurrent session boots do not race on Codex's shared SQLite state DB.
type codexStartupCoordinator struct {
	mu    sync.Mutex
	tails map[string]chan struct{}
}

func (c *codexStartupCoordinator) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.tails == nil {
		c.tails = map[string]chan struct{}{}
	}
	prev := c.tails[key]
	mine := make(chan struct{})
	c.tails[key] = mine
	c.mu.Unlock()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(mine)
			c.mu.Lock()
			if c.tails[key] == mine {
				delete(c.tails, key)
			}
			c.mu.Unlock()
		})
	}
	if prev != nil {
		select {
		case <-prev:
		case <-ctx.Done():
			// Chain release only after prev completes so a cancelled waiter never
			// lets later waiters bypass the active owner.
			go func() {
				<-prev
				release()
			}()
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// resumeCodexThread calls thread/resume and, if Codex reports the thread is
// archived, calls thread/unarchive (ignoring "no archived rollout found") and
// retries thread/resume once.
func resumeCodexThread(ctx context.Context, client *codex.AppServerClient, params codex.ThreadResumeParams) (codex.ThreadResumeResult, error) {
	res, err := client.ThreadResume(ctx, params)
	if err == nil || !isCodexArchivedThreadError(err) {
		return res, err
	}
	if unarchErr := client.ThreadUnarchive(ctx, params.ThreadID); unarchErr != nil && !isCodexNoArchivedRolloutError(unarchErr) {
		return codex.ThreadResumeResult{}, unarchErr
	}
	return client.ThreadResume(ctx, params)
}

type codexServerLaunchConfig struct {
	runDir      string
	sessionID   string
	project     string
	split       codex.RemoteLaunchSplit
	serverEnv   map[string]string
	stateDirKey string
}

type codexStartedServer struct {
	tr         *codexAppServerTransport
	cmd        *exec.Cmd
	exited     chan struct{}
	client     *codex.AppServerClient
	remoteAddr string
	initRes    codex.InitializeResult
}

type codexAbortOptions struct {
	GracePeriod           time.Duration
	ForceRestartOnTimeout *bool // nil defaults to true
}

type codexAbortResult struct {
	HadActiveTurn bool
	Aborted       bool
	ForcedRestart bool
	ResumedThread bool
	Err           error
}

type inFlightAbort struct {
	done   chan struct{}
	result codexAbortResult
}

// codexRestartOutcome holds the newly started server and TUI launch spec
// produced during force-recovery restart.
type codexRestartOutcome struct {
	client        *codex.AppServerClient
	cmd           *exec.Cmd
	exited        chan struct{}
	cleanupFiles  func()
	userAgent     string
	initUsage     *protocol.Usage
	initGoal      *protocol.GoalStatus
	initFastMode  bool
	threadID      string
	threadPath    string
	resumedThread bool
	tuiCmd        []string
	tuiEnv        map[string]string
}

// codexBridge owns a session's managed `codex app-server` subprocess and its
// WebSocket JSON-RPC observer/controller connection.
type codexBridge struct {
	sessionID string

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	launchCfg     *codexServerLaunchConfig
	restartServer func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error)

	mu                  sync.Mutex
	closed              bool
	epoch               uint64
	restarting          bool
	swapped             chan struct{} // closed and replaced on client/restart state changes
	cmd                 *exec.Cmd
	exited              chan struct{}
	client              *codex.AppServerClient
	cleanupFiles        func()
	serverUserAgent     string
	tid                 string
	pendingPath         string // rollout path reported by app-server, bound once written on disk
	subscribed          bool
	bound               bool            // true once ConversationId + verified rollout path are bound
	ignored             map[string]bool // ephemeral / subagent thread IDs to ignore
	abandoned           map[string]bool // previous top-level thread IDs switched away from
	initUsage           *protocol.Usage
	initGoal            *protocol.GoalStatus
	goalUnsupported     bool
	fastMode            bool
	initFastMode        bool
	serviceTierOverride string
	serviceTierDirty    bool
	lastSeq             int64
	activeTurn          string          // active liveHub step label
	activeTurnID        string          // server-assigned turn ID when known
	turnActive          bool            // true while a turn is in flight
	turnDone            chan struct{}   // closed when active turn settles
	completedTurns      map[string]bool // dedup turn/completed and codex/event completions
	aborting            *inFlightAbort  // non-nil while an abort is in progress
	itemIndices         map[string]int32
	nextBlock           int32
	thinkingSeen        map[string]bool
	fgItems             map[string]bool
	fgProcesses         map[string]bool
	bgUnsupported       bool
	bgInFlight          bool
	bgDirty             bool
	bgTimer             *time.Timer
	bgStarted           map[string]int64
	bgHasTasks          bool
}

func newCodexBridge(parent context.Context, sessionID string, client *codex.AppServerClient, cmd *exec.Cmd, exited chan struct{}, cleanupFiles func()) *codexBridge {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	b := &codexBridge{
		sessionID:      sessionID,
		epoch:          1,
		swapped:        make(chan struct{}),
		cmd:            cmd,
		exited:         exited,
		client:         client,
		cleanupFiles:   cleanupFiles,
		ctx:            ctx,
		cancel:         cancel,
		ignored:        map[string]bool{},
		abandoned:      map[string]bool{},
		completedTurns: map[string]bool{},
		itemIndices:    map[string]int32{},
		thinkingSeen:   map[string]bool{},
		fgItems:        map[string]bool{},
		fgProcesses:    map[string]bool{},
		bgStarted:      map[string]int64{},
	}
	if exited != nil {
		b.watchProcessExit(exited, 1)
	}
	return b
}

func (b *codexBridge) ensureInitLocked() {
	if b.epoch == 0 {
		b.epoch = 1
	}
	if b.swapped == nil {
		b.swapped = make(chan struct{})
	}
	if b.ignored == nil {
		b.ignored = map[string]bool{}
	}
	if b.abandoned == nil {
		b.abandoned = map[string]bool{}
	}
	if b.completedTurns == nil {
		b.completedTurns = map[string]bool{}
	}
	if b.itemIndices == nil {
		b.itemIndices = map[string]int32{}
	}
	if b.thinkingSeen == nil {
		b.thinkingSeen = map[string]bool{}
	}
	if b.fgItems == nil {
		b.fgItems = map[string]bool{}
	}
	if b.fgProcesses == nil {
		b.fgProcesses = map[string]bool{}
	}
	if b.bgStarted == nil {
		b.bgStarted = map[string]int64{}
	}
}

func (b *codexBridge) watchProcessExit(done <-chan struct{}, gen uint64) {
	go func() {
		select {
		case <-done:
			b.onProcessExit(gen)
		case <-b.ctx.Done():
		}
	}()
}

// onProcessExit handles a managed app-server process exiting. Exits belonging
// to superseded epochs or occurring during an intentional restart are ignored
// so stale process exits cannot corrupt a newly started generation.
func (b *codexBridge) onProcessExit(gen uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.restarting || b.epoch != gen {
		return
	}
	b.settleTurnLocked()
}

func (b *codexBridge) signalSwapLocked() {
	if b.swapped != nil {
		close(b.swapped)
	}
	b.swapped = make(chan struct{})
}

func (b *codexBridge) currentClient() *codex.AppServerClient {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client
}

func (b *codexBridge) activeStream() (*codex.AppServerClient, uint64, <-chan struct{}, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureInitLocked()
	return b.client, b.epoch, b.swapped, b.restarting
}

func (b *codexBridge) isCurrentEpoch(gen uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && b.epoch == gen
}

func (b *codexBridge) shouldContinueAfterEOF(gen uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.ctx.Err() != nil {
		return false
	}
	return b.restarting || b.epoch != gen
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

func (b *codexBridge) advanceSeq(watermark int64) {
	b.mu.Lock()
	if watermark > b.lastSeq {
		b.lastSeq = watermark
	}
	b.mu.Unlock()
}

func (b *codexBridge) resetTurnLocked(turnID string) {
	b.activeTurn = turnID
	b.itemIndices = map[string]int32{}
	b.nextBlock = 0
	b.thinkingSeen = map[string]bool{}
	b.fgItems = map[string]bool{}
	b.fgProcesses = map[string]bool{}
}

func (b *codexBridge) noteTurnStartedLocked(turnID string) {
	b.turnActive = true
	if turnID != "" {
		b.activeTurnID = turnID
		delete(b.completedTurns, turnID)
	}
	if b.turnDone == nil {
		b.turnDone = make(chan struct{})
	}
}

func (b *codexBridge) settleTurnLocked() {
	b.turnActive = false
	b.activeTurnID = ""
	if b.turnDone != nil {
		close(b.turnDone)
		b.turnDone = nil
	}
}

func (b *codexBridge) isStaleTurnLocked(turnID string) bool {
	return b.turnActive && b.activeTurnID != "" && turnID != "" && b.activeTurnID != turnID
}

func (b *codexBridge) markTurnCompletedLocked(turnID string) bool {
	if turnID == "" {
		return true
	}
	if b.completedTurns == nil {
		b.completedTurns = map[string]bool{}
	}
	if b.completedTurns[turnID] {
		return false
	}
	if len(b.completedTurns) >= 64 {
		b.completedTurns = map[string]bool{}
	}
	b.completedTurns[turnID] = true
	return true
}

func (b *codexBridge) waitAbort(ctx context.Context) error {
	for {
		b.mu.Lock()
		inFlight := b.aborting
		b.mu.Unlock()
		if inFlight == nil {
			return nil
		}
		select {
		case <-inFlight.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-b.ctx.Done():
			return b.ctx.Err()
		}
	}
}

func (b *codexBridge) blockIndexLocked(itemID string) int32 {
	if b.itemIndices == nil {
		b.itemIndices = map[string]int32{}
	}
	if idx, ok := b.itemIndices[itemID]; ok {
		return idx
	}
	idx := b.nextBlock
	b.nextBlock++
	b.itemIndices[itemID] = idx
	return idx
}

func (b *codexBridge) userAgent() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.serverUserAgent
}

// Close tears down the WebSocket client, terminates the managed app-server
// process group, and removes socket/token files. Safe to call multiple times.
func (b *codexBridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		b.cancel()
		b.mu.Lock()
		b.closed = true
		b.epoch++
		b.restarting = false
		if b.bgTimer != nil {
			b.bgTimer.Stop()
			b.bgTimer = nil
		}
		b.settleTurnLocked()
		client := b.client
		cmd := b.cmd
		exited := b.exited
		cleanupFiles := b.cleanupFiles
		b.client = nil
		b.cmd = nil
		b.cleanupFiles = nil
		b.signalSwapLocked()
		b.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		stopCodexServerCmd(cmd, exited)
		if cleanupFiles != nil {
			cleanupFiles()
		}
	})
}

func (b *codexBridge) scheduleBackgroundRefresh(s *session.Session) {
	if b == nil || s == nil {
		return
	}
	b.mu.Lock()
	if b.client == nil || b.bgUnsupported || b.tid == "" || b.ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	if b.bgTimer != nil {
		b.bgTimer.Stop()
		b.bgTimer = nil
	}
	if b.bgInFlight {
		b.bgDirty = true
		b.mu.Unlock()
		return
	}
	b.bgInFlight = true
	b.bgDirty = false
	b.mu.Unlock()

	go b.runBackgroundRefresh(s)
}

func (b *codexBridge) runBackgroundRefresh(s *session.Session) {
	for {
		b.mu.Lock()
		tid := b.tid
		unsupported := b.bgUnsupported
		client := b.client
		epoch := b.epoch
		b.mu.Unlock()
		if unsupported || tid == "" || client == nil || b.ctx.Err() != nil {
			b.mu.Lock()
			b.bgInFlight = false
			b.bgDirty = false
			b.mu.Unlock()
			return
		}

		rows, err := b.listBackgroundTerminals(client, tid)

		b.mu.Lock()
		if b.epoch != epoch || b.closed {
			b.bgInFlight = false
			b.bgDirty = false
			b.mu.Unlock()
			return
		}
		if err != nil {
			if codex.IsMethodNotFound(err) {
				b.bgUnsupported = true
				b.bgInFlight = false
				b.bgDirty = false
				b.mu.Unlock()
				return
			}
			// Transient error: re-poll later if we previously saw live tasks.
			retry := b.bgHasTasks && b.ctx.Err() == nil
			dirty := b.bgDirty && b.ctx.Err() == nil
			if dirty {
				b.bgDirty = false
				b.mu.Unlock()
				continue
			}
			b.bgInFlight = false
			if retry {
				b.bgTimer = time.AfterFunc(codexBGPollInterval, func() {
					b.scheduleBackgroundRefresh(s)
				})
			}
			b.mu.Unlock()
			return
		}

		// Filter out commands currently executing in the active foreground turn.
		now := time.Now().Unix()
		seen := make(map[string]bool, len(rows))
		var tasks []*protocol.BackgroundTask
		for _, row := range rows {
			if row.ProcessID == "" {
				continue
			}
			if (row.ItemID != "" && b.fgItems[row.ItemID]) || b.fgProcesses[row.ProcessID] {
				continue
			}
			seen[row.ProcessID] = true
			started := b.bgStarted[row.ProcessID]
			if started == 0 {
				started = now
				b.bgStarted[row.ProcessID] = started
			}
			cmd := row.Command
			if len(cmd) > codexBGCommandMaxLen {
				cmd = cmd[:codexBGCommandMaxLen]
			}
			tasks = append(tasks, &protocol.BackgroundTask{
				Id:      row.ProcessID,
				CallId:  row.ItemID,
				Command: cmd,
				Running: true,
				Started: started,
			})
		}
		for pid := range b.bgStarted {
			if !seen[pid] {
				delete(b.bgStarted, pid)
			}
		}
		b.bgHasTasks = len(tasks) > 0
		dirty := b.bgDirty && b.ctx.Err() == nil
		if dirty {
			b.bgDirty = false
		}
		if !dirty {
			b.bgInFlight = false
			if b.bgHasTasks && b.ctx.Err() == nil {
				b.bgTimer = time.AfterFunc(codexBGPollInterval, func() {
					b.scheduleBackgroundRefresh(s)
				})
			}
		}
		b.mu.Unlock()

		s.ReportBridgeTasks(tasks)

		if !dirty {
			return
		}
	}
}

func (b *codexBridge) listBackgroundTerminals(client *codex.AppServerClient, tid string) ([]codex.BackgroundTerminal, error) {
	if client == nil {
		return nil, errors.New("codex app-server: disconnected")
	}
	tctx, cancel := context.WithTimeout(b.ctx, codexRPCTimeout)
	defer cancel()
	return client.ThreadBackgroundTerminalsList(tctx, tid)
}

func (b *codexBridge) resolveThreadID(ctx context.Context, client *codex.AppServerClient) (string, error) {
	tid := b.threadID()
	if tid == "" && client != nil {
		// On a brand-new session where the TUI creates the thread, ThreadLoadedList
		// can discover the active thread even before the thread/started notification
		// has been processed by runCodexBridge.
		if res, err := client.ThreadLoadedList(ctx); err == nil && len(res.Data) == 1 && res.Data[0] != "" {
			b.mu.Lock()
			if b.tid == "" && !b.ignored[res.Data[0]] {
				b.tid = res.Data[0]
			}
			tid = b.tid
			b.mu.Unlock()
		}
	}
	if tid == "" {
		return "", errors.New("codex app-server: no active thread")
	}
	return tid, nil
}

func (b *codexBridge) goalActionCapabilities() (canEdit, canClear bool) {
	b.mu.Lock()
	supported := !b.goalUnsupported
	b.mu.Unlock()
	return supported, supported
}

func (b *codexBridge) markGoalUnsupported(s *session.Session) {
	b.mu.Lock()
	b.goalUnsupported = true
	b.mu.Unlock()
	if s == nil {
		return
	}
	if cur := s.Info().GetGoal(); cur != nil && cur.Active() && (cur.GetCanEdit() || cur.GetCanClear()) {
		cur.CanEdit = false
		cur.CanClear = false
		s.ReportBridgeGoal(cur)
	}
}

func isCodexGoalUnsupportedError(err error) bool {
	if codex.IsMethodNotFound(err) {
		return true
	}
	var rpcErr *codex.RPCError
	if errors.As(err, &rpcErr) {
		msg := strings.ToLower(rpcErr.Message)
		return strings.Contains(msg, "unknown method") || strings.Contains(msg, "method not found") || strings.Contains(msg, "unsupported")
	}
	return false
}

// setGoal updates or sets the active Codex thread's goal via `thread/goal/set`.
func (b *codexBridge) setGoal(ctx context.Context, s *session.Session, objective string) error {
	if ctx == nil {
		ctx = b.ctx
	}
	tctx, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()

	if err := b.waitAbort(tctx); err != nil {
		return err
	}
	client := b.currentClient()
	if client == nil {
		return errors.New("codex app-server: disconnected")
	}
	if canEdit, _ := b.goalActionCapabilities(); !canEdit {
		return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: "this Codex version does not support thread goal RPCs"}
	}
	tid, err := b.resolveThreadID(tctx, client)
	if err != nil {
		return err
	}
	res, err := client.ThreadGoalSet(tctx, codex.ThreadGoalSetParams{
		ThreadID:  tid,
		Objective: &objective,
	})
	if err != nil {
		if isCodexGoalUnsupportedError(err) {
			b.markGoalUnsupported(s)
			return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: "this Codex version does not support thread goal RPCs"}
		}
		return err
	}
	if s != nil && res.Goal != nil {
		canEdit, canClear := b.goalActionCapabilities()
		if goal := codex.MapThreadGoal(res.Goal, tid, canEdit, canClear, time.Now()); goal != nil {
			s.ReportBridgeGoal(goal)
		}
	}
	return nil
}

// clearGoal clears the active Codex thread's goal via `thread/goal/clear`.
func (b *codexBridge) clearGoal(ctx context.Context, s *session.Session) error {
	if ctx == nil {
		ctx = b.ctx
	}
	tctx, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()

	if err := b.waitAbort(tctx); err != nil {
		return err
	}
	client := b.currentClient()
	if client == nil {
		return errors.New("codex app-server: disconnected")
	}
	if _, canClear := b.goalActionCapabilities(); !canClear {
		return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: "this Codex version does not support thread goal RPCs"}
	}
	tid, err := b.resolveThreadID(tctx, client)
	if err != nil {
		return err
	}
	res, err := client.ThreadGoalClear(tctx, tid)
	if err != nil {
		if isCodexGoalUnsupportedError(err) {
			b.markGoalUnsupported(s)
			return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: "this Codex version does not support thread goal RPCs"}
		}
		return err
	}
	if s != nil && (res.Cleared == nil || *res.Cleared) {
		if goal := codex.MapGoalCleared(codex.ThreadGoalClearedNotification{ThreadID: tid}, tid, time.Now()); goal != nil {
			s.ReportBridgeGoal(goal)
		}
	}
	return nil
}

// setFastMode toggles session/thread-scoped Codex Fast Mode via thread/resume
// when idle on a materialized thread, or defers the serviceTier override to the
// next turn/start when a turn is active, no thread is bound yet, or the thread
// has no rollout yet (-32600). Global Codex config is never modified.
func (b *codexBridge) setFastMode(ctx context.Context, s *session.Session, enabled bool) error {
	if ctx == nil {
		ctx = b.ctx
	}
	tctx, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()

	if err := b.waitAbort(tctx); err != nil {
		return err
	}

	b.mu.Lock()
	client := b.client
	epoch := b.epoch
	tid := b.tid
	turnActive := b.turnActive
	closed := b.closed
	b.mu.Unlock()
	if closed || client == nil {
		return errors.New("codex app-server: disconnected")
	}

	tier := codex.ServiceTierForFastMode(enabled)
	dirty := true
	if tid != "" && !turnActive {
		_, err := resumeCodexThread(tctx, client, codex.ThreadResumeParams{
			ThreadID:     tid,
			ExcludeTurns: true,
			ServiceTier:  tier,
		})
		switch {
		case err == nil:
			dirty = false
		case codex.IsInvalidRequest(err):
			// 0-turn unmaterialized rollout (-32600): defer to the next turn/start.
			dirty = true
		default:
			return err
		}
	}

	b.mu.Lock()
	if b.closed || b.epoch != epoch {
		b.mu.Unlock()
		return errors.New("codex app-server: disconnected")
	}
	if !dirty && b.tid != tid {
		// Active thread switched while thread/resume was in flight: keep dirty
		// so the tier is still applied to the new thread on its next turn.
		dirty = true
	}
	b.fastMode = enabled
	b.serviceTierOverride = tier
	b.serviceTierDirty = dirty
	b.mu.Unlock()

	if s != nil {
		s.ReportFastMode(enabled)
		s.Report(&protocol.Usage{FastMode: proto.Bool(enabled)})
	}
	return nil
}

// deliverMessage injects text into the active Codex thread via `thread/queue/add`
// (which immediately starts a turn if idle and auto-drains FIFO after the current
// turn if busy), falling back to `turn/start` on `-32600` (e.g. ephemeral thread
// or server without queue support). When a deferred serviceTier override is
// pending and idle, `turn/start` is attempted first so the tier override takes
// effect for this and subsequent turns.
func (b *codexBridge) deliverMessage(ctx context.Context, text string) error {
	if ctx == nil {
		ctx = b.ctx
	}
	tctx, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()

	// Serialize behind any in-flight abort/restart so a stale interrupt cannot
	// race past and abort the newly delivered turn.
	if err := b.waitAbort(tctx); err != nil {
		return err
	}

	client := b.currentClient()
	if client == nil {
		return errors.New("codex app-server: disconnected")
	}

	tid, err := b.resolveThreadID(tctx, client)
	if err != nil {
		return err
	}

	b.mu.Lock()
	dirty := b.serviceTierDirty
	tier := b.serviceTierOverride
	turnActive := b.turnActive
	b.mu.Unlock()

	input := []codex.UserInput{{Type: "text", Text: text}}
	triedTurnStart := false
	if dirty && tier != "" && !turnActive {
		triedTurnStart = true
		res, startErr := client.TurnStart(tctx, codex.TurnStartParams{
			ThreadID:    tid,
			Input:       input,
			ServiceTier: tier,
		})
		if startErr == nil {
			b.mu.Lock()
			if b.serviceTierOverride == tier {
				b.serviceTierDirty = false
			}
			if res.Turn.ID != "" {
				b.noteTurnStartedLocked(res.Turn.ID)
			}
			b.mu.Unlock()
			return nil
		}
		if !codex.IsInvalidRequest(startErr) && !isCodexTurnBusyError(startErr) {
			return startErr
		}
		// Busy or -32600: fall back to thread/queue/add keeping serviceTierDirty = true.
	}

	_, err = client.ThreadQueueAdd(tctx, codex.ThreadQueueAddParams{
		ThreadID:            tid,
		ClientUserMessageID: conversationUUID(),
		Input:               input,
	})
	if err == nil {
		return nil
	}
	if codex.IsInvalidRequest(err) && !triedTurnStart {
		params := codex.TurnStartParams{
			ThreadID: tid,
			Input:    input,
		}
		if dirty && tier != "" {
			params.ServiceTier = tier
		}
		res, startErr := client.TurnStart(tctx, params)
		if startErr == nil {
			b.mu.Lock()
			if dirty && b.serviceTierOverride == tier {
				b.serviceTierDirty = false
			}
			if res.Turn.ID != "" {
				b.noteTurnStartedLocked(res.Turn.ID)
			}
			b.mu.Unlock()
		}
		return startErr
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

func (d *Daemon) startCodexAppServerProcess(ctx context.Context, cfg codexServerLaunchConfig) (*codexStartedServer, error) {
	// Serialize per-CODEX_HOME startup so concurrent launches do not collide on
	// Codex's shared SQLite state database, and retry up to 3 times on SQLite
	// runtime init failure.
	queueCtx, queueCancel := context.WithTimeout(ctx, codexStartupQueueTimeout)
	releaseLane, err := d.codexStartup.acquire(queueCtx, cfg.stateDirKey)
	queueCancel()
	if err != nil {
		return nil, err
	}
	defer releaseLane()

	var (
		tr         *codexAppServerTransport
		cmd        *exec.Cmd
		exited     chan struct{}
		client     *codex.AppServerClient
		remoteAddr string
		initRes    codex.InitializeResult
	)
	for attempt := 1; attempt <= codexStartupMaxAttempts; attempt++ {
		var attemptErr error
		tr, attemptErr = newCodexAppServerTransport(cfg.runDir, cfg.sessionID)
		if attemptErr != nil {
			return nil, attemptErr
		}

		serverArgs := make([]string, 0, 3+len(tr.extraServerArgs)+len(cfg.split.ServerArgs))
		serverArgs = append(serverArgs, "app-server", "--listen", tr.listenArg)
		serverArgs = append(serverArgs, tr.extraServerArgs...)
		serverArgs = append(serverArgs, cfg.split.ServerArgs...)

		cmd = exec.Command(cfg.split.Program, serverArgs...)
		cmd.Dir = cfg.project
		cmd.Stdout = io.Discard
		stderrPipe, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			tr.cleanupFiles()
			return nil, pipeErr
		}
		cmd.Env = mergeEnvSlice(os.Environ(), cfg.serverEnv)
		configureCodexServerCmd(cmd)

		if startErr := safeexec.Start(cmd); startErr != nil {
			tr.cleanupFiles()
			return nil, startErr
		}
		exited = make(chan struct{})
		go func(c *exec.Cmd, done chan struct{}) {
			_ = c.Wait()
			close(done)
		}(cmd, exited)

		tail := &stderrTail{}
		bootCtx, bootCancel := context.WithTimeout(ctx, codexServerBootTimeout)
		client, remoteAddr, attemptErr = tr.waitReady(bootCtx, exited, stderrPipe, tail)
		if attemptErr == nil {
			initRes, attemptErr = client.Initialize(bootCtx, codex.NonOriginatingClientName, codex.NonOriginatingClientVersion, true)
		}
		bootCancel()
		if attemptErr == nil {
			return &codexStartedServer{
				tr:         tr,
				cmd:        cmd,
				exited:     exited,
				client:     client,
				remoteAddr: remoteAddr,
				initRes:    initRes,
			}, nil
		}

		if client != nil {
			_ = client.Close()
			client = nil
		}
		stopCodexServerCmd(cmd, exited)
		tr.cleanupFiles()

		stderrText := tail.String()
		if attempt < codexStartupMaxAttempts && ctx.Err() == nil && isCodexSQLiteStartupError(stderrText) {
			d.log.Warn("codex app-server: SQLite state startup failed; retrying with fresh process",
				"session", cfg.sessionID, "attempt", attempt, "max_attempts", codexStartupMaxAttempts, "err", attemptErr)
			continue
		}
		return nil, attemptErr
	}
	return nil, errors.New("codex app-server: startup attempts exhausted")
}

// buildCodexRemoteTUICommand constructs the `codex --remote` command line and
// transport environment overrides for either a resumed thread or a fresh session.
// Place `--` before positional args when a prompt is present so prompts starting
// with `-` (e.g. `-fix-me`) are never misparsed as CLI flags.
func buildCodexRemoteTUICommand(
	split codex.RemoteLaunchSplit,
	tr *codexAppServerTransport,
	remoteAddr, threadID string,
	includeInitialPrompt bool,
) ([]string, map[string]string) {
	var extraArgs []string
	var extraEnv map[string]string
	if tr != nil {
		extraArgs = tr.extraTUIArgs
		extraEnv = tr.extraTUIEnv
	}
	tuiCmd := make([]string, 0, 6+len(extraArgs)+len(split.TUIArgs)+len(split.NewTUIArgs))
	tuiCmd = append(tuiCmd, split.Program, "--remote", remoteAddr)
	tuiCmd = append(tuiCmd, extraArgs...)
	if threadID != "" {
		tuiCmd = append(tuiCmd, "resume")
		tuiCmd = append(tuiCmd, split.TUIArgs...)
		if includeInitialPrompt && split.HasPrompt {
			tuiCmd = append(tuiCmd, "--", threadID, split.Prompt)
		} else {
			tuiCmd = append(tuiCmd, threadID)
		}
	} else {
		tuiCmd = append(tuiCmd, split.TUIArgs...)
		tuiCmd = append(tuiCmd, split.NewTUIArgs...)
		if includeInitialPrompt && split.HasPrompt {
			tuiCmd = append(tuiCmd, "--", split.Prompt)
		}
	}
	return tuiCmd, extraEnv
}

func (d *Daemon) restartManagedCodexServer(ctx context.Context, cfg codexServerLaunchConfig, prevThreadID, serviceTier string) (*codexRestartOutcome, error) {
	started, err := d.startCodexAppServerProcess(ctx, cfg)
	if err != nil {
		return nil, err
	}

	bootCtx, bootCancel := context.WithTimeout(ctx, codexServerBootTimeout)
	defer bootCancel()

	initUsage := &protocol.Usage{}
	if rl, err := started.client.RateLimitsRead(bootCtx); err == nil {
		applyRateLimits(initUsage, rl.RateLimits)
	}
	var initFastMode bool
	if cfgRes, err := started.client.ConfigRead(bootCtx); err == nil {
		initFastMode = codex.IsFastServiceTier(cfgRes.Config.ServiceTier)
		if cfgRes.Config.ServiceTier != "" {
			initUsage.FastMode = proto.Bool(initFastMode)
		}
	}

	var (
		threadID      string
		threadPath    string
		resumedThread bool
		initGoal      *protocol.GoalStatus
	)
	if prevThreadID != "" {
		cwd := cmp.Or(cfg.split.Cwd, cfg.project)
		res, err := resumeCodexThread(bootCtx, started.client, codex.ThreadResumeParams{
			ThreadID:       prevThreadID,
			ExcludeTurns:   true,
			Cwd:            cwd,
			Model:          cfg.split.Model,
			ServiceTier:    serviceTier,
			ApprovalPolicy: cfg.split.ApprovalPolicy,
			Sandbox:        cfg.split.Sandbox,
		})
		if err != nil {
			d.log.Warn("codex app-server: failed to resume thread after force restart",
				"session", cfg.sessionID, "thread", prevThreadID, "err", err)
		} else {
			threadID = cmp.Or(res.Thread.ID, prevThreadID)
			threadPath = res.Thread.Path
			resumedThread = threadID != ""
			applyThreadMetadata(initUsage, res.Model, res.ReasoningEffort, res.ApprovalPolicy, res.Sandbox)
			if res.ServiceTier != "" {
				initFastMode = codex.IsFastServiceTier(res.ServiceTier)
				initUsage.FastMode = proto.Bool(initFastMode)
			} else if serviceTier != "" {
				initFastMode = codex.IsFastServiceTier(serviceTier)
				initUsage.FastMode = proto.Bool(initFastMode)
			}
			if res.Thread.Goal != nil && threadID != "" {
				initGoal = codex.MapThreadGoal(res.Thread.Goal, threadID, true, true, time.Now())
			}
		}
	}

	tuiCmd, tuiEnv := buildCodexRemoteTUICommand(cfg.split, started.tr, started.remoteAddr, threadID, false)
	if agent.IsLoopbackURL(started.remoteAddr) {
		tmp := agent.Launch{Env: map[string]string{}}
		if v, ok := cfg.serverEnv["NO_PROXY"]; ok {
			tmp.Env["NO_PROXY"] = v
		}
		if v, ok := cfg.serverEnv["no_proxy"]; ok {
			tmp.Env["no_proxy"] = v
		}
		maps.Copy(tmp.Env, tuiEnv)
		agent.EnsureLocalProxyBypass(&tmp)
		tuiEnv = tmp.Env
	}
	if initUsage.IsZero() {
		initUsage = nil
	}
	return &codexRestartOutcome{
		client:        started.client,
		cmd:           started.cmd,
		exited:        started.exited,
		cleanupFiles:  started.tr.cleanupFiles,
		userAgent:     started.initRes.UserAgent,
		initUsage:     initUsage,
		initGoal:      initGoal,
		initFastMode:  initFastMode,
		threadID:      threadID,
		threadPath:    threadPath,
		resumedThread: resumedThread,
		tuiCmd:        tuiCmd,
		tuiEnv:        tuiEnv,
	}, nil
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

	serverEnv := maps.Clone(launch.Env)
	if serverEnv == nil {
		serverEnv = map[string]string{}
	}
	// Hooks configured via -c run inside codex app-server; pass MAKO_SESSION so
	// `mako hook` routes events back to this session.
	serverEnv["MAKO_SESSION"] = sessionID

	cfg := codexServerLaunchConfig{
		runDir:      runDir,
		sessionID:   sessionID,
		project:     project,
		split:       split,
		serverEnv:   serverEnv,
		stateDirKey: codexStateDirKey(serverEnv),
	}

	started, err := d.startCodexAppServerProcess(ctx, cfg)
	if err != nil {
		d.log.Warn("codex app-server: startup failed; falling back to direct PTY", "session", sessionID, "err", err)
		return nil, noop
	}

	bridge := newCodexBridge(d.ctx, sessionID, started.client, started.cmd, started.exited, started.tr.cleanupFiles)
	bridge.launchCfg = &cfg
	bridge.serverUserAgent = started.initRes.UserAgent
	fail := func(msg string, err error) (*codexBridge, func()) {
		d.log.Warn("codex app-server: "+msg+"; falling back to direct PTY", "session", sessionID, "err", err)
		bridge.Close()
		return nil, noop
	}

	bootCtx, bootCancel := context.WithTimeout(ctx, codexServerBootTimeout)
	defer bootCancel()

	initUsage := &protocol.Usage{}
	if rl, err := started.client.RateLimitsRead(bootCtx); err == nil {
		applyRateLimits(initUsage, rl.RateLimits)
	}
	var initFastMode bool
	if cfgRes, err := started.client.ConfigRead(bootCtx); err == nil {
		initFastMode = codex.IsFastServiceTier(cfgRes.Config.ServiceTier)
		if cfgRes.Config.ServiceTier != "" {
			initUsage.FastMode = proto.Bool(initFastMode)
		}
	}

	cwd := cmp.Or(split.Cwd, project)
	targetID := cmp.Or(*conversationID, split.ResumeID)
	var threadID, threadPath string
	if targetID != "" {
		res, err := resumeCodexThread(bootCtx, started.client, codex.ThreadResumeParams{
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
		if res.ServiceTier != "" {
			initFastMode = codex.IsFastServiceTier(res.ServiceTier)
			initUsage.FastMode = proto.Bool(initFastMode)
		}

		bridge.mu.Lock()
		bridge.tid = threadID
		bridge.pendingPath = threadPath
		bridge.subscribed = true
		bridge.initFastMode = initFastMode
		bridge.fastMode = initFastMode
		if !initUsage.IsZero() {
			bridge.initUsage = initUsage
		}
		if res.Thread.Goal != nil {
			bridge.initGoal = codex.MapThreadGoal(res.Thread.Goal, threadID, true, true, time.Now())
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
		bridge.initFastMode = initFastMode
		bridge.fastMode = initFastMode
		if !initUsage.IsZero() {
			bridge.initUsage = initUsage
		}
		bridge.mu.Unlock()
	}

	tuiCmd, tuiEnv := buildCodexRemoteTUICommand(split, started.tr, started.remoteAddr, threadID, true)
	launch.Command = tuiCmd
	for k, v := range tuiEnv {
		launch.SetEnv(k, v)
	}
	if agent.IsLoopbackURL(started.remoteAddr) {
		agent.EnsureLocalProxyBypass(&launch.Launch)
	}

	d.log.Info("codex app-server ready", "session", sessionID, "thread", threadID, "remote", started.remoteAddr)
	return bridge, bridge.Close
}

func (d *Daemon) attachCodexBridge(s *session.Session, b *codexBridge) {
	if s == nil || b == nil {
		return
	}
	b.mu.Lock()
	b.ensureInitLocked()
	initUsage := b.initUsage
	initGoal := b.initGoal
	initFastMode := b.initFastMode || b.fastMode
	b.fastMode = initFastMode
	b.mu.Unlock()

	d.codexMu.Lock()
	if d.codexBridges == nil {
		d.codexBridges = map[string]*codexBridge{}
	}
	d.codexBridges[s.ID()] = b
	d.codexMu.Unlock()

	s.SetFastModeSupported(true)
	s.ReportFastMode(initFastMode)
	if initUsage != nil && !initUsage.IsZero() {
		s.Report(initUsage)
	}
	if initGoal != nil {
		s.ReportBridgeGoal(initGoal)
	}
	go d.runCodexBridge(s, b)
	if b.threadID() != "" {
		b.scheduleBackgroundRefresh(s)
	}
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

func (d *Daemon) abortViaCodexBridge(ctx context.Context, s *session.Session) bool {
	if s == nil {
		return false
	}
	b := d.codexBridge(s.ID())
	if b == nil {
		return false
	}
	res := d.abortCodexTurn(ctx, s, b, codexAbortOptions{})
	return res.HadActiveTurn
}

// abortCodexTurn requests graceful turn cancellation (`turn/interrupt`) and
// waits up to the configured grace period for the turn to settle. If the turn
// stays wedged past the grace period, it force-restarts the managed
// `codex app-server`, resumes the previous thread, and respawns the session's
// `--remote` PTY TUI cleanly. Concurrent abort calls are deduplicated.
func (d *Daemon) abortCodexTurn(ctx context.Context, s *session.Session, b *codexBridge, opts codexAbortOptions) codexAbortResult {
	if b == nil {
		return codexAbortResult{}
	}
	if ctx == nil {
		ctx = b.ctx
	}
	grace := opts.GracePeriod
	if grace <= 0 {
		grace = codexAbortGracePeriod
	}
	forceRestart := true
	if opts.ForceRestartOnTimeout != nil {
		forceRestart = *opts.ForceRestartOnTimeout
	}

	b.mu.Lock()
	b.ensureInitLocked()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return codexAbortResult{}
	}
	if inFlight := b.aborting; inFlight != nil {
		b.mu.Unlock()
		select {
		case <-inFlight.done:
			return inFlight.result
		case <-ctx.Done():
			return codexAbortResult{HadActiveTurn: true, Err: ctx.Err()}
		case <-b.ctx.Done():
			return codexAbortResult{HadActiveTurn: true, Err: b.ctx.Err()}
		}
	}
	hadTurn := b.turnActive || b.activeTurnID != ""
	if !hadTurn && s != nil {
		info := s.Info()
		st := info.GetStatus()
		if protocol.AtWork(st) || (st == protocol.AgentWaiting && info.GetApproval() != nil) {
			hadTurn = true
			b.noteTurnStartedLocked("")
		}
	}
	if !hadTurn {
		b.mu.Unlock()
		return codexAbortResult{HadActiveTurn: false}
	}
	inFlight := &inFlightAbort{done: make(chan struct{})}
	b.aborting = inFlight
	b.mu.Unlock()

	// Execute on a goroutine tied to b.ctx so an RPC caller cancelling mid-abort
	// cannot leave a force-restart halfway completed.
	go func() {
		res := d.executeCodexAbort(s, b, grace, forceRestart)
		b.mu.Lock()
		inFlight.result = res
		if b.aborting == inFlight {
			b.aborting = nil
		}
		close(inFlight.done)
		b.mu.Unlock()
	}()

	select {
	case <-inFlight.done:
		return inFlight.result
	case <-ctx.Done():
		return codexAbortResult{HadActiveTurn: true, Err: ctx.Err()}
	case <-b.ctx.Done():
		return codexAbortResult{HadActiveTurn: true, Err: b.ctx.Err()}
	}
}

func (d *Daemon) executeCodexAbort(s *session.Session, b *codexBridge, grace time.Duration, forceRestart bool) codexAbortResult {
	// Resolve any pending PermissionRequest approvals first so a blocked tool
	// prompt does not wedge turn cancellation.
	if s != nil {
		d.approvals.abortSession(s.ID())
	}

	b.mu.Lock()
	client := b.client
	tid := b.tid
	turnID := b.activeTurnID
	b.mu.Unlock()

	if tid == "" && s != nil {
		tid = s.Info().GetConversationId()
	}

	lookupTimeout := min(grace/2, codexTurnLookupTimeout)
	if lookupTimeout <= 0 {
		lookupTimeout = 50 * time.Millisecond
	}
	if tid == "" && client != nil {
		lctx, lcancel := context.WithTimeout(b.ctx, lookupTimeout)
		if res, err := client.ThreadLoadedList(lctx); err == nil && len(res.Data) == 1 && res.Data[0] != "" {
			tid = res.Data[0]
			b.mu.Lock()
			if b.tid == "" && !b.ignored[tid] {
				b.tid = tid
			}
			b.mu.Unlock()
		}
		lcancel()
	}
	if turnID == "" && tid != "" && client != nil {
		lctx, lcancel := context.WithTimeout(b.ctx, lookupTimeout)
		if res, err := client.ThreadRead(lctx, tid, true); err == nil {
			for i := len(res.Thread.Turns) - 1; i >= 0; i-- {
				t := res.Thread.Turns[i]
				if t.ID != "" && (t.Status == "" || t.Status == "inProgress") {
					turnID = t.ID
					b.mu.Lock()
					if b.turnActive && b.activeTurnID == "" {
						b.activeTurnID = turnID
					}
					b.mu.Unlock()
					break
				}
			}
		}
		lcancel()
	}

	// Best-effort turn/interrupt first without blocking the grace timer on the
	// RPC itself: a wedged tool/subagent/MCP call can prevent the app-server
	// from responding at all, and the restart fallback handles that case.
	// StartTurnInterrupt registers the pending interrupt synchronously so a
	// subsequent TurnStart/ThreadQueueAdd cannot slip ahead of it on fast settles.
	if client != nil && tid != "" && turnID != "" {
		intCtx, intCancel := context.WithTimeout(b.ctx, max(grace, time.Millisecond))
		intDone := client.StartTurnInterrupt(intCtx, codex.TurnInterruptParams{
			ThreadID: tid,
			TurnID:   turnID,
		})
		go func() {
			defer intCancel()
			if err := <-intDone; err != nil && !errors.Is(err, context.Canceled) {
				d.log.Debug("codex app-server: turn/interrupt error", "session", b.sessionID, "err", err)
			}
		}()
	} else if s != nil {
		// No server turn ID was discovered (e.g. brand-new unsubscribed turn);
		// nudge the --remote PTY TUI with Escape before waiting out the grace window.
		_, _ = s.Write([]byte("\x1b"))
	}

	if b.waitTurnSettled(s, grace) {
		return codexAbortResult{HadActiveTurn: true, Aborted: true}
	}
	if !forceRestart {
		return codexAbortResult{HadActiveTurn: true, Aborted: false}
	}

	d.log.Warn("codex app-server: interrupt did not settle turn within grace period; force-restarting backend",
		"session", b.sessionID, "grace", grace)
	resumed, err := d.forceRestartCodexBridge(s, b)
	return codexAbortResult{
		HadActiveTurn: true,
		Aborted:       true,
		ForcedRestart: true,
		ResumedThread: resumed,
		Err:           err,
	}
}

func (b *codexBridge) waitTurnSettled(s *session.Session, timeout time.Duration) bool {
	deadline := time.NewTimer(max(timeout, time.Millisecond))
	defer deadline.Stop()
	for {
		b.mu.Lock()
		active := b.turnActive
		done := b.turnDone
		noServerTurnID := b.activeTurnID == ""
		b.mu.Unlock()
		if !active || done == nil {
			return true
		}
		var sessChanged <-chan struct{}
		if s != nil {
			sessChanged = s.Changed()
			info := s.Info()
			if info.GetState() == protocol.StateExited {
				return true
			}
			if noServerTurnID && (info.GetStatus() == protocol.AgentIdle || info.GetStatus() == protocol.AgentDone) {
				b.mu.Lock()
				if b.activeTurnID == "" {
					b.settleTurnLocked()
				}
				b.mu.Unlock()
				return true
			}
		}
		select {
		case <-done:
			return true
		case <-sessChanged:
			continue
		case <-b.ctx.Done():
			return true
		case <-deadline.C:
			return false
		}
	}
}

func (d *Daemon) forceRestartCodexBridge(s *session.Session, b *codexBridge) (bool, error) {
	b.mu.Lock()
	b.ensureInitLocked()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return false, context.Canceled
	}
	prevThreadID := b.tid
	if prevThreadID == "" && s != nil {
		prevThreadID = s.Info().GetConversationId()
	}
	if b.bgTimer != nil {
		b.bgTimer.Stop()
		b.bgTimer = nil
	}
	// Advance epoch and mark restarting BEFORE tearing down the old generation
	// so runCodexBridge stays parked waiting for the new client and stale exits
	// or late notifications from the previous generation are ignored.
	b.epoch++
	targetEpoch := b.epoch
	b.restarting = true
	b.settleTurnLocked()
	b.resetTurnLocked("")
	b.completedTurns = map[string]bool{}
	b.bgStarted = map[string]int64{}
	b.bgHasTasks = false
	oldClient := b.client
	oldCmd := b.cmd
	oldExited := b.exited
	oldCleanup := b.cleanupFiles
	b.client = nil
	b.cmd = nil
	b.cleanupFiles = nil
	b.signalSwapLocked()
	restartFn := b.restartServer
	launchCfg := b.launchCfg
	desiredServiceTier := b.serviceTierOverride
	if desiredServiceTier == "" && b.fastMode {
		desiredServiceTier = codex.ServiceTierForFastMode(true)
	}
	b.mu.Unlock()

	if s != nil {
		d.live.clear(s.ID())
		s.ReportBridgeTasks(nil)
		s.SetStatus(protocol.AgentIdle)
	}

	var teardownOnce sync.Once
	teardownOld := func() {
		teardownOnce.Do(func() {
			if oldClient != nil {
				_ = oldClient.Close()
			}
			stopCodexServerCmd(oldCmd, oldExited)
			if oldCleanup != nil {
				oldCleanup()
			}
		})
	}

	var outcome *codexRestartOutcome
	prepareRestart := func(rctx context.Context) ([]string, map[string]string, error) {
		ctx, cancel := context.WithCancel(b.ctx)
		defer cancel()
		if rctx != nil {
			stop := context.AfterFunc(rctx, cancel)
			defer stop()
		}

		teardownOld()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		var err error
		switch {
		case restartFn != nil:
			outcome, err = restartFn(ctx, prevThreadID)
		case launchCfg != nil:
			outcome, err = d.restartManagedCodexServer(ctx, *launchCfg, prevThreadID, desiredServiceTier)
		default:
			err = errors.New("codex app-server: restart unavailable")
		}
		if err != nil {
			return nil, nil, err
		}
		if outcome == nil || outcome.client == nil {
			return nil, nil, errors.New("codex app-server: restart returned nil client")
		}

		b.mu.Lock()
		if b.closed || b.ctx.Err() != nil {
			b.mu.Unlock()
			_ = outcome.client.Close()
			stopCodexServerCmd(outcome.cmd, outcome.exited)
			if outcome.cleanupFiles != nil {
				outcome.cleanupFiles()
			}
			return nil, nil, context.Canceled
		}
		b.client = outcome.client
		b.cmd = outcome.cmd
		b.exited = outcome.exited
		b.cleanupFiles = outcome.cleanupFiles
		if outcome.userAgent != "" {
			b.serverUserAgent = outcome.userAgent
		}
		b.restarting = false
		b.ignored = map[string]bool{}
		b.abandoned = map[string]bool{}
		b.fastMode = outcome.initFastMode
		b.initFastMode = outcome.initFastMode
		if outcome.resumedThread && outcome.threadID != "" {
			b.serviceTierOverride = codex.ServiceTierForFastMode(outcome.initFastMode)
		} else {
			b.serviceTierOverride = ""
		}
		b.serviceTierDirty = false
		if outcome.resumedThread && outcome.threadID != "" {
			b.tid = outcome.threadID
			b.subscribed = true
			if outcome.threadPath != "" {
				b.pendingPath = outcome.threadPath
			}
			b.bound = s != nil && s.TranscriptPath() != "" && s.Info().GetConversationId() == outcome.threadID
		} else {
			b.tid = ""
			b.pendingPath = ""
			b.subscribed = false
			b.bound = false
		}
		b.signalSwapLocked()
		gen := b.epoch
		exited := outcome.exited
		b.mu.Unlock()

		if exited != nil {
			b.watchProcessExit(exited, gen)
		}
		if s != nil {
			s.ReportFastMode(outcome.initFastMode)
			if outcome.initUsage != nil && !outcome.initUsage.IsZero() {
				s.Report(outcome.initUsage)
			}
			if outcome.resumedThread && outcome.threadID != "" {
				if outcome.initGoal != nil {
					goal := proto.CloneOf(outcome.initGoal)
					if goal.Active() {
						canEdit, canClear := b.goalActionCapabilities()
						goal.CanEdit = canEdit
						goal.CanClear = canClear
					}
					s.ReportBridgeGoal(goal)
				}
			} else {
				s.ClearGoal()
			}
		}
		return outcome.tuiCmd, outcome.tuiEnv, nil
	}

	var restartErr error
	if s != nil && s.Info().GetState() == protocol.StateRunning {
		restartErr = s.Restart(prepareRestart)
	} else {
		_, _, restartErr = prepareRestart(b.ctx)
	}
	if restartErr != nil {
		teardownOld()
		b.mu.Lock()
		if b.epoch == targetEpoch && b.restarting {
			b.restarting = false
			b.signalSwapLocked()
		}
		b.mu.Unlock()
	}

	resumed := outcome != nil && outcome.resumedThread
	if s != nil {
		s.SetStatus(protocol.AgentIdle)
		b.advanceSeq(s.StatusWatermark())
		switch {
		case restartErr != nil:
			s.PostNotice("Codex task force-stopped",
				"Force-stopped active task after interrupt timeout, but restarting Codex failed: "+restartErr.Error())
		case resumed:
			s.PostNotice("Codex task force-stopped",
				"Force-stopped active task after interrupt timeout. Codex backend was restarted and the previous thread was resumed.")
		default:
			s.PostNotice("Codex task force-stopped",
				"Force-stopped active task after interrupt timeout. Codex backend was restarted, but the previous thread could not be resumed.")
		}
		if restartErr == nil && resumed && b.threadID() != "" {
			d.maybeBindCodexThread(s, b, targetEpoch)
			b.scheduleBackgroundRefresh(s)
		}
	}
	return resumed, restartErr
}

func (d *Daemon) runCodexBridge(s *session.Session, b *codexBridge) {
	defer d.removeCodexBridge(s.ID(), b)
	var daemonDone <-chan struct{}
	if d.ctx != nil {
		daemonDone = d.ctx.Done()
	}
	for {
		client, epoch, swapped, restarting := b.activeStream()
		if client == nil {
			if !restarting {
				return
			}
			select {
			case <-swapped:
				continue
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
		notifs := client.Notifications()
		for {
			select {
			case n, ok := <-notifs:
				if !ok {
					if b.shouldContinueAfterEOF(epoch) {
						goto nextClient
					}
					return
				}
				if !b.isCurrentEpoch(epoch) {
					goto nextClient
				}
				d.handleCodexNotification(s, b, n, epoch)
			case <-swapped:
				goto nextClient
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
	nextClient:
	}
}

func (d *Daemon) handleCodexNotification(s *session.Session, b *codexBridge, n codex.Notification, epoch uint64) {
	if !b.isCurrentEpoch(epoch) {
		return
	}
	defer d.observeCodexAuthNotification(s, b, n, epoch)
	switch n.Method {
	case "thread/started":
		var p codex.ThreadStartedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.Thread.ID == "" {
			return
		}
		if p.Thread.Ephemeral || p.Thread.IsSubagent() {
			b.mu.Lock()
			if b.epoch == epoch {
				b.ignored[p.Thread.ID] = true
			}
			b.mu.Unlock()
			return
		}
		b.mu.Lock()
		if b.epoch != epoch {
			b.mu.Unlock()
			return
		}
		if p.Thread.ID == b.tid {
			if p.Thread.Path != "" && b.pendingPath == "" {
				b.pendingPath = p.Thread.Path
			}
			b.mu.Unlock()
			d.maybeBindCodexThread(s, b, epoch)
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
		b.settleTurnLocked()
		b.fgItems = map[string]bool{}
		b.fgProcesses = map[string]bool{}
		b.mu.Unlock()
		if b.isCurrentEpoch(epoch) {
			s.ClearGoal()
			if p.Thread.Goal != nil {
				canEdit, canClear := b.goalActionCapabilities()
				if goal := codex.MapThreadGoal(p.Thread.Goal, p.Thread.ID, canEdit, canClear, time.Now()); goal != nil {
					s.ReportBridgeGoal(goal)
				}
			}
		}
		d.maybeBindCodexThread(s, b, epoch)
		b.scheduleBackgroundRefresh(s)

	case "thread/status/changed":
		var p codex.ThreadStatusChangedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID == "" {
			return
		}
		b.mu.Lock()
		if b.epoch != epoch {
			b.mu.Unlock()
			return
		}
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
			if !d.maybeSwitchCodexThread(s, b, p.ThreadID, p.Status.Type, epoch) {
				return
			}
			curTID = p.ThreadID
			b.mu.Lock()
			subscribed = b.subscribed
			b.mu.Unlock()
		}

		if p.ThreadID == curTID && (p.Status.Type == "active" || p.Status.Type == "idle") {
			if !subscribed {
				d.trySubscribeCodexThread(s, b, p.ThreadID, epoch)
			}
			d.maybeBindCodexThread(s, b, epoch)
		}
		if !b.isCurrentEpoch(epoch) || p.ThreadID != b.threadID() {
			return
		}

		seq := b.seqFor(n.EmittedAtMs)
		switch p.Status.Type {
		case "active":
			b.mu.Lock()
			if b.epoch == epoch && !b.turnActive {
				b.noteTurnStartedLocked("")
			}
			b.mu.Unlock()
			if slices.Contains(p.Status.ActiveFlags, "waitingOnApproval") || slices.Contains(p.Status.ActiveFlags, "waitingOnUserInput") {
				s.ReportToolStatus(protocol.AgentWaiting, seq)
			} else {
				// Use ReportToolStatus so mid-turn active updates never withdraw a
				// live PermissionRequest approval dialog.
				s.ReportToolStatus(protocol.AgentWorking, seq)
			}
		case "idle":
			b.mu.Lock()
			aborting := b.aborting != nil
			wasActive := b.turnActive
			step := cmp.Or(b.activeTurn, "turn")
			if b.epoch == epoch {
				b.settleTurnLocked()
			}
			b.mu.Unlock()
			if aborting && wasActive {
				d.live.take(livePost{Session: s.ID(), Step: step, End: true})
				s.ReportStatus(protocol.AgentIdle, seq)
			} else if s.Info().GetStatus() == protocol.AgentWaiting {
				// Clear AgentWaiting once approval/input resolves without clobbering AgentDone.
				s.ReportStatus(protocol.AgentIdle, seq)
			}
		case "systemError":
			b.mu.Lock()
			wasActive := b.turnActive
			step := cmp.Or(b.activeTurn, "turn")
			if b.epoch == epoch {
				b.settleTurnLocked()
			}
			b.mu.Unlock()
			if wasActive {
				d.live.take(livePost{Session: s.ID(), Step: step, End: true})
			}
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
		if b.epoch != epoch {
			b.mu.Unlock()
			return
		}
		b.resetTurnLocked(step)
		b.noteTurnStartedLocked(p.Turn.ID)
		b.mu.Unlock()
		d.maybeBindCodexThread(s, b, epoch)
		s.ReportStatus(protocol.AgentWorking, b.seqFor(n.EmittedAtMs))
		d.live.take(livePost{Session: s.ID(), Step: step})

	case "item/started":
		var p codex.ItemNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() || p.Item.ID == "" {
			return
		}
		b.mu.Lock()
		if b.epoch != epoch {
			b.mu.Unlock()
			return
		}
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
			b.fgItems[p.Item.ID] = true
			if p.Item.ProcessID != "" {
				b.fgProcesses[p.Item.ProcessID] = true
			}
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
		if b.epoch != epoch {
			b.mu.Unlock()
			return
		}
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
		if b.epoch != epoch || b.thinkingSeen[itemID] {
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

	case "item/completed":
		var p codex.ItemNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		if p.Item.Type == "commandExecution" {
			b.mu.Lock()
			if b.epoch == epoch {
				delete(b.fgItems, p.Item.ID)
				if p.Item.ProcessID != "" {
					delete(b.fgProcesses, p.Item.ProcessID)
				}
			}
			b.mu.Unlock()
			b.scheduleBackgroundRefresh(s)
		}

	case "turn/completed":
		var p codex.TurnNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		d.completeCodexTurn(s, b, epoch, p.Turn.ID, p.Turn.Status == "completed", n.EmittedAtMs)

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

	case "thread/settings/updated":
		var p codex.ThreadSettingsUpdatedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID == "" {
			return
		}
		hasServiceTier := p.ThreadSettings.ServiceTier != nil || threadSettingsHasServiceTier(n.Params)
		var fast bool
		if p.ThreadSettings.ServiceTier != nil {
			fast = codex.IsFastServiceTier(*p.ThreadSettings.ServiceTier)
		}
		b.mu.Lock()
		sameEpoch := b.epoch == epoch
		curTID := b.tid
		if sameEpoch && curTID != "" && p.ThreadID == curTID && hasServiceTier {
			b.fastMode = fast
			b.serviceTierOverride = codex.ServiceTierForFastMode(fast)
			b.serviceTierDirty = false
		}
		b.mu.Unlock()
		if !sameEpoch || curTID == "" || p.ThreadID != curTID {
			return
		}
		if hasServiceTier && b.isCurrentEpoch(epoch) {
			s.ReportFastMode(fast)
		}
		u := &protocol.Usage{}
		applyThreadMetadata(u, p.ThreadSettings.Model, p.ThreadSettings.Effort, p.ThreadSettings.ApprovalPolicy, p.ThreadSettings.SandboxPolicy)
		if hasServiceTier {
			u.FastMode = proto.Bool(fast)
		}
		if !u.IsZero() && b.isCurrentEpoch(epoch) {
			s.Report(u)
		}

	case "thread/goal/updated":
		var p codex.ThreadGoalUpdatedNotification
		if json.Unmarshal(n.Params, &p) != nil {
			return
		}
		b.mu.Lock()
		sameEpoch := b.epoch == epoch
		curTID := b.tid
		b.mu.Unlock()
		if !sameEpoch || curTID == "" {
			return
		}
		if tid := p.ResolvedThreadID(); tid != "" && tid != curTID {
			return
		}
		observed := time.Now()
		if n.EmittedAtMs > 0 {
			observed = time.UnixMilli(n.EmittedAtMs)
		}
		canEdit, canClear := b.goalActionCapabilities()
		if goal := codex.MapGoalUpdated(p, curTID, canEdit, canClear, observed); goal != nil && b.isCurrentEpoch(epoch) {
			s.ReportBridgeGoal(goal)
		}

	case "thread/goal/cleared":
		var p codex.ThreadGoalClearedNotification
		if json.Unmarshal(n.Params, &p) != nil {
			return
		}
		b.mu.Lock()
		sameEpoch := b.epoch == epoch
		curTID := b.tid
		b.mu.Unlock()
		if !sameEpoch || curTID == "" {
			return
		}
		if tid := p.ResolvedThreadID(); tid != "" && tid != curTID {
			return
		}
		observed := time.Now()
		if n.EmittedAtMs > 0 {
			observed = time.UnixMilli(n.EmittedAtMs)
		}
		if goal := codex.MapGoalCleared(p, curTID, observed); goal != nil && b.isCurrentEpoch(epoch) {
			s.ReportBridgeGoal(goal)
		}

	default:
		if n.Method == "codex/event" || strings.HasPrefix(n.Method, "codex/event/") {
			var p struct {
				ThreadID       string `json:"threadId"`
				ThreadIDSnake  string `json:"thread_id"`
				ConversationID string `json:"conversationId"`
				ConvIDSnake    string `json:"conversation_id"`
				Msg            struct {
					Type           string `json:"type"`
					TurnID         string `json:"turn_id"`
					TurnIDV2       string `json:"turnId"`
					ThreadID       string `json:"thread_id"`
					ThreadIDV2     string `json:"threadId"`
					ConversationID string `json:"conversation_id"`
					ConvIDV2       string `json:"conversationId"`
				} `json:"msg"`
			}
			if json.Unmarshal(n.Params, &p) != nil || p.Msg.Type == "" {
				return
			}
			evTID := cmp.Or(
				p.ThreadID, p.ThreadIDSnake, p.ConversationID, p.ConvIDSnake,
				p.Msg.ThreadID, p.Msg.ThreadIDV2, p.Msg.ConversationID, p.Msg.ConvIDV2,
			)
			if evTID != "" && evTID != b.threadID() {
				return
			}
			turnID := cmp.Or(p.Msg.TurnID, p.Msg.TurnIDV2)
			switch p.Msg.Type {
			case "task_started":
				b.mu.Lock()
				if b.epoch == epoch {
					b.noteTurnStartedLocked(turnID)
				}
				b.mu.Unlock()
			case "task_complete":
				d.completeCodexTurn(s, b, epoch, turnID, true, n.EmittedAtMs)
			case "turn_aborted":
				d.completeCodexTurn(s, b, epoch, turnID, false, n.EmittedAtMs)
			}
		}
	}
}

func (d *Daemon) completeCodexTurn(s *session.Session, b *codexBridge, epoch uint64, turnID string, completed bool, emittedAtMs int64) {
	b.mu.Lock()
	if b.epoch != epoch {
		b.mu.Unlock()
		return
	}
	if b.isStaleTurnLocked(turnID) {
		b.mu.Unlock()
		return
	}
	if !b.markTurnCompletedLocked(turnID) {
		b.settleTurnLocked()
		b.mu.Unlock()
		return
	}
	step := cmp.Or(turnID, b.activeTurn, "turn")
	b.settleTurnLocked()
	b.fgItems = map[string]bool{}
	b.fgProcesses = map[string]bool{}
	b.mu.Unlock()

	d.live.take(livePost{Session: s.ID(), Step: step, End: true})
	d.maybeBindCodexThread(s, b, epoch)
	b.scheduleBackgroundRefresh(s)
	seq := b.seqFor(emittedAtMs)
	if completed {
		d.refreshActivity(s)
		s.ReportStatus(protocol.AgentDone, seq)
	} else {
		s.ReportStatus(protocol.AgentIdle, seq)
	}
}

func (d *Daemon) maybeSwitchCodexThread(s *session.Session, b *codexBridge, threadID, statusType string, epoch uint64) bool {
	client := b.currentClient()
	if client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	res, err := client.ThreadRead(ctx, threadID, false)
	if err != nil {
		return false
	}
	if res.Thread.Ephemeral || res.Thread.IsSubagent() {
		b.mu.Lock()
		if b.epoch == epoch {
			b.ignored[threadID] = true
		}
		b.mu.Unlock()
		return false
	}
	var (
		reportFast  bool
		fastModeVal bool
	)
	b.mu.Lock()
	if b.epoch != epoch {
		b.mu.Unlock()
		return false
	}
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
	if res.ServiceTier != "" && !b.serviceTierDirty {
		fastModeVal = codex.IsFastServiceTier(res.ServiceTier)
		b.fastMode = fastModeVal
		b.serviceTierOverride = codex.ServiceTierForFastMode(fastModeVal)
		reportFast = true
	}
	b.settleTurnLocked()
	b.fgItems = map[string]bool{}
	b.fgProcesses = map[string]bool{}
	b.mu.Unlock()
	if b.isCurrentEpoch(epoch) {
		if reportFast && s != nil {
			s.ReportFastMode(fastModeVal)
		}
		s.ClearGoal()
		if res.Thread.Goal != nil {
			canEdit, canClear := b.goalActionCapabilities()
			if goal := codex.MapThreadGoal(res.Thread.Goal, threadID, canEdit, canClear, time.Now()); goal != nil {
				s.ReportBridgeGoal(goal)
			}
		}
	}
	b.scheduleBackgroundRefresh(s)
	return true
}

func (d *Daemon) trySubscribeCodexThread(s *session.Session, b *codexBridge, threadID string, epoch uint64) {
	client := b.currentClient()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	res, err := resumeCodexThread(ctx, client, codex.ThreadResumeParams{
		ThreadID:     threadID,
		ExcludeTurns: true,
	})
	if err != nil {
		return
	}
	var (
		reportFast  bool
		fastModeVal bool
	)
	b.mu.Lock()
	if b.epoch == epoch && b.tid == threadID {
		b.subscribed = true
		if res.Thread.Path != "" {
			b.pendingPath = res.Thread.Path
		}
		if res.ServiceTier != "" && !b.serviceTierDirty {
			fastModeVal = codex.IsFastServiceTier(res.ServiceTier)
			b.fastMode = fastModeVal
			b.serviceTierOverride = codex.ServiceTierForFastMode(fastModeVal)
			reportFast = true
		}
	}
	b.mu.Unlock()
	u := &protocol.Usage{}
	applyThreadMetadata(u, res.Model, res.ReasoningEffort, res.ApprovalPolicy, res.Sandbox)
	if reportFast {
		u.FastMode = proto.Bool(fastModeVal)
	}
	if !u.IsZero() && s != nil && b.isCurrentEpoch(epoch) {
		s.Report(u)
	}
	if reportFast && s != nil && b.isCurrentEpoch(epoch) {
		s.ReportFastMode(fastModeVal)
	}
	if res.Thread.Goal != nil && s != nil && b.isCurrentEpoch(epoch) && b.threadID() == threadID {
		canEdit, canClear := b.goalActionCapabilities()
		if goal := codex.MapThreadGoal(res.Thread.Goal, threadID, canEdit, canClear, time.Now()); goal != nil {
			s.ReportBridgeGoal(goal)
		}
	}
}

// maybeBindCodexThread binds the active thread's ConversationId and rollout path
// once its rollout file actually exists on disk and verifies.
func (d *Daemon) maybeBindCodexThread(s *session.Session, b *codexBridge, epoch uint64) {
	b.mu.Lock()
	if b.epoch != epoch {
		b.mu.Unlock()
		return
	}
	tid := b.tid
	path := b.pendingPath
	bound := b.bound
	client := b.client
	b.mu.Unlock()
	if tid == "" || bound {
		return
	}
	if path == "" && client != nil {
		ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
		res, err := client.ThreadRead(ctx, tid, false)
		cancel()
		if err == nil && res.Thread.Path != "" {
			path = res.Thread.Path
			b.mu.Lock()
			if b.epoch == epoch && b.tid == tid {
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
		if b.epoch == epoch && b.tid == tid {
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
	return agent.BuildChildEnv(base, overrides)
}
