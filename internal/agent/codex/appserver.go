package codex

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/safeexec"
)

// RPCError is a JSON-RPC 2.0 error returned by codex app-server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app-server rpc error %d: %s", e.Code, e.Message)
}

// IsInvalidRequest reports whether err is a JSON-RPC -32600 invalid request error
// (for example, when a thread does not support queued submissions or has no rollout yet).
func IsInvalidRequest(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == -32600
}

// IsMethodNotFound reports whether err is a JSON-RPC -32601 method not found error.
func IsMethodNotFound(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == -32601
}

// NonOriginatingClientName is Codex's reserved non-originating app-server
// client identity so managed sessions keep Codex's default CLI identity.
const (
	NonOriginatingClientName    = "codex_app_server_daemon"
	NonOriginatingClientTitle   = "Codex App Server Daemon"
	NonOriginatingClientVersion = "0.0.0"
)

// Notification is an unsolicited JSON-RPC notification from codex app-server.
type Notification struct {
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params,omitempty"`
	EmittedAtMs int64           `json:"emittedAtMs,omitempty"`
}

// InitializeParams negotiates client identity and optional experimental APIs.
type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
}

type InitializeResult struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// ThreadStatus describes the server-side execution state of a Codex thread.
type ThreadStatus struct {
	Type        string   `json:"type"`                  // notLoaded, idle, systemError, active
	ActiveFlags []string `json:"activeFlags,omitempty"` // waitingOnApproval, waitingOnUserInput
}

// ThreadTurnSummary is minimal turn metadata returned by thread/read(includeTurns=true).
type ThreadTurnSummary struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}

// ThreadInfo is thread metadata returned by thread/start, thread/resume, thread/read, and thread/started.
type ThreadInfo struct {
	ID              string              `json:"id"`
	SessionID       string              `json:"sessionId,omitempty"`
	ParentThreadID  string              `json:"parentThreadId,omitempty"`
	Source          json.RawMessage     `json:"source,omitempty"`
	Path            string              `json:"path,omitempty"`
	Cwd             string              `json:"cwd,omitempty"`
	Model           string              `json:"model,omitempty"`
	ReasoningEffort string              `json:"reasoningEffort,omitempty"`
	Ephemeral       bool                `json:"ephemeral,omitempty"`
	HistoryMode     string              `json:"historyMode,omitempty"` // "legacy" or "paginated"
	Turns           []ThreadTurnSummary `json:"turns,omitempty"`
	Status          ThreadStatus        `json:"status,omitempty"`
}

// IsSubagent reports whether this thread belongs to a subagent rather than
// the interactive parent conversation.
func (t ThreadInfo) IsSubagent() bool {
	return t.ParentThreadID != "" || strings.Contains(strings.ToLower(string(t.Source)), "subagent")
}

type ThreadStartParams struct {
	Cwd            string `json:"cwd,omitempty"`
	Model          string `json:"model,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	Ephemeral      *bool  `json:"ephemeral,omitempty"`
}

type ThreadStartResult struct {
	Thread          ThreadInfo      `json:"thread"`
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoningEffort,omitempty"`
	ApprovalPolicy  json.RawMessage `json:"approvalPolicy,omitempty"`
	Sandbox         json.RawMessage `json:"sandbox,omitempty"`
}

type ThreadResumeParams struct {
	ThreadID       string `json:"threadId"`
	ExcludeTurns   bool   `json:"excludeTurns,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Model          string `json:"model,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
}

type ThreadResumeResult struct {
	Thread          ThreadInfo      `json:"thread"`
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoningEffort,omitempty"`
	ApprovalPolicy  json.RawMessage `json:"approvalPolicy,omitempty"`
	Sandbox         json.RawMessage `json:"sandbox,omitempty"`
}

type ThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns"`
}

type ThreadReadResult struct {
	Thread ThreadInfo `json:"thread"`
}

type ThreadLoadedListResult struct {
	Data       []string `json:"data"`
	NextCursor *string  `json:"nextCursor,omitempty"`
}

