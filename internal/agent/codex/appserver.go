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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ironpark/mako/internal/agent"
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

// ThreadInfo is thread metadata returned by thread/start, thread/resume, thread/read, and thread/started.
type ThreadInfo struct {
	ID              string          `json:"id"`
	SessionID       string          `json:"sessionId,omitempty"`
	ParentThreadID  string          `json:"parentThreadId,omitempty"`
	Source          json.RawMessage `json:"source,omitempty"`
	Path            string          `json:"path,omitempty"`
	Cwd             string          `json:"cwd,omitempty"`
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoningEffort,omitempty"`
	Ephemeral       bool            `json:"ephemeral,omitempty"`
	Status          ThreadStatus    `json:"status,omitempty"`
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
	Type    string `json:"type"`
	ID      string `json:"id"`
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Server  string `json:"server,omitempty"`
	Query   string `json:"query,omitempty"`
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
	params := InitializeParams{
		ClientInfo: ClientInfo{
			Name:    cmp.Or(name, "mako"),
			Title:   "Mako",
			Version: cmp.Or(version, "0.1.0"),
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

func (c *AppServerClient) ThreadQueueAdd(ctx context.Context, params ThreadQueueAddParams) (ThreadQueueAddResult, error) {
	var res ThreadQueueAddResult
	err := c.Call(ctx, "thread/queue/add", params, &res)
	return res, err
}

func (c *AppServerClient) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResult, error) {
	var res TurnStartResult
	err := c.Call(ctx, "turn/start", params, &res)
	return res, err
}

func (c *AppServerClient) TurnSteer(ctx context.Context, params TurnSteerParams) (TurnSteerResult, error) {
	var res TurnSteerResult
	err := c.Call(ctx, "turn/steer", params, &res)
	return res, err
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
	if len(env) > 0 {
		cmd.Env = mergeProcessEnv(os.Environ(), env)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
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

func mergeProcessEnv(base []string, overrides map[string]string) []string {
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
