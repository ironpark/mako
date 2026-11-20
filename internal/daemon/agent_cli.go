package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

const (
	agentMsgArg   = "agent-msg"
	agySidecarArg = "agy-sidecar"
	agentCLISock  = "agent-cli.sock"
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
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, agentMsgArg, socket}) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "mako-msg"), []byte(script), 0o700)
}

type agentCLIRequest struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	Op      string `json:"op"` // wait, read, send, list, profiles, sidecar_subscribe

	// wait / read options:
	AfterID    uint64 `json:"after_id,omitempty"`
	AckThrough uint64 `json:"ack_through,omitempty"`
	From       string `json:"from,omitempty"`
	WatchID    string `json:"watch_session_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	TimeoutSec int    `json:"timeout_seconds,omitempty"`
	NoTimeout  bool   `json:"no_timeout,omitempty"`
	Ack        bool   `json:"ack,omitempty"`

	// send options:
	To   string `json:"to,omitempty"`
	Text string `json:"text,omitempty"`
}

type agentCLIResponse struct {
	OK          bool                     `json:"ok"`
	Error       string                   `json:"error,omitempty"`
	Reason      string                   `json:"reason,omitempty"` // message, turn_ended, idle, waiting, exited, gone, timeout
	Messages    []agentMessage           `json:"messages,omitempty"`
	NextAfterID uint64                   `json:"next_after_id,omitempty"`
	Sent        *agentMessage            `json:"sent,omitempty"`
	Agents      *listAgentsResult        `json:"agents,omitempty"`
	Profiles    *listAgentProfilesResult `json:"profiles,omitempty"`
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
	if err := json.NewDecoder(io.LimitReader(conn, 128<<10)).Decode(&req); err != nil {
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

	default:
		writeResp(agentCLIResponse{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

func (d *Daemon) cliWait(ctx context.Context, peer *agentPeer, req agentCLIRequest) agentCLIResponse {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return agentCLIResponse{Error: err.Error()}
	}
	if !req.NoTimeout {
		sec := req.TimeoutSec
		if sec <= 0 {
			sec = 600
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}

	var target *waited
	var initialTurns uint64
	var worked bool
	if req.WatchID != "" {
		if req.WatchID == peer.id {
			return agentCLIResponse{Error: "an agent cannot wait on itself"}
		}
		scope := d.projectScope(caller.Info())
		if s, ok := d.sessions.get(req.WatchID); ok && d.inProject(scope, s.Info()) {
			target = &waited{info: s.Info, changed: s.Changed}
		} else if d.remoteAgent(scope, req.WatchID) != nil {
			target = &waited{info: func() *protocol.SessionInfo { return d.remoteAgent(scope, req.WatchID) }}
		} else {
			return agentCLIResponse{Error: errNotInProject.Error()}
		}
		release := d.projectAgents.waitOn(req.WatchID)
		defer release()
		initialTurns = target.info().GetTurns()
	}

	var recheck <-chan time.Time
	var ticker *time.Ticker
	if target != nil {
		ticker = time.NewTicker(waitRecheck)
		defer ticker.Stop()
		recheck = ticker.C
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

		var sessChanged <-chan struct{}
		if target != nil {
			if target.changed != nil {
				sessChanged = target.changed()
			}
			info := target.info()
			status := info.GetStatus()
			worked = worked || protocol.AtWork(status)
			switch {
			case info == nil:
				return agentCLIResponse{OK: true, Reason: "gone"}
			case info.GetState() == protocol.StateExited:
				return agentCLIResponse{OK: true, Reason: "exited", Status: status, State: info.GetState()}
			case status == protocol.AgentWaiting:
				return agentCLIResponse{OK: true, Reason: "waiting", Status: status, State: info.GetState()}
			case info.GetTurns() > initialTurns || (initialTurns == 0 && worked && !protocol.AtWork(status)):
				return agentCLIResponse{OK: true, Reason: "turn_ended", Status: status, State: info.GetState()}
			}
		}

		select {
		case <-changed:
		case <-sessChanged:
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
		watch := fs.String("session", "", "also wake when this child session finishes, waits, or exits")
		timeout := fs.Int("timeout", 600, "timeout in seconds (0 waits indefinitely)")
		noAck := fs.Bool("no-ack", false, "do not acknowledge returned messages")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.AfterID = *after
		req.From = strings.TrimSpace(*from)
		req.WatchID = strings.TrimSpace(*watch)
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
