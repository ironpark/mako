package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

const (
	agentMsgArg    = "agent-msg"
	agentPluginArg = "agent-plugin"
	agySidecarArg  = "agy-sidecar"
	agentCLISock   = "agent-cli.sock"
)

// writeAgentMsgTool writes the `mako-msg` helper script into dir (alongside
// clipboard shims, which are prepended to every session's PATH).
func writeAgentMsgTool(dir, program, socket string) error {
	if dir == "" || program == "" || socket == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@echo off\r\n\"%s\" %s \"%s\" %%*\r\n", program, agentMsgArg, socket)
		return os.WriteFile(filepath.Join(dir, "mako-msg.cmd"), []byte(script), 0o700)
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentMsgArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-msg"), []byte(script), 0o700)
}

// writeAgentPluginTool writes the `mako-plugin` helper script into dir
// (alongside `mako-msg`) so agents and shells inside a Mako session can
// discover and call plugin tools via CLI without bloating MCP schemas.
func writeAgentPluginTool(dir, program, socket string) error {
	if dir == "" || program == "" || socket == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@echo off\r\n\"%s\" %s \"%s\" %%*\r\n", program, agentPluginArg, socket)
		return os.WriteFile(filepath.Join(dir, "mako-plugin.cmd"), []byte(script), 0o700)
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentPluginArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-plugin"), []byte(script), 0o700)
}

type agentCLIRequest struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	Op      string `json:"op"` // wait, read, send, list, profiles, plugin_list, plugin_call, sidecar_subscribe

	// wait / read options:
	AfterID       uint64   `json:"after_id,omitempty"`
	AckThrough    uint64   `json:"ack_through,omitempty"`
	From          string   `json:"from,omitempty"`
	WatchID       string   `json:"watch_session_id,omitempty"`
	WatchIDs      []string `json:"watch_session_ids,omitempty"`
	WatchChildren bool     `json:"watch_children,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	TimeoutSec    int      `json:"timeout_seconds,omitempty"`
	NoTimeout     bool     `json:"no_timeout,omitempty"`
	Ack           bool     `json:"ack,omitempty"`

	// send options:
	To   string `json:"to,omitempty"`
	Text string `json:"text,omitempty"`

	// plugin_list / plugin_call options:
	PluginID string          `json:"plugin_id,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
}

type agentCLIResponse struct {
	OK          bool                     `json:"ok"`
	Error       string                   `json:"error,omitempty"`
	Reason      string                   `json:"reason,omitempty"` // message, turn_ended, idle, waiting, exited, gone, timeout
	SessionID   string                   `json:"session_id,omitempty"`
	Messages    []agentMessage           `json:"messages,omitempty"`
	NextAfterID uint64                   `json:"next_after_id,omitempty"`
	Sent        *agentMessage            `json:"sent,omitempty"`
	Agents      *listAgentsResult        `json:"agents,omitempty"`
	Profiles    *listAgentProfilesResult `json:"profiles,omitempty"`
	Tools       []plugin.ContributedTool `json:"tools,omitempty"`
	Output      json.RawMessage          `json:"output,omitempty"`
	Status      string                   `json:"status,omitempty"`
	State       string                   `json:"state,omitempty"`
}

type sidecarPush struct {
	ID             uint64 `json:"id"`
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	Text           string `json:"text"`
	Home           string `json:"home,omitempty"`
}

type sidecarAck struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type agySidecarClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	mu   chan struct{} // 1-buffered lock for request/response serialization
}

func (c *agySidecarClient) deliver(ctx context.Context, push sidecarPush) error {
	select {
	case c.mu <- struct{}{}:
		defer func() { <-c.mu }()
	case <-ctx.Done():
		return ctx.Err()
	}
	deadline := time.Now().Add(12 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetDeadline(deadline)
	if err := c.enc.Encode(push); err != nil {
		return err
	}
	var ack sidecarAck
	if err := c.dec.Decode(&ack); err != nil {
		return err
	}
	if !ack.OK {
		if ack.Error != "" {
			return errors.New(ack.Error)
		}
		return errors.New("sidecar delivery failed")
	}
	return nil
}

func (d *Daemon) listenAgentCLI(ctx context.Context, dir string) error {
	path := filepath.Join(dir, agentCLISock)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("agent CLI: %w", err)
	}
	ln = transport.OwnListener(ln, path)
	d.projectAgents.cliSocket = path
	if d.clipTools != "" && d.hookBinary != "" {
		_ = writeAgentMsgTool(d.clipTools, d.hookBinary, path)
		_ = writeAgentPluginTool(d.clipTools, d.hookBinary, path)
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serveAgentCLI(ctx, conn)
		}
	}()
	return nil
}