// ThreadForkParams forks an existing Codex thread, optionally just before beforeTurnId.
type ThreadForkParams struct {
	ThreadID               string `json:"threadId"`
	BeforeTurnID           string `json:"beforeTurnId,omitempty"`
	Cwd                    string `json:"cwd,omitempty"`
	Model                  string `json:"model,omitempty"`
	ApprovalPolicy         string `json:"approvalPolicy,omitempty"`
	Sandbox                string `json:"sandbox,omitempty"`
	ExcludeTurns           bool   `json:"excludeTurns,omitempty"`
	PersistExtendedHistory bool   `json:"persistExtendedHistory,omitempty"`
}

type ThreadForkResult struct {
	Thread ThreadInfo `json:"thread"`
}

type ThreadRollbackParams struct {
	ThreadID string `json:"threadId"`
	NumTurns int    `json:"numTurns"`
}

type ThreadRollbackResult struct {
	Thread ThreadInfo `json:"thread"`
}

// BackgroundTerminal is a live background shell owned by a Codex thread.
type BackgroundTerminal struct {
	ItemID    string `json:"itemId,omitempty"`
	ProcessID string `json:"processId"`
	Command   string `json:"command"`
}

type BackgroundTerminalsListParams struct {
	ThreadID string `json:"threadId"`
	Cursor   string `json:"cursor,omitempty"`
}

type BackgroundTerminalsListResult struct {
	Data       []BackgroundTerminal `json:"data"`
	NextCursor *string              `json:"nextCursor,omitempty"`
}

// UserInput is one element of a user prompt submitted to Codex app-server.
type UserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ThreadQueueAddParams struct {
	ThreadID            string      `json:"threadId"`
	ClientUserMessageID string      `json:"clientUserMessageId"`
	Input               []UserInput `json:"input"`
}

type ThreadQueueAddResult struct {
	QueuedSubmission struct {
		ID                  string `json:"id"`
		ClientUserMessageID string `json:"clientUserMessageId"`
	} `json:"queuedSubmission"`
}

type TurnStartParams struct {
	ThreadID string      `json:"threadId"`
	Input    []UserInput `json:"input"`
}

type TurnInfo struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"` // inProgress, completed, interrupted, failed
}

type TurnStartResult struct {
	Turn TurnInfo `json:"turn"`
}

type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []UserInput `json:"input"`
}

type TurnSteerResult struct {
	TurnID string `json:"turnId"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type TurnInterruptResult struct{}

// RateLimitWindow is one metered quota window from account/rateLimits/*.
type RateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type RateLimitSnapshot struct {
	LimitID   string           `json:"limitId,omitempty"`
	Primary   *RateLimitWindow `json:"primary,omitempty"`
	Secondary *RateLimitWindow `json:"secondary,omitempty"`
	PlanType  string           `json:"planType,omitempty"`
}

type RateLimitsReadResult struct {
	RateLimits          RateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]RateLimitSnapshot `json:"rateLimitsByLimitId,omitempty"`
}

type TokenUsageBreakdown struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type ThreadTokenUsage struct {
	Total              TokenUsageBreakdown `json:"total"`
	Last               TokenUsageBreakdown `json:"last"`
	ModelContextWindow *int64              `json:"modelContextWindow,omitempty"`
}

// Notification payload structures.
type ThreadStartedNotification struct {
	Thread ThreadInfo `json:"thread"`
}

type ThreadStatusChangedNotification struct {
	ThreadID string       `json:"threadId"`
	Status   ThreadStatus `json:"status"`
}

type TurnNotification struct {
	ThreadID string   `json:"threadId"`
	Turn     TurnInfo `json:"turn"`
}

type ThreadItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ProcessID string `json:"processId,omitempty"`
	Status    string `json:"status,omitempty"`
	Text      string `json:"text,omitempty"`
	Command   string `json:"command,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Server    string `json:"server,omitempty"`
	Query     string `json:"query,omitempty"`
}

type ItemNotification struct {
	ThreadID string     `json:"threadId"`
	TurnID   string     `json:"turnId"`
	Item     ThreadItem `json:"item"`
}

type AgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ReasoningDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ThreadTokenUsageNotification struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}

type AccountRateLimitsUpdatedNotification struct {
	RateLimits RateLimitSnapshot `json:"rateLimits"`
}

// ModelListParams requests available models from codex app-server.
type ModelListParams struct {
	Cursor        *string `json:"cursor,omitempty"`
	IncludeHidden *bool   `json:"includeHidden,omitempty"`
	Limit         *int    `json:"limit,omitempty"`
}

