package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
)

// hookArg, as a program's first argument, runs it as an agent's hook.
const hookArg = "hook"

// statusArg, as a program's first argument, runs it as Claude's status
// line.
const statusArg = "statusline"

// hookProgram is this program, once HookMain has said it can be a hook.
var hookProgram string

// HookMain runs this program as an agent's hook when it was started as one,
// reporting true: main then returns. Called first in main, it also says the
// program can be one, so the daemons it runs give their agents a hook;
// without it they only watch the agents' open files.
func HookMain() bool {
	if len(os.Args) > 1 && os.Args[1] == hookArg {
		runHook(os.Args[2:], os.Stdin, os.Stdout)
		return true
	}
	if len(os.Args) > 1 && os.Args[1] == statusArg {
		runStatusLine(os.Args[2:], os.Stdin, os.Stdout)
		return true
	}
	if exe, err := os.Executable(); err == nil {
		hookProgram = exe
	}
	return false
}

// hookLimit bounds what one hook may send.
const hookLimit = 1 << 20

// hookMessage is what a hook passes on: the session it runs in, when it ran,
// and the agent's own input to it. Kind is statusArg for a status line's
// input, and "" for a hook's.
type hookMessage struct {
	Session string          `json:"session"`
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind,omitempty"`
	Input   json.RawMessage `json:"input"`
}

// hookReply answers a hook that asks for an approval: allow or deny, or ""
// to leave the question to the agent's own prompt.
type hookReply struct {
	Decision string `json:"decision,omitempty"`
	Message  string `json:"message,omitempty"`
}

// runHook is an agent's hook, run by the daemon's own program with the
// daemon's hook socket as args[0]. It passes the agent's input on for the
// session in MAKO_SESSION. The agent waits on it, so it gives up quickly,
// and it never fails: the daemon falls back to watching the agent's open
// files. An approval waits for the daemon's answer, which it writes to out
// as the agent's decision.
func runHook(args []string, stdin io.Reader, out io.Writer) {
	seq := time.Now().UnixNano() // when the agent ran it, not when it arrives
	session := os.Getenv("MAKO_SESSION")
	if len(args) != 1 || session == "" {
		return
	}
	input, err := io.ReadAll(io.LimitReader(stdin, hookLimit))
	if err != nil || !json.Valid(input) {
		return
	}
	var ev struct {
		Event string `json:"hook_event_name"`
	}
	_ = json.Unmarshal(input, &ev)
	conn, err := sendHook(args[0], hookMessage{Session: session, Seq: seq, Input: input})
	if err != nil {
		return
	}
	defer conn.Close()
	if ev.Event != "PermissionRequest" {
		return
	}
	// The agent's own timeout bounds the wait.
	_ = conn.SetDeadline(time.Time{})
	var reply hookReply
	if json.NewDecoder(conn).Decode(&reply) != nil || reply.Decision == "" {
		return
	}
	decision := map[string]any{"behavior": reply.Decision}
	if reply.Decision == protocol.DecisionDeny {
		decision["message"] = reply.Message
	}
	_ = json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision":      decision,
	}})
}

// sendHook passes msg on to the daemon at socket, giving up quickly, and
// returns the connection its answer, if any, comes back on.
func sendHook(socket string, msg hookMessage) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(msg); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// runStatusLine is Claude's status line, run by the daemon's own program
// with the daemon's hook socket as args[0]. It passes Claude's state on for
// the session in MAKO_SESSION, without waiting for an answer, and prints
// the status line the user set in Claude's own settings, which mako's
// takes the place of.
func runStatusLine(args []string, stdin io.Reader, out io.Writer) {
	input, err := io.ReadAll(io.LimitReader(stdin, hookLimit))
	if err != nil {
		return
	}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		session := os.Getenv("MAKO_SESSION")
		if len(args) != 1 || session == "" || !json.Valid(input) {
			return
		}
		if conn, err := sendHook(args[0], hookMessage{Session: session, Seq: time.Now().UnixNano(), Kind: statusArg, Input: input}); err == nil {
			_ = conn.Close()
		}
	}()
	if command := userStatusLine(input); command != "" {
		cmd := exec.Command("sh", "-c", command)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), out, os.Stderr
		_ = cmd.Run()
	}
	<-sent
}