func (d *Daemon) serveAgentCLI(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req agentCLIRequest
	if err := json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	if req.Op == "sidecar_subscribe" {
		d.serveAgySidecar(ctx, conn)
		return
	}

	writeResp := func(r agentCLIResponse) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(conn).Encode(r)
	}

	peer := d.projectAgents.byToken(req.Token)
	if peer == nil || peer.id != req.Session {
		writeResp(agentCLIResponse{Error: "invalid or expired session credentials"})
		return
	}
	select {
	case <-peer.ready:
	case <-peer.ctx.Done():
		writeResp(agentCLIResponse{Error: "session ended"})
		return
	case <-ctx.Done():
		return
	}

	reqCtx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		cancel()
	}()

	switch req.Op {
	case "read":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 100 {
			limit = 100
		}
		msgs, err := d.projectAgents.messages.read(reqCtx, peer.id, req.AfterID, req.AckThrough, limit, 0)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		next := max(req.AfterID, req.AckThrough)
		if len(msgs) > 0 {
			next = msgs[len(msgs)-1].ID
		}
		writeResp(agentCLIResponse{OK: true, Messages: msgs, NextAfterID: next})

	case "send":
		if _, err := d.projectCaller(peer); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		if err := checkAgentText(req.Text); err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		msg, err := d.sendAgentMessage(reqCtx, peer, req.To, req.Text)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Sent: &msg})

	case "list":
		out, err := d.listProjectAgents(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Agents: &out})

	case "profiles":
		out, err := d.listAgentProfiles(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		writeResp(agentCLIResponse{OK: true, Profiles: &out})

	case "wait":
		resp := d.cliWait(reqCtx, peer, req)
		writeResp(resp)

	case "plugin_list":
		s, err := d.projectCaller(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		var tools []plugin.ContributedTool
		if d.plugins != nil {
			scopeCtx := d.sessionScopeContext(s.ID(), s.Info())
			for _, tl := range d.plugins.Tools() {
				if req.PluginID != "" && tl.PluginID != req.PluginID {
					continue
				}
				if !toolUsableInScope(tl.Scope, scopeCtx) {
					continue
				}
				tools = append(tools, tl)
			}
		}
		writeResp(agentCLIResponse{OK: true, Tools: tools})

	case "plugin_call":
		s, err := d.projectCaller(peer)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		if d.plugins == nil {
			writeResp(agentCLIResponse{Error: "plugins are not enabled on this node"})
			return
		}
		if req.PluginID == "" || req.ToolID == "" {
			writeResp(agentCLIResponse{Error: "plugin_id and tool_id are required"})
			return
		}
		scopeCtx := d.sessionScopeContext(s.ID(), s.Info())
		out, err := d.plugins.CallToolScoped(reqCtx, req.PluginID, req.ToolID, req.Args, scopeCtx)
		if err != nil {
			writeResp(agentCLIResponse{Error: err.Error()})
			return
		}
		rawOut := json.RawMessage(out)
		if !json.Valid(rawOut) {
			rawOut, _ = json.Marshal(out)
		}
		writeResp(agentCLIResponse{OK: true, Output: rawOut})

	default:
		writeResp(agentCLIResponse{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

func (d *Daemon) cliWait(ctx context.Context, peer *agentPeer, req agentCLIRequest) agentCLIResponse {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return agentCLIResponse{Error: err.Error()}
	}
	defer d.projectAgents.addCLIWaiter(peer.id)()

	if !req.NoTimeout {
		sec := req.TimeoutSec
		if sec <= 0 {
			sec = 600
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}

	watchIDs := slices.Clone(req.WatchIDs)
	if req.WatchID != "" && !slices.Contains(watchIDs, req.WatchID) {
		watchIDs = append(watchIDs, req.WatchID)
	}
	scope := d.projectScope(caller.Info())
	if req.WatchChildren {
		d.sessionMu.Lock()
		for id, saved := range d.saved {
			if saved.ParentID == peer.id && !slices.Contains(watchIDs, id) {
				if s, ok := d.sessions.get(id); ok && s.Info().GetState() == protocol.StateRunning && d.inProject(scope, s.Info()) {
					watchIDs = append(watchIDs, id)
				}
			}
		}
		d.sessionMu.Unlock()
	}

	type watchedTarget struct {
		id           string
		target       *waited
		initialTurns uint64
		worked       bool
		lastCh       <-chan struct{}
	}
	var targets []*watchedTarget
	for _, wid := range watchIDs {
		wid = strings.TrimSpace(wid)
		if wid == "" {
			continue
		}
		if wid == peer.id {
			return agentCLIResponse{Error: "an agent cannot wait on itself"}
		}
		var wt *waited
		if s, ok := d.sessions.get(wid); ok && d.inProject(scope, s.Info()) {
			wt = &waited{info: s.Info, changed: s.Changed}
		} else if d.remoteAgent(scope, wid) != nil {
			idCopy := wid
			wt = &waited{info: func() *protocol.SessionInfo { return d.remoteAgent(scope, idCopy) }}
		} else {
			return agentCLIResponse{Error: errNotInProject.Error()}
		}
		release := d.projectAgents.waitOn(wid)
		defer release()
		targets = append(targets, &watchedTarget{
			id:           wid,
			target:       wt,
			initialTurns: wt.info().GetTurns(),
		})
	}

	var recheck <-chan time.Time
	var ticker *time.Ticker
	if len(targets) > 0 {
		ticker = time.NewTicker(waitRecheck)
		defer ticker.Stop()
		recheck = ticker.C
	}

	// Fan-in change notifications for multiple watched sessions into one channel.
	var anySessChanged chan struct{}
	if len(targets) > 0 {
		anySessChanged = make(chan struct{}, 1)
	}

	for {
		inbox, changed := d.projectAgents.messages.pending(peer.id)
		var matched []agentMessage
		for _, m := range inbox {
			if m.ID <= req.AfterID {
				continue
			}
			if req.From != "" && m.From != req.From {
				continue
			}
			matched = append(matched, m)
		}
		if len(matched) > 0 {
			next := matched[len(matched)-1].ID
			if req.Ack {
				_, _ = d.projectAgents.messages.read(ctx, peer.id, next, next, 1, 0)
			}
			return agentCLIResponse{
				OK:          true,
				Reason:      "message",
				Messages:    matched,
				NextAfterID: next,
			}
		}

		var singleChanged <-chan struct{}
		for _, wt := range targets {
			if len(targets) == 1 && wt.target.changed != nil {
				singleChanged = wt.target.changed()
			} else if wt.target.changed != nil {
				if ch := wt.target.changed(); ch != wt.lastCh {
					wt.lastCh = ch
					go func() {
						select {
						case <-ch:
							select {
							case anySessChanged <- struct{}{}:
							default:
							}
						case <-ctx.Done():
						}
					}()
				}
			}
			info := wt.target.info()
			status := info.GetStatus()
			wt.worked = wt.worked || protocol.AtWork(status)
			switch {
			case info == nil:
				return agentCLIResponse{OK: true, Reason: "gone", SessionID: wt.id}
			case info.GetState() == protocol.StateExited:
				return agentCLIResponse{OK: true, Reason: "exited", SessionID: wt.id, Status: status, State: info.GetState()}
			case status == protocol.AgentWaiting:
				return agentCLIResponse{OK: true, Reason: "waiting", SessionID: wt.id, Status: status, State: info.GetState()}
			case info.GetTurns() > wt.initialTurns || (wt.initialTurns == 0 && wt.worked && !protocol.AtWork(status)):
				return agentCLIResponse{OK: true, Reason: "turn_ended", SessionID: wt.id, Status: status, State: info.GetState()}
			}
		}

		var sessWake <-chan struct{}
		if len(targets) == 1 {
			sessWake = singleChanged
		} else if len(targets) > 1 {
			sessWake = anySessChanged
		}

		select {
		case <-changed:
		case <-sessWake:
		case <-recheck:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return agentCLIResponse{OK: true, Reason: "timeout", Messages: []agentMessage{}}
			}
			return agentCLIResponse{Error: ctx.Err().Error()}
		}
	}
}

func (d *Daemon) serveAgySidecar(ctx context.Context, conn net.Conn) {
	client := &agySidecarClient{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(bufio.NewReader(conn)),
		mu:   make(chan struct{}, 1),
	}
	d.projectAgents.mu.Lock()
	if old := d.projectAgents.agySidecar; old != nil {
		_ = old.conn.Close()
	}
	d.projectAgents.agySidecar = client
	d.projectAgents.mu.Unlock()
	defer func() {
		d.projectAgents.mu.Lock()
		if d.projectAgents.agySidecar == client {
			d.projectAgents.agySidecar = nil
		}
		d.projectAgents.mu.Unlock()
	}()
	// Wait until daemon shuts down or the sidecar disconnects.
	<-ctx.Done()
}

func (d *Daemon) deliverViaAgySidecar(ctx context.Context, push sidecarPush) bool {
	d.projectAgents.mu.Lock()
	client := d.projectAgents.agySidecar
	d.projectAgents.mu.Unlock()
	if client == nil {
		return false
	}
	if err := client.deliver(ctx, push); err != nil {
		d.log.Warn("agy sidecar delivery failed; falling back", "session", push.SessionID, "err", err)
		d.projectAgents.mu.Lock()
		if d.projectAgents.agySidecar == client {
			d.projectAgents.agySidecar = nil
		}
		d.projectAgents.mu.Unlock()
		_ = client.conn.Close()
		return false
	}
	return true
}

// runAgySidecar runs inside Antigravity's sidecar supervisor, bridging Mako
// messages from the daemon's CLI socket into agy conversations via agentapi.
func runAgySidecar(args []string, stderr io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("expected agent CLI socket path")
	}
	socket := args[0]
	driver := agy.New()
	for {
		if err := runAgySidecarOnce(socket, driver); err != nil {
			fmt.Fprintln(stderr, "mako agy-sidecar:", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func runAgySidecarOnce(socket string, driver *agy.Agent) error {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(agentCLIRequest{Op: "sidecar_subscribe"}); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var push sidecarPush
		if err := dec.Decode(&push); err != nil {
			return err
		}
		var env map[string]string
		if push.Home != "" {
			env = map[string]string{"HOME": push.Home}
		}
		err := driver.DeliverMessage(context.Background(), push.ConversationID, push.Title, push.Text, env)
		ack := sidecarAck{ID: push.ID, OK: err == nil}
		if err != nil {
			ack.Error = err.Error()
		}
		if err := enc.Encode(ack); err != nil {
			return err
		}
	}
}

// runAgentMsg implements the `mako-msg` CLI for background message waiting and
// messaging from any agent's shell (e.g., Claude Code Bash(run_in_background)).
func runAgentMsg(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: mako-msg <wait|read|send|list|profiles> [flags]")
		return 2
	}
	socket := args[0]
	sub := args[1]
	rest := args[2:]

	sessionID := os.Getenv("AGENT_MAKO_SESSION")
	if sessionID == "" {
		sessionID = os.Getenv("MAKO_SESSION")
	}
	token := os.Getenv(agentMCPTokenEnv)
	if sessionID == "" || token == "" {
		fmt.Fprintln(stderr, "mako-msg: missing AGENT_MAKO_SESSION / MAKO_AGENT_MCP_TOKEN in environment")
		return 1
	}

	req := agentCLIRequest{Session: sessionID, Token: token, Op: sub}
	switch sub {
	case "wait":
		fs := flag.NewFlagSet("mako-msg wait", flag.ContinueOnError)
		fs.SetOutput(stderr)
		after := fs.Uint64("after", 0, "only return messages with id > after")
		from := fs.String("from", "", "only wake on messages from this session ID")
		watch := fs.String("session", "", "also wake when these child session ID(s) finish, wait, or exit (comma-separated)")
		allChildren := fs.Bool("all-children", false, "also wake when any running child session spawned by this session finishes, waits, or exits")
		timeout := fs.Int("timeout", 600, "timeout in seconds (0 waits indefinitely)")
		noAck := fs.Bool("no-ack", false, "do not acknowledge returned messages")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.AfterID = *after
		req.From = strings.TrimSpace(*from)
		for _, part := range strings.Split(*watch, ",") {
			if id := strings.TrimSpace(part); id != "" {
				req.WatchIDs = append(req.WatchIDs, id)
			}
		}
		req.WatchChildren = *allChildren
		req.TimeoutSec = *timeout
		req.NoTimeout = *timeout == 0
		req.Ack = !*noAck

	case "read":
		fs := flag.NewFlagSet("mako-msg read", flag.ContinueOnError)
		fs.SetOutput(stderr)
		after := fs.Uint64("after", 0, "return messages with id > after")
		ack := fs.Uint64("ack", 0, "acknowledge messages through this ID")
		limit := fs.Int("limit", 50, "maximum messages to return")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.AfterID = *after
		req.AckThrough = *ack
		req.Limit = *limit

	case "send":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "usage: mako-msg send <to-session-id> <text...>")
			return 2
		}
		req.To = rest[0]
		if len(rest) == 1 || (len(rest) == 2 && rest[1] == "-") {
			b, err := io.ReadAll(io.LimitReader(stdin, maxAgentMessage+1))
			if err != nil {
				fmt.Fprintln(stderr, "mako-msg send:", err)
				return 1
			}
			req.Text = string(b)
		} else {
			req.Text = strings.Join(rest[1:], " ")
		}

	case "list", "profiles":
		// no extra args required

	default:
		fmt.Fprintf(stderr, "mako-msg: unknown subcommand %q (expected wait, read, send, list, profiles)\n", sub)
		return 2
	}

	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	var resp agentCLIResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(stderr, "mako-msg:", resp.Error)
		return 1
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	_, _ = stdout.Write(append(out, '\n'))
	return 0
}

// toolUsableInScope reports whether a contributed tool declaring scope can be
// invoked by a session whose resolved ScopeContext is sc.
func toolUsableInScope(scope string, sc plugin.ScopeContext) bool {
	switch scope {
	case "", protocol.ScopeGlobal:
		return true
	case protocol.ScopeSession:
		return sc.SessionID != ""
	case protocol.ScopeProject:
		return sc.ProjectID != ""
	case protocol.ScopeSpace:
		return sc.SpaceID != ""
	default:
		return false
	}
}

// pluginEnvFilter parses AGENT_MAKO_PLUGINS (or MAKO_PLUGINS) into a predicate
// selecting which plugin tools are exposed to this shell environment:
//   - unset / empty: all tools usable in the session's scope
//   - "none" / "-": no plugin tools
//   - comma-separated entries of "<plugin>" or "<plugin>:<tool>"
func pluginEnvFilter() func(pluginID, toolID string) bool {
	raw, ok := os.LookupEnv("AGENT_MAKO_PLUGINS")
	if !ok {
		raw, ok = os.LookupEnv("MAKO_PLUGINS")
	}
	if !ok {
		return func(string, string) bool { return true }
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return func(string, string) bool { return true }
	}
	if strings.EqualFold(trimmed, "none") || trimmed == "-" {
		return func(string, string) bool { return false }
	}
	allowedPlugins := map[string]bool{}
	allowedTools := map[string]bool{}
	for _, entry := range strings.Split(trimmed, ",") {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		if strings.Contains(e, ":") {
			allowedTools[e] = true
		} else {
			allowedPlugins[e] = true
		}
	}
	return func(pluginID, toolID string) bool {
		return allowedPlugins[pluginID] || allowedTools[pluginID+":"+toolID]
	}
}

func filterToolsByEnv(tools []plugin.ContributedTool, allow func(pluginID, toolID string) bool) []plugin.ContributedTool {
	var out []plugin.ContributedTool
	for _, tl := range tools {
		if allow(tl.PluginID, tl.ID) {
			out = append(out, tl)
		}
	}
	return out
}

func dialAgentCLI(socket string, req agentCLIRequest) (agentCLIResponse, error) {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return agentCLIResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return agentCLIResponse{}, err
	}
	var resp agentCLIResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return agentCLIResponse{}, err
	}
	return resp, nil
}