type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description,omitempty"`
}

type ModelListEntry struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Hidden                    bool                    `json:"hidden"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort,omitempty"`
	IsDefault                 bool                    `json:"isDefault,omitempty"`
}

type ModelListResult struct {
	Data       []ModelListEntry `json:"data"`
	NextCursor *string          `json:"nextCursor,omitempty"`
}

// wireMessage handles all JSON-RPC 2.0 frames emitted by codex app-server.
type wireMessage struct {
	ID          json.RawMessage `json:"id,omitempty"`
	Method      string          `json:"method,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       *RPCError       `json:"error,omitempty"`
	EmittedAtMs int64           `json:"emittedAtMs,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// AppServerClient speaks JSON-RPC 2.0 over a codex app-server WebSocket connection
// (either Unix domain socket or localhost TCP WebSocket).
type AppServerClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	readErr error

	interruptMu    sync.Mutex
	interruptCount int
	interruptDone  chan struct{}

	notifs    chan Notification
	done      chan struct{}
	closeOnce sync.Once
}

// DialAppServerUnix connects to a codex app-server listening on unix://<sockPath>.
func DialAppServerUnix(ctx context.Context, sockPath string) (*AppServerClient, error) {
	dialer := websocket.Dialer{
		NetDialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(dctx, "unix", sockPath)
		},
	}
	conn, _, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if err != nil {
		return nil, fmt.Errorf("dial codex app-server unix %s: %w", sockPath, err)
	}
	return newAppServerClient(conn), nil
}

// DialAppServerWS connects to a codex app-server listening on ws://host:port,
// sending an Authorization: Bearer header when bearerToken is non-empty.
func DialAppServerWS(ctx context.Context, wsURL, bearerToken string) (*AppServerClient, error) {
	var hdr http.Header
	if bearerToken != "" {
		hdr = http.Header{"Authorization": []string{"Bearer " + bearerToken}}
	}
	dialer := websocket.Dialer{}
	conn, _, err := dialer.DialContext(ctx, wsURL, hdr)
	if err != nil {
		return nil, fmt.Errorf("dial codex app-server ws %s: %w", wsURL, err)
	}
	return newAppServerClient(conn), nil
}

func newAppServerClient(conn *websocket.Conn) *AppServerClient {
	// Match server's x-codex-websocket-max-unfragmented-message-bytes (16 MiB).
	conn.SetReadLimit(16 << 20)
	c := &AppServerClient{
		conn:    conn,
		pending: map[int64]chan rpcResponse{},
		notifs:  make(chan Notification, 256),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Notifications returns the stream of server notifications until the client closes.
func (c *AppServerClient) Notifications() <-chan Notification { return c.notifs }

// Done is closed when the underlying connection terminates.
func (c *AppServerClient) Done() <-chan struct{} { return c.done }

// Close closes the WebSocket connection and cancels all pending RPC calls.
func (c *AppServerClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(200*time.Millisecond),
		)
		err = c.conn.Close()
	})
	return err
}

func (c *AppServerClient) readLoop() {
	defer func() {
		c.mu.Lock()
		c.closed = true
		err := c.readErr
		if err == nil {
			err = io.EOF
		}
		for id, ch := range c.pending {
			ch <- rpcResponse{err: err}
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.notifs)
		close(c.done)
	}()

	for {
		mt, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			continue
		}
		var msg wireMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
		switch {
		case hasID && msg.Method == "":
			var id int64
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- rpcResponse{err: msg.Error}
				} else {
					ch <- rpcResponse{result: msg.Result}
				}
			}
		case !hasID && msg.Method != "":
			n := Notification{Method: msg.Method, Params: msg.Params, EmittedAtMs: msg.EmittedAtMs}
			select {
			case c.notifs <- n:
			default:
				// Never block the read loop on a slow consumer; drop oldest notification.
				select {
				case <-c.notifs:
				default:
				}
				select {
				case c.notifs <- n:
				default:
				}
			}
		default:
			// ServerRequest (hasID && msg.Method != ""): intentionally passive so
			// the connected --remote TUI and Mako's PermissionRequest hook handle approvals.
		}
	}
}