// userStatusLine is the status line command Claude's own settings name,
// "" for none: the project's local settings, then its shared ones, then
// the user's, as Claude ranks them.
func userStatusLine(input []byte) string {
	var state struct {
		Cwd       string `json:"cwd"`
		Workspace struct {
			ProjectDir string `json:"project_dir"`
		} `json:"workspace"`
	}
	_ = json.Unmarshal(input, &state)
	project := state.Workspace.ProjectDir
	if project == "" {
		project = state.Cwd
	}
	config := transcript.ConfigRoot("claude", map[string]string{"CLAUDE_CONFIG_DIR": os.Getenv("CLAUDE_CONFIG_DIR"), "HOME": os.Getenv("HOME")})
	var files []string
	if project != "" {
		files = append(files, filepath.Join(project, ".claude", "settings.local.json"), filepath.Join(project, ".claude", "settings.json"))
	}
	if config != "" {
		files = append(files, filepath.Join(config, "settings.json"))
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var settings struct {
			StatusLine *struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"statusLine"`
		}
		if json.Unmarshal(body, &settings) != nil || settings.StatusLine == nil {
			continue
		}
		if settings.StatusLine.Type != "command" {
			return ""
		}
		return settings.StatusLine.Command
	}
	return ""
}

// statusUsage reads what a status line's input says of the session's
// tokens: the model's context window, and the account's limits.
func statusUsage(input []byte) *protocol.Usage {
	type limit struct {
		Used   float64 `json:"used_percentage"`
		Resets int64   `json:"resets_at"`
	}
	var state struct {
		Context struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
		Limits struct {
			FiveHour *limit `json:"five_hour"`
			SevenDay *limit `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &state) != nil {
		return &protocol.Usage{}
	}
	u := &protocol.Usage{Window: state.Context.Size}
	for minutes, l := range map[int64]*limit{protocol.FiveHours: state.Limits.FiveHour, protocol.Week: state.Limits.SevenDay} {
		if l != nil {
			u.SetLimit(&protocol.RateLimit{Minutes: minutes, Used: l.Used, Resets: l.Resets})
		}
	}
	return u
}

// listenHooks serves the hook socket in dir until ctx ends, setting
// hookCommand once it listens; Run calls it before it serves any session.
// The socket sits in the private run directory, so only this user's
// processes reach it.
func (d *Daemon) listenHooks(ctx context.Context, dir string) {
	if d.hookBinary == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.log.Warn("hooks: no run directory", "err", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("hooks-%d.sock", d.port))
	_ = os.Remove(path) // left by a daemon that did not stop cleanly
	ln, err := net.Listen("unix", path)
	if err != nil {
		d.log.Warn("hooks: not listening", "err", err)
		return
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	d.hookCommand = agentspec.JoinArgs([]string{d.hookBinary, hookArg, path})
	d.statusCommand = agentspec.JoinArgs([]string{d.hookBinary, statusArg, path})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serveHook(ctx, conn)
		}
	}()
}

func (d *Daemon) serveHook(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var msg hookMessage
	if err := json.NewDecoder(io.LimitReader(conn, hookLimit+4096)).Decode(&msg); err != nil {
		return
	}
	d.sessionMu.Lock()
	s, ok := d.sessions.get(msg.Session)
	d.sessionMu.Unlock()
	if !ok {
		return
	}
	if msg.Kind == statusArg {
		s.Report(statusUsage(msg.Input))
		return
	}
	native, ok := transcript.Lookup(s.Info().GetAgent())
	if !ok {
		return
	}
	ev, ok := native.ParseHook(msg.Input)
	if !ok {
		return
	}
	switch ev.Event {
	case "SessionStart":
		s.SetStatus(protocol.AgentIdle)
		if err := d.reportConversation(s, msg.Seq, ev); err != nil {
			d.log.Warn("hooks: report dropped", "session", msg.Session, "err", err)
		}
	case "UserPromptSubmit", "PostToolUse":
		s.SetStatus(protocol.AgentWorking)
	case "Notification":
		// Claude asks something of the user; a permission it asks through
		// mako is already an approval.
		if ev.NotificationType == "permission_prompt" || ev.NotificationType == "elicitation_dialog" {
			s.SetStatus(protocol.AgentWaiting)
		}
	case "Stop":
		s.SetStatus(protocol.AgentDone)
	case "SessionEnd":
		s.SetStatus(protocol.AgentIdle)
	case "PermissionRequest":
		_ = conn.SetDeadline(time.Time{})
		reply := d.approvals.ask(ctx, s, ev)
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = json.NewEncoder(conn).Encode(reply)
	}
}