// runAgentPlugin implements the `mako-plugin` CLI available in every agent
// session's PATH, letting agents discover and invoke plugin tools directly
// from the shell using the session's authenticated project/space/session scope.
func runAgentPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	const helpText = `usage:
  mako-plugin list [--json] [plugin-id]
  mako-plugin help <plugin-id> [tool-id]
  mako-plugin call <plugin-id> <tool-id> [--json <json|->] [--<param> <value|-> ...]
`
	if len(args) < 2 {
		fmt.Fprint(stderr, helpText)
		return 2
	}
	socket := args[0]
	sub := args[1]
	rest := args[2:]

	if sub == "-h" || sub == "--help" || (sub == "help" && len(rest) == 0) {
		fmt.Fprint(stdout, helpText)
		return 0
	}

	sessionID := os.Getenv("AGENT_MAKO_SESSION")
	if sessionID == "" {
		sessionID = os.Getenv("MAKO_SESSION")
	}
	token := os.Getenv(agentMCPTokenEnv)
	if sessionID == "" || token == "" {
		fmt.Fprintln(stderr, "mako-plugin: missing AGENT_MAKO_SESSION / MAKO_AGENT_MCP_TOKEN in environment")
		return 1
	}
	allowEnv := pluginEnvFilter()

	switch sub {
	case "list":
		asJSON := false
		pluginFilter := ""
		for _, a := range rest {
			switch a {
			case "--json", "-json":
				asJSON = true
			default:
				if strings.HasPrefix(a, "-") {
					fmt.Fprintf(stderr, "mako-plugin list: unknown flag %q\n", a)
					return 2
				}
				pluginFilter = a
			}
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_list",
			PluginID: pluginFilter,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		visible := filterToolsByEnv(resp.Tools, allowEnv)
		if asJSON {
			if visible == nil {
				visible = []plugin.ContributedTool{}
			}
			out, _ := json.MarshalIndent(visible, "", "  ")
			_, _ = stdout.Write(append(out, '\n'))
			return 0
		}
		if len(visible) == 0 {
			fmt.Fprintln(stdout, "No plugin tools available.")
			return 0
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PLUGIN\tTOOL\tSCOPE\tDESCRIPTION")
		for _, tl := range visible {
			scope := tl.Scope
			if scope == "" {
				scope = "global"
			}
			desc := strings.TrimPrefix(tl.Description, fmt.Sprintf("[%s plugin] ", tl.PluginID))
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", tl.PluginID, tl.ID, scope, desc)
		}
		_ = w.Flush()
		return 0

	case "help":
		pluginID := rest[0]
		toolFilter := ""
		if len(rest) > 1 {
			toolFilter = rest[1]
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_list",
			PluginID: pluginID,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		matched := 0
		for _, tl := range filterToolsByEnv(resp.Tools, allowEnv) {
			if toolFilter != "" && tl.ID != toolFilter {
				continue
			}
			matched++
			scope := tl.Scope
			if scope == "" {
				scope = "global"
			}
			desc := strings.TrimPrefix(tl.Description, fmt.Sprintf("[%s plugin] ", tl.PluginID))
			fmt.Fprintf(stdout, "%s %s (%s)\n  %s\n", tl.PluginID, tl.ID, scope, desc)
			props, _ := tl.Schema["properties"].(map[string]any)
			reqSet := map[string]bool{}
			if reqList, ok := tl.Schema["required"].([]any); ok {
				for _, r := range reqList {
					if s, ok := r.(string); ok {
						reqSet[s] = true
					}
				}
			}
			names := make([]string, 0, len(props))
			for k := range props {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) == 0 {
				fmt.Fprintln(stdout, "  (no parameters)")
			} else {
				for _, name := range names {
					pMap, _ := props[name].(map[string]any)
					typ, _ := pMap["type"].(string)
					pDesc, _ := pMap["description"].(string)
					reqTag := ""
					if reqSet[name] {
						reqTag = ", required"
					}
					if pDesc != "" {
						fmt.Fprintf(stdout, "  --%s <%s%s>  %s\n", name, typ, reqTag, pDesc)
					} else {
						fmt.Fprintf(stdout, "  --%s <%s%s>\n", name, typ, reqTag)
					}
				}
			}
			fmt.Fprintln(stdout)
		}
		if matched == 0 {
			fmt.Fprintf(stderr, "mako-plugin: no matching tool for %s %s\n", pluginID, toolFilter)
			return 1
		}
		return 0

	case "call":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "usage: mako-plugin call <plugin-id> <tool-id> [--json <json|->] [--<param> <value|-> ...]")
			return 2
		}
		pluginID, toolID := rest[0], rest[1]
		if !allowEnv(pluginID, toolID) {
			fmt.Fprintf(stderr, "mako-plugin: tool %s %s is not enabled in this environment\n", pluginID, toolID)
			return 1
		}
		argTokens := rest[2:]

		var argsJSON json.RawMessage
		if len(argTokens) == 0 {
			argsJSON = json.RawMessage(`{}`)
		} else if len(argTokens) >= 1 && (argTokens[0] == "--json" || strings.HasPrefix(argTokens[0], "--json=")) {
			var raw string
			if v, ok := strings.CutPrefix(argTokens[0], "--json="); ok {
				raw = v
			} else {
				if len(argTokens) < 2 {
					fmt.Fprintln(stderr, "mako-plugin call: --json requires a JSON payload or '-' for stdin")
					return 2
				}
				raw = argTokens[1]
			}
			if raw == "-" {
				b, err := io.ReadAll(io.LimitReader(stdin, 3<<20))
				if err != nil {
					fmt.Fprintln(stderr, "mako-plugin call:", err)
					return 1
				}
				raw = strings.TrimSpace(string(b))
			}
			if !json.Valid([]byte(raw)) {
				fmt.Fprintln(stderr, "mako-plugin call: invalid JSON argument")
				return 2
			}
			argsJSON = json.RawMessage(raw)
		} else {
			// Fetch schema once so flag values can be coerced to their declared JSON types.
			var paramTypes map[string]string
			if listResp, err := dialAgentCLI(socket, agentCLIRequest{
				Session:  sessionID,
				Token:    token,
				Op:       "plugin_list",
				PluginID: pluginID,
			}); err == nil && listResp.OK {
				for _, tl := range listResp.Tools {
					if tl.ID == toolID {
						if props, ok := tl.Schema["properties"].(map[string]any); ok {
							paramTypes = make(map[string]string, len(props))
							for k, v := range props {
								if m, ok := v.(map[string]any); ok {
									if t, ok := m["type"].(string); ok {
										paramTypes[k] = t
									}
								}
							}
						}
						break
					}
				}
			}
			parsed, err := parsePluginCallFlags(argTokens, paramTypes, stdin)
			if err != nil {
				fmt.Fprintln(stderr, "mako-plugin call:", err)
				return 2
			}
			b, err := json.Marshal(parsed)
			if err != nil {
				fmt.Fprintln(stderr, "mako-plugin call:", err)
				return 1
			}
			argsJSON = b
		}

		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_call",
			PluginID: pluginID,
			ToolID:   toolID,
			Args:     argsJSON,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, resp.Output, "", "  "); err == nil {
			_, _ = stdout.Write(append(pretty.Bytes(), '\n'))
		} else {
			_, _ = stdout.Write(append(resp.Output, '\n'))
		}
		return 0

	default:
		fmt.Fprintf(stderr, "mako-plugin: unknown subcommand %q\n%s", sub, helpText)
		return 2
	}
}