// Call sends a JSON-RPC 2.0 request and decodes the response into out (if non-nil).
func (c *AppServerClient) Call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)

	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		c.dropPending(id)
		return err
	}
	if err := c.writeJSON(payload); err != nil {
		c.dropPending(id)
		return err
	}

	select {
	case resp := <-ch:
		if resp.err != nil {
			return resp.err
		}
		if out != nil && len(resp.result) > 0 {
			if err := json.Unmarshal(resp.result, out); err != nil {
				return fmt.Errorf("codex app-server decode %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	}
}

// Notify sends a JSON-RPC 2.0 notification (no response expected).
func (c *AppServerClient) Notify(method string, params any) error {
	msg := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.writeJSON(payload)
}

func (c *AppServerClient) writeJSON(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *AppServerClient) dropPending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Initialize performs the initialize request + initialized notification handshake.
func (c *AppServerClient) Initialize(ctx context.Context, name, version string, experimentalAPI bool) (InitializeResult, error) {
	var res InitializeResult
	clientName := cmp.Or(name, "mako")
	title := "Mako"
	ver := cmp.Or(version, "0.1.0")
	if clientName == NonOriginatingClientName {
		title = NonOriginatingClientTitle
		ver = cmp.Or(version, NonOriginatingClientVersion)
	}
	params := InitializeParams{
		ClientInfo: ClientInfo{
			Name:    clientName,
			Title:   title,
			Version: ver,
		},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: experimentalAPI},
	}
	if err := c.Call(ctx, "initialize", params, &res); err != nil {
		return res, err
	}
	if err := c.Notify("initialized", nil); err != nil {
		return res, err
	}
	return res, nil
}

func (c *AppServerClient) ThreadStart(ctx context.Context, params ThreadStartParams) (ThreadStartResult, error) {
	var res ThreadStartResult
	err := c.Call(ctx, "thread/start", params, &res)
	return res, err
}

func (c *AppServerClient) ThreadResume(ctx context.Context, params ThreadResumeParams) (ThreadResumeResult, error) {
	var res ThreadResumeResult
	err := c.Call(ctx, "thread/resume", params, &res)
	return res, err
}

func (c *AppServerClient) ThreadRead(ctx context.Context, threadID string, includeTurns bool) (ThreadReadResult, error) {
	var res ThreadReadResult
	err := c.Call(ctx, "thread/read", ThreadReadParams{ThreadID: threadID, IncludeTurns: includeTurns}, &res)
	return res, err
}

func (c *AppServerClient) ThreadLoadedList(ctx context.Context) (ThreadLoadedListResult, error) {
	var res ThreadLoadedListResult
	err := c.Call(ctx, "thread/loaded/list", struct{}{}, &res)
	return res, err
}

func (c *AppServerClient) ThreadUnarchive(ctx context.Context, threadID string) error {
	return c.Call(ctx, "thread/unarchive", struct {
		ThreadID string `json:"threadId"`
	}{ThreadID: threadID}, nil)
}

func (c *AppServerClient) ThreadFork(ctx context.Context, params ThreadForkParams) (ThreadForkResult, error) {
	var res ThreadForkResult
	err := c.Call(ctx, "thread/fork", params, &res)
	return res, err
}

func (c *AppServerClient) ThreadRollback(ctx context.Context, threadID string, numTurns int) (ThreadRollbackResult, error) {
	var res ThreadRollbackResult
	err := c.Call(ctx, "thread/rollback", ThreadRollbackParams{ThreadID: threadID, NumTurns: numTurns}, &res)
	return res, err
}

func (c *AppServerClient) ThreadBackgroundTerminalsList(ctx context.Context, threadID string) ([]BackgroundTerminal, error) {
	var out []BackgroundTerminal
	var cursor string
	for page := 0; page < 16; page++ {
		var res BackgroundTerminalsListResult
		if err := c.Call(ctx, "thread/backgroundTerminals/list", BackgroundTerminalsListParams{
			ThreadID: threadID,
			Cursor:   cursor,
		}, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Data...)
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = *res.NextCursor
	}
	return out, nil
}

func (c *AppServerClient) beginInterrupt() {
	c.interruptMu.Lock()
	if c.interruptCount == 0 {
		c.interruptDone = make(chan struct{})
	}
	c.interruptCount++
	c.interruptMu.Unlock()
}

func (c *AppServerClient) endInterrupt() {
	c.interruptMu.Lock()
	c.interruptCount--
	if c.interruptCount == 0 && c.interruptDone != nil {
		close(c.interruptDone)
		c.interruptDone = nil
	}
	c.interruptMu.Unlock()
}

// WaitPendingInterrupt blocks until any in-flight TurnInterrupt RPC finishes
// (or ctx is cancelled), preventing a stale turn/interrupt from racing ahead
// and aborting a subsequently started turn.
func (c *AppServerClient) WaitPendingInterrupt(ctx context.Context) error {
	for {
		c.interruptMu.Lock()
		done := c.interruptDone
		c.interruptMu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *AppServerClient) ThreadQueueAdd(ctx context.Context, params ThreadQueueAddParams) (ThreadQueueAddResult, error) {
	if err := c.WaitPendingInterrupt(ctx); err != nil {
		return ThreadQueueAddResult{}, err
	}
	var res ThreadQueueAddResult
	err := c.Call(ctx, "thread/queue/add", params, &res)
	return res, err
}

func (c *AppServerClient) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResult, error) {
	if err := c.WaitPendingInterrupt(ctx); err != nil {
		return TurnStartResult{}, err
	}
	var res TurnStartResult
	err := c.Call(ctx, "turn/start", params, &res)
	return res, err
}

func (c *AppServerClient) TurnSteer(ctx context.Context, params TurnSteerParams) (TurnSteerResult, error) {
	if err := c.WaitPendingInterrupt(ctx); err != nil {
		return TurnSteerResult{}, err
	}
	var res TurnSteerResult
	err := c.Call(ctx, "turn/steer", params, &res)
	return res, err
}

// StartTurnInterrupt registers the pending interrupt synchronously before
// dispatching JSON-RPC `turn/interrupt` asynchronously, returning a channel
// that receives the RPC result. Because registration happens before return,
// subsequent TurnStart / ThreadQueueAdd / TurnSteer calls on this client are
// guaranteed to serialize behind the interrupt even before the RPC goroutine
// is scheduled.
func (c *AppServerClient) StartTurnInterrupt(ctx context.Context, params TurnInterruptParams) <-chan error {
	done := make(chan error, 1)
	if params.ThreadID == "" || params.TurnID == "" {
		done <- nil
		close(done)
		return done
	}
	c.beginInterrupt()
	go func() {
		defer c.endInterrupt()
		done <- c.Call(ctx, "turn/interrupt", params, nil)
		close(done)
	}()
	return done
}

// TurnInterrupt requests cancellation of an active Codex turn via JSON-RPC
// `turn/interrupt`. While in flight, subsequent TurnStart / ThreadQueueAdd /
// TurnSteer calls on this client wait for TurnInterrupt to settle first.
func (c *AppServerClient) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	return <-c.StartTurnInterrupt(ctx, params)
}

func (c *AppServerClient) RateLimitsRead(ctx context.Context) (RateLimitsReadResult, error) {
	var res RateLimitsReadResult
	err := c.Call(ctx, "account/rateLimits/read", struct{}{}, &res)
	return res, err
}

func (c *AppServerClient) ModelList(ctx context.Context, params ModelListParams) (ModelListResult, error) {
	var res ModelListResult
	err := c.Call(ctx, "model/list", params, &res)
	return res, err
}

// QueryModelsStdio runs a short-lived `codex app-server --stdio` subprocess,
// handshakes over NDJSON, paginates `model/list`, and converts non-hidden models.
func QueryModelsStdio(ctx context.Context, program string, env map[string]string) ([]agent.ModelInfo, error) {
	if program == "" {
		return nil, errors.New("codex: empty program")
	}
	cmd := exec.CommandContext(ctx, program, "app-server", "--stdio")
	cmd.Env = mergeProcessEnv(os.Environ(), env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := safeexec.Start(cmd); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	return queryModelsStdioConn(ctx, stdout, stdin)
}

func queryModelsStdioConn(ctx context.Context, r io.Reader, w io.Writer) ([]agent.ModelInfo, error) {
	type readResult struct {
		line []byte
		err  error
	}
	lines := make(chan readResult, 8)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		for sc.Scan() {
			select {
			case lines <- readResult{line: slices.Clone(sc.Bytes())}:
			case <-done:
				return
			}
		}
		if err := sc.Err(); err != nil {
			select {
			case lines <- readResult{err: err}:
			case <-done:
			}
		}
	}()

	writeLine := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(append(b, '\n'))
		return err
	}

	readResponse := func(wantID int64, out any) error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case res, ok := <-lines:
				if !ok {
					return io.EOF
				}
				if res.err != nil {
					return res.err
				}
				var msg wireMessage
				if json.Unmarshal(res.line, &msg) != nil {
					continue
				}
				if len(msg.ID) == 0 || msg.Method != "" {
					continue // ignore interleaved notifications/requests
				}
				var id int64
				if json.Unmarshal(msg.ID, &id) != nil || id != wantID {
					continue
				}
				if msg.Error != nil {
					return msg.Error
				}
				if out != nil && len(msg.Result) > 0 {
					return json.Unmarshal(msg.Result, out)
				}
				return nil
			}
		}
	}

	// 1. initialize + initialized
	if err := writeLine(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": InitializeParams{
			ClientInfo: ClientInfo{Name: "mako", Title: "Mako", Version: "1"},
		},
	}); err != nil {
		return nil, err
	}
	var initRes InitializeResult
	if err := readResponse(1, &initRes); err != nil {
		return nil, err
	}
	if err := writeLine(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return nil, err
	}

	// 2. Paginate model/list (includeHidden: false).
	includeHidden := false
	var cursor *string
	var models []agent.ModelInfo
	seen := map[string]bool{}
	var reqID int64 = 2
	for page := 0; page < 20; page++ {
		if err := writeLine(map[string]any{
			"jsonrpc": "2.0",
			"id":      reqID,
			"method":  "model/list",
			"params": ModelListParams{
				Cursor:        cursor,
				IncludeHidden: &includeHidden,
			},
		}); err != nil {
			return nil, err
		}
		var pageRes ModelListResult
		if err := readResponse(reqID, &pageRes); err != nil {
			return nil, err
		}
		reqID++
		for _, m := range pageRes.Data {
			id := cmp.Or(m.ID, m.Model)
			if id == "" || m.Hidden || seen[id] {
				continue
			}
			seen[id] = true
			efforts := make([]string, 0, len(m.SupportedReasoningEfforts))
			for _, r := range m.SupportedReasoningEfforts {
				if r.ReasoningEffort != "" {
					efforts = append(efforts, r.ReasoningEffort)
				}
			}
			models = append(models, agent.ModelInfo{
				ID:      id,
				Name:    cmp.Or(m.DisplayName, id),
				Efforts: efforts,
			})
		}
		if pageRes.NextCursor == nil || *pageRes.NextCursor == "" {
			break
		}
		cursor = pageRes.NextCursor
	}
	return models, nil
}