// parsePluginCallFlags parses `--key value` and `--key=value` arguments into a
// JSON-serializable map, reading stdin when a value is `-` and coercing values
// to the tool parameter's declared JSON Schema type when known.
func parsePluginCallFlags(tokens []string, paramTypes map[string]string, stdin io.Reader) (map[string]any, error) {
	out := map[string]any{}
	stdinUsed := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "--") || len(tok) <= 2 {
			return nil, fmt.Errorf("unexpected argument %q (expected --param value or --json '{...}')", tok)
		}
		body := tok[2:]
		key, val, hasEq := strings.Cut(body, "=")
		if !hasEq {
			if paramTypes[key] == "boolean" && (i+1 >= len(tokens) || strings.HasPrefix(tokens[i+1], "--")) {
				out[key] = true
				continue
			}
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("flag --%s requires a value", key)
			}
			i++
			val = tokens[i]
		}
		if val == "-" {
			if stdinUsed {
				return nil, fmt.Errorf("stdin ('-') can only be used once per call")
			}
			stdinUsed = true
			b, err := io.ReadAll(io.LimitReader(stdin, 3<<20))
			if err != nil {
				return nil, err
			}
			val = string(b)
		}
		coerced, err := coercePluginParam(val, paramTypes[key])
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", key, err)
		}
		out[key] = coerced
	}
	return out, nil
}

func coercePluginParam(raw, typ string) (any, error) {
	switch typ {
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("expected boolean, got %q", raw)
		}
		return b, nil
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("expected number, got %q", raw)
		}
		return f, nil
	case "array", "object":
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("expected JSON %s: %w", typ, err)
		}
		return v, nil
	default:
		return raw, nil
	}
}