// RewindThreadOptions configures a non-destructive Codex conversation fork
// immediately before UserTurnIndex (0-based).
type RewindThreadOptions struct {
	ThreadID        string
	UserTurnIndex   int
	ServerUserAgent string
	Cwd             string
	Model           string
	ApprovalPolicy  string
	Sandbox         string
}

// RewindThreadResult describes the forked thread created by RewindThread.
type RewindThreadResult struct {
	ThreadID  string
	Path      string
	ZeroTurns bool
}

var codexSemverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// codexVersionAtLeast reports whether a codex userAgent string contains a
// semantic version >= (major, minor, patch).
func codexVersionAtLeast(userAgent string, major, minor, patch int) bool {
	m := codexSemverRe.FindStringSubmatch(userAgent)
	if len(m) != 4 {
		return false
	}
	maj, err1 := strconv.Atoi(m[1])
	min, err2 := strconv.Atoi(m[2])
	pat, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	if maj != major {
		return maj > major
	}
	if min != minor {
		return min > minor
	}
	return pat >= patch
}

// RewindThread forks an existing Codex thread just before UserTurnIndex using
// an active app-server client. On Codex >=0.156 or paginated history threads it
// uses `thread/fork` with `beforeTurnId`; on legacy threads it uses
// `thread/fork` followed by `thread/rollback`.
func RewindThread(ctx context.Context, client *AppServerClient, opts RewindThreadOptions) (RewindThreadResult, error) {
	if client == nil {
		return RewindThreadResult{}, errors.New("codex rewind: nil app-server client")
	}
	if opts.ThreadID == "" {
		return RewindThreadResult{}, errors.New("codex rewind: missing thread id")
	}
	if opts.UserTurnIndex < 0 {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: invalid turn index %d", opts.UserTurnIndex)
	}
	if opts.UserTurnIndex == 0 {
		return RewindThreadResult{ZeroTurns: true}, nil
	}

	readRes, err := client.ThreadRead(ctx, opts.ThreadID, true)
	if err != nil {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/read: %w", err)
	}
	turns := readRes.Thread.Turns
	if opts.UserTurnIndex >= len(turns) {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: turn index %d out of range (%d turns)", opts.UserTurnIndex, len(turns))
	}
	targetTurnID := turns[opts.UserTurnIndex].ID
	numTurns := len(turns) - opts.UserTurnIndex

	baseFork := ThreadForkParams{
		ThreadID:               opts.ThreadID,
		Cwd:                    opts.Cwd,
		Model:                  opts.Model,
		ApprovalPolicy:         opts.ApprovalPolicy,
		Sandbox:                opts.Sandbox,
		ExcludeTurns:           true,
		PersistExtendedHistory: true,
	}

	preferBeforeTurn := readRes.Thread.HistoryMode == "paginated" || codexVersionAtLeast(opts.ServerUserAgent, 0, 156, 0)
	if preferBeforeTurn && targetTurnID != "" {
		p := baseFork
		p.BeforeTurnID = targetTurnID
		forked, err := client.ThreadFork(ctx, p)
		if err == nil {
			if forked.Thread.ID == "" {
				return RewindThreadResult{}, errors.New("codex rewind: thread/fork returned empty thread id")
			}
			return RewindThreadResult{ThreadID: forked.Thread.ID, Path: forked.Thread.Path}, nil
		}
		if !IsInvalidRequest(err) {
			return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/fork: %w", err)
		}
		// Fall through to legacy fork + rollback if server rejected beforeTurnId.
	}

	forked, err := client.ThreadFork(ctx, baseFork)
	if err != nil {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/fork: %w", err)
	}
	if forked.Thread.ID == "" {
		return RewindThreadResult{}, errors.New("codex rewind: thread/fork returned empty thread id")
	}
	rolled, err := client.ThreadRollback(ctx, forked.Thread.ID, numTurns)
	if err != nil {
		// If Codex 0.156+ dropped thread/rollback despite an unrecognized User-Agent,
		// retry via beforeTurnId.
		if IsMethodNotFound(err) && targetTurnID != "" {
			p := baseFork
			p.BeforeTurnID = targetTurnID
			fork2, err2 := client.ThreadFork(ctx, p)
			if err2 == nil && fork2.Thread.ID != "" {
				return RewindThreadResult{ThreadID: fork2.Thread.ID, Path: fork2.Thread.Path}, nil
			}
		}
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/rollback: %w", err)
	}
	finalID := cmp.Or(rolled.Thread.ID, forked.Thread.ID)
	finalPath := cmp.Or(rolled.Thread.Path, forked.Thread.Path)
	return RewindThreadResult{ThreadID: finalID, Path: finalPath}, nil
}

// RewindThreadStdio runs a short-lived `codex app-server --stdio` subprocess to
// rewind a thread when no live per-session app-server bridge is attached.
func RewindThreadStdio(ctx context.Context, program string, env map[string]string, opts RewindThreadOptions) (RewindThreadResult, error) {
	if opts.UserTurnIndex == 0 {
		return RewindThreadResult{ZeroTurns: true}, nil
	}
	if program == "" {
		return RewindThreadResult{}, errors.New("codex rewind: empty program")
	}
	cmd := exec.CommandContext(ctx, program, "app-server", "--stdio")
	cmd.Env = mergeProcessEnv(os.Environ(), env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return RewindThreadResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RewindThreadResult{}, err
	}
	cmd.Stderr = io.Discard
	if err := safeexec.Start(cmd); err != nil {
		return RewindThreadResult{}, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	return rewindThreadStdioConn(ctx, stdout, stdin, opts)
}

func rewindThreadStdioConn(ctx context.Context, r io.Reader, w io.Writer, opts RewindThreadOptions) (RewindThreadResult, error) {
	type readResult struct {
		line []byte
		err  error
	}
	lines := make(chan readResult, 8)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		for sc.Scan() {
			select {
			case lines <- readResult{line: slices.Clone(sc.Bytes())}:
			case <-done:
				return
			}
		}
		if err := sc.Err(); err != nil {
			select {
			case lines <- readResult{err: err}:
			case <-done:
			}
		}
	}()

	var nextID int64
	call := func(method string, params any, out any) error {
		nextID++
		id := nextID
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case res, ok := <-lines:
				if !ok {
					return io.EOF
				}
				if res.err != nil {
					return res.err
				}
				var msg wireMessage
				if json.Unmarshal(res.line, &msg) != nil || len(msg.ID) == 0 || msg.Method != "" {
					continue
				}
				var gotID int64
				if json.Unmarshal(msg.ID, &gotID) != nil || gotID != id {
					continue
				}
				if msg.Error != nil {
					return msg.Error
				}
				if out != nil && len(msg.Result) > 0 {
					return json.Unmarshal(msg.Result, out)
				}
				return nil
			}
		}
	}

	var initRes InitializeResult
	if err := call("initialize", InitializeParams{
		ClientInfo: ClientInfo{
			Name:    NonOriginatingClientName,
			Title:   NonOriginatingClientTitle,
			Version: NonOriginatingClientVersion,
		},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: true},
	}, &initRes); err != nil {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: initialize: %w", err)
	}
	if b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err == nil {
		_, _ = w.Write(append(b, '\n'))
	}

	var readRes ThreadReadResult
	if err := call("thread/read", ThreadReadParams{ThreadID: opts.ThreadID, IncludeTurns: true}, &readRes); err != nil {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/read: %w", err)
	}
	turns := readRes.Thread.Turns
	if opts.UserTurnIndex >= len(turns) {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: turn index %d out of range (%d turns)", opts.UserTurnIndex, len(turns))
	}
	targetTurnID := turns[opts.UserTurnIndex].ID
	numTurns := len(turns) - opts.UserTurnIndex

	baseFork := ThreadForkParams{
		ThreadID:               opts.ThreadID,
		Cwd:                    opts.Cwd,
		Model:                  opts.Model,
		ApprovalPolicy:         opts.ApprovalPolicy,
		Sandbox:                opts.Sandbox,
		ExcludeTurns:           true,
		PersistExtendedHistory: true,
	}

	ua := cmp.Or(opts.ServerUserAgent, initRes.UserAgent)
	if (readRes.Thread.HistoryMode == "paginated" || codexVersionAtLeast(ua, 0, 156, 0)) && targetTurnID != "" {
		p := baseFork
		p.BeforeTurnID = targetTurnID
		var forked ThreadForkResult
		err := call("thread/fork", p, &forked)
		if err == nil {
			if forked.Thread.ID == "" {
				return RewindThreadResult{}, errors.New("codex rewind: thread/fork returned empty thread id")
			}
			return RewindThreadResult{ThreadID: forked.Thread.ID, Path: forked.Thread.Path}, nil
		}
		if !IsInvalidRequest(err) {
			return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/fork: %w", err)
		}
	}

	var forked ThreadForkResult
	if err := call("thread/fork", baseFork, &forked); err != nil {
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/fork: %w", err)
	}
	if forked.Thread.ID == "" {
		return RewindThreadResult{}, errors.New("codex rewind: thread/fork returned empty thread id")
	}
	var rolled ThreadRollbackResult
	if err := call("thread/rollback", ThreadRollbackParams{ThreadID: forked.Thread.ID, NumTurns: numTurns}, &rolled); err != nil {
		if IsMethodNotFound(err) && targetTurnID != "" {
			p := baseFork
			p.BeforeTurnID = targetTurnID
			var fork2 ThreadForkResult
			if err2 := call("thread/fork", p, &fork2); err2 == nil && fork2.Thread.ID != "" {
				return RewindThreadResult{ThreadID: fork2.Thread.ID, Path: fork2.Thread.Path}, nil
			}
		}
		return RewindThreadResult{}, fmt.Errorf("codex rewind: thread/rollback: %w", err)
	}
	return RewindThreadResult{
		ThreadID: cmp.Or(rolled.Thread.ID, forked.Thread.ID),
		Path:     cmp.Or(rolled.Thread.Path, forked.Thread.Path),
	}, nil
}

func mergeProcessEnv(base []string, overrides map[string]string) []string {
	return agent.BuildChildEnv(base, overrides)
}
