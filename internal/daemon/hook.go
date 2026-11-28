package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
	"github.com/ironpark/mako/internal/transport"
)

// hookArg, as a program's first argument, runs it as an agent's hook.
const hookArg = "hook"

// statusArg, as a program's first argument, runs it as an agent's status
// line.
const statusArg = "statusline"

// hookProgram is this program, once HookMain has said it can be a hook.
var hookProgram string

// HookMain runs this program as an agent's hook when it was started as one,
// reporting true: main then returns. Called first in main, it also says the
// program can be one, so the daemons it runs give their agents a hook;
// without it they only watch the agents' open files.
func HookMain() bool {
	if len(os.Args) > 1 && os.Args[1] == agentMCPArg {
		if err := runAgentMCP(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "mako agent MCP:", err)
			os.Exit(1)
		}
		return true
	}
	if len(os.Args) > 1 && os.Args[1] == agentMsgArg {
		os.Exit(runAgentMsg(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == agentPluginArg {
		os.Exit(runAgentPlugin(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == agentProjectArg {
		os.Exit(runAgentProject(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == agySidecarArg {
		if err := runAgySidecar(os.Args[2:], os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "mako agy-sidecar:", err)
			os.Exit(1)
		}
		return true
	}
	if len(os.Args) > 1 && os.Args[1] == hookArg {
		runHook(os.Args[2:], os.Stdin, os.Stdout)
		return true
	}
	if len(os.Args) > 1 && os.Args[1] == clipArg {
		os.Exit(runClip(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
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

// hookTokenDir is the folder, beside the hook socket, of the files that
// give each session its hook token, which the hooks, status lines and
// clipboard tools its programs run carry to the socket to be taken as its
// own. A file, only the user's to read, and not the session's environment:
// a process of the same user reads another's environment on macOS even in
// a sandbox, but a sandbox can hide the run directory but for the
// session's own file.
const hookTokenDir = "hook-tokens"

// hookTokenFile is the file of session's hook token, for the hook socket
// in the folder run: named for the session, whatever its ID holds.
func hookTokenFile(run, session string) string {
	sum := sha256.Sum256([]byte(session))
	return filepath.Join(run, hookTokenDir, hex.EncodeToString(sum[:16]))
}

// readHookToken is session's hook token, for the hook socket at socket,
// as the session's hooks read it; "" for none they can read.
func readHookToken(socket, session string) string {
	f, err := os.Open(hookTokenFile(filepath.Dir(socket), session))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 256))
	return strings.TrimSpace(string(b))
}

// hookMessage is what a hook passes on: the session it runs in, its proof
// that it runs there, when it ran, and the agent's own input to it. Kind
// is statusArg for a status line's input, and "" for a hook's.
type hookMessage struct {
	Session string          `json:"session"`
	Token   string          `json:"token,omitempty"`
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind,omitempty"`
	Input   json.RawMessage `json:"input"`
}

// sessionMAC is session's token under secret: what only a holder of the
// secret, the daemon, makes, and what differs from session to session.
func sessionMAC(secret []byte, session string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(session))
	return hex.EncodeToString(mac.Sum(nil))
}

// hookToken is what session's hook connections carry, given to it in its
// hookTokenFile: of this daemon's secret, made new each time it starts, so
// a process that cannot read that file, as a sandboxed one of another
// session cannot, cannot report for the session.
func (d *Daemon) hookToken(session string) string {
	return sessionMAC(d.hookSecret, session)
}

// giveHookToken writes session's hook token to its file, for the hook
// socket in d.hookRun, before the session starts. The token stays the same
// for as long as the daemon runs, so a session started again finds it.
func (d *Daemon) giveHookToken(session string) error {
	path := hookTokenFile(d.hookRun, session)
	f, err := os.CreateTemp(filepath.Dir(path), ".token-*") // only the user's to read
	if err != nil {
		return err
	}
	_, err = f.WriteString(d.hookToken(session))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// hookProven reports whether msg carries its session's own token.
func (d *Daemon) hookProven(msg hookMessage) bool {
	return msg.Session != "" && msg.Token != "" && len(d.hookSecret) > 0 &&
		hmac.Equal([]byte(msg.Token), []byte(d.hookToken(msg.Session)))
}

// hookRefusalEvery is how often a refused hook connection is logged at
// most, so a process that keeps trying does not fill the log.
const hookRefusalEvery = time.Minute

// hookRefusals logs the hook connections refused, at most once every
// hookRefusalEvery, with how many went unlogged since the last.
type hookRefusals struct {
	mu       sync.Mutex
	last     time.Time
	unlogged int
}

func (r *hookRefusals) note(log *slog.Logger, msg hookMessage) {
	r.mu.Lock()
	now := time.Now()
	if !r.last.IsZero() && now.Sub(r.last) < hookRefusalEvery {
		r.unlogged++
		r.mu.Unlock()
		return
	}
	unlogged := r.unlogged
	r.last, r.unlogged = now, 0
	r.mu.Unlock()
	why := "wrong token"
	if msg.Token == "" {
		why = "no token"
	}
	// What the sender chose goes to the log only in part.
	log.Warn("hooks: refused a connection without its session's token",
		"session", clipped(msg.Session), "kind", clipped(msg.Kind), "why", why, "unlogged", unlogged)
}

// clipped is s, cut short when it is long.
func clipped(s string) string {
	const most = 64
	if len(s) <= most {
		return s
	}
	return strings.ToValidUTF8(s[:most], "") + "..."
}

// hookReply answers a hook that asks for an approval: allow or deny, or ""
// to leave the question to the agent's own prompt.
type hookReply struct {
	Decision string `json:"decision,omitempty"`
	Message  string `json:"message,omitempty"`
	// Output is the decision as the agent reads it, which the hook prints;
	// a daemon before drivers sent the decision alone.
	Output json.RawMessage `json:"output,omitempty"`
}

// runHook is an agent's hook, run by the daemon's own program with the
// daemon's hook socket as args[0]. It passes the agent's input on for the
// session in MAKO_SESSION, with the token in its hookTokenFile that proves
// it runs there. The agent waits on it, so it gives up quickly,
// and it never fails: the daemon falls back to watching the agent's open
// files. An approval waits for the daemon's answer, which it writes to out
// as the agent's decision. An agent whose input does not name its event,
// as agy's does not, names it in args[1].
func runHook(args []string, stdin io.Reader, out io.Writer) {
	seq := time.Now().UnixNano() // when the agent ran it, not when it arrives
	session := os.Getenv("MAKO_SESSION")
	if len(args) < 1 || len(args) > 2 || session == "" {
		// An agent that hooks mako for good runs it outside sessions too,
		// and may not like its input left unread.
		_, _ = io.Copy(io.Discard, io.LimitReader(stdin, hookLimit))
		return
	}
	input, err := io.ReadAll(io.LimitReader(stdin, hookLimit))
	if err != nil || !json.Valid(input) {
		return
	}
	if len(args) == 2 {
		if input, err = namedEvent(input, args[1]); err != nil {
			return
		}
	}
	var ev struct {
		Event string `json:"hook_event_name"`
		Seq   string `json:"mako_hook_seq"`
	}
	_ = json.Unmarshal(input, &ev)
	// Extensions launch hook processes asynchronously. Preserve the event's
	// production order even if those processes start in a different order.
	if produced, err := strconv.ParseInt(ev.Seq, 10, 64); err == nil && produced > 0 {
		seq = produced
	}
	conn, err := sendHook(args[0], hookMessage{Session: session, Token: readHookToken(args[0], session), Seq: seq, Input: input})
	if err != nil {
		return
	}
	defer conn.Close()
	if startsTurn(ev.Event) {
		// The turn waits for its checkpoint, which the daemon captures
		// before it hangs up.
		_ = conn.SetDeadline(time.Now().Add(turnWait))
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	if ev.Event != "PermissionRequest" {
		return
	}
	// The agent's own timeout bounds the wait.
	_ = conn.SetDeadline(time.Time{})
	var reply hookReply
	if json.NewDecoder(conn).Decode(&reply) != nil || reply.Decision == "" {
		return
	}
	if len(reply.Output) == 0 {
		reply.Output = agent.PermissionReply(reply.Decision, reply.Message)
	}
	_, _ = out.Write(reply.Output)
}

// namedEvent is input with the event named in it, as the agents that name
// it themselves do.
func namedEvent(input []byte, event string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	fields["hook_event_name"], _ = json.Marshal(event)
	return json.Marshal(fields)
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

// runStatusLine is an agent's status line, run by the daemon's own program
// with the daemon's hook socket as args[0] and the agent as args[1], Claude
// when a launch before drivers left it out. It passes the agent's state on
// for the session in MAKO_SESSION, without waiting for an answer, and
// prints the status line the user set in the agent's own settings, which
// mako's takes the place of.
func runStatusLine(args []string, stdin io.Reader, out io.Writer) {
	name := "claude"
	if len(args) > 1 {
		name, args = args[1], args[:1]
	}
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
		if conn, err := sendHook(args[0], hookMessage{Session: session, Token: readHookToken(args[0], session), Seq: time.Now().UnixNano(), Kind: statusArg, Input: input}); err == nil {
			_ = conn.Close()
		}
	}()
	if command := userStatusLine(name, input); command != "" {
		cmd := shellCommand(command)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), out, os.Stderr
		_ = safeexec.Run(cmd)
	}
	<-sent
}

// userStatusLine is the status line the user set in the agent's own
// settings, "" for none.
func userStatusLine(agentName string, input []byte) string {
	line, ok := agents.As[agent.StatusLine](agentName)
	if !ok {
		return ""
	}
	return line.UserStatusLine(input)
}

// listenHooks serves the hook socket in dir until ctx ends, setting
// hookCommand once it listens; Run calls it before it serves any session.
// The socket sits in the private run directory, so only this user's
// processes reach it; of those, serveHook takes a connection only as the
// session whose token it carries.
func (d *Daemon) listenHooks(ctx context.Context, dir string) {
	if d.hookBinary == "" {
		return
	}
	ln, path, err := listenUnix(dir, "hooks.sock")
	if err != nil {
		d.log.Warn("hooks: not listening", "err", err)
		return
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	// The tokens a daemon that ran before gave are of its secret, which
	// is gone: no session can use them.
	tokens := filepath.Join(dir, hookTokenDir)
	_ = os.RemoveAll(tokens)
	if err := os.Mkdir(tokens, 0o700); err != nil {
		d.log.Warn("hooks: no folder for the sessions' tokens", "err", err)
		_ = ln.Close()
		return
	}
	d.hookRun = dir
	d.hookCommand = agentspec.CommandLine([]string{d.hookBinary, hookArg, path})
	d.statusCommand = agentspec.CommandLine([]string{d.hookBinary, statusArg, path})
	if d.clipTools, err = writeClipTools(dir, d.hookBinary, path); err != nil {
		d.log.Warn("hooks: no clipboard tools", "err", err)
	}
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

// listenUnix listens on the socket name in dir, the private run
// directory, made if it is not there, in place of one a daemon that did
// not stop cleanly left.
func listenUnix(dir, name string) (net.Listener, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, name)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, path, err
	}
	return transport.OwnListener(ln, path), path, nil
}

func (d *Daemon) serveHook(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var msg hookMessage
	if err := json.NewDecoder(io.LimitReader(conn, hookLimit+4096)).Decode(&msg); err != nil {
		return
	}
	// Any process of the user's reaches the socket: only one of the
	// session's own, given its token, speaks for it.
	if !d.hookProven(msg) {
		d.hookRefusals.note(d.log, msg)
		return
	}
	s, ok := d.sessions.get(msg.Session)
	if !ok {
		return
	}
	if msg.Kind == clipArg {
		var text string
		if json.Unmarshal(msg.Input, &text) == nil && text != "" {
			s.Copy(text)
		}
		return
	}
	name := s.Agent()
	if msg.Kind == statusArg {
		if line, ok := agents.As[agent.StatusLine](name); ok {
			u := line.Status(msg.Input)
			// agy's statusLine reports cycle_mode ("auto", "accept-edits", "plan")
			// even when --dangerously-skip-permissions is active for the session.
			// Surface "bypassPermissions" unless the session is explicitly in plan mode.
			if name == "agy" && agy.HasYoloFlag(s.Command()) {
				mode := u.GetPermissionMode()
				if mode != "plan" && (mode != "" || s.Reported().GetPermissionMode() != "plan") {
					if u == nil {
						u = &protocol.Usage{}
					}
					u.PermissionMode = "bypassPermissions"
				}
			}
			s.Report(u)
		}
		if events, ok := agents.As[agent.StatusLineEvents](name); ok {
			if ev, ok := events.StatusHookEvent(msg.Input); ok {
				if isSubAgentEvent(name, s.Info().GetConversationId(), s.TranscriptPath(), ev) {
					return
				}
				if ev.SessionID != "" && ev.SessionID != s.Info().GetConversationId() {
					if err := d.reportConversation(s, msg.Seq, ev); err != nil {
						d.log.Warn("statusline: conversation report dropped", "session", msg.Session, "err", err)
					}
				}
				switch ev.Kind {
				case agent.Waiting:
					s.ReportToolStatus(protocol.AgentWaiting, msg.Seq)
				case agent.Prompted:
					// Periodic statusLine heartbeats while a tool runs must not
					// clear an active in-app approval (e.g. save_template, layout).
					s.ReportToolStatus(protocol.AgentWorking, msg.Seq)
				case agent.Idle:
					d.turns.end(s.ID(), msg.Seq)
					// Clear waiting state once a terminal confirmation finishes,
					// without clobbering an active in-app approval or AgentDone.
					if s.Info().GetStatus() == protocol.AgentWaiting {
						s.ReportToolStatus(protocol.AgentIdle, msg.Seq)
					}
				}
			}
		}
		return
	}
	hooks, ok := agents.As[agent.Hooks](name)
	if !ok {
		return
	}
	ev, ok := hooks.Event(msg.Input)
	if !ok {
		return
	}
	if parentID := s.Info().GetConversationId(); isSubAgentEvent(name, parentID, s.TranscriptPath(), ev) {
		if ev.Kind == agent.Started || ev.Kind == agent.Stopped {
			s.ReportSubagent(parentID, ev.SessionID, ev.Kind == agent.Started, msg.Seq)
		}
		return
	}
	switch ev.Kind {
	case agent.Started, agent.Stopped, agent.Ended, agent.Idle:
		d.turns.end(s.ID(), msg.Seq)
	}
	switch ev.Kind {
	case agent.Started:
		if !s.ReportStatus(protocol.AgentIdle, msg.Seq) {
			return
		}
		s.ClearAuthError(msg.Seq)
		if err := d.reportConversation(s, msg.Seq, ev); err != nil {
			d.log.Warn("hooks: report dropped", "session", msg.Session, "err", err)
		}
	case agent.ToolDone:
		s.ReportToolDone(msg.Seq)
	case agent.Prompted:
		if !s.ReportStatus(protocol.AgentWorking, msg.Seq) {
			// Codex app-server turn/started or agy statusLine heartbeats can
			// advance statusSeq in-process before the external prompt hook
			// connects. Still capture the turn checkpoint if the session is
			// actively working rather than exited or from a stale ended turn.
			if info := s.Info(); info.GetState() != protocol.StateExited && info.GetStatus() == protocol.AgentWorking {
				d.checkpointTurn(ctx, s, msg.Seq, ev)
			}
			return
		}
		// Only a prompt of this turn makes the suggestion stale, not a
		// late hook of an earlier one.
		d.live.clearSuggestion(msg.Session)
		// A conversation whose start went unheard, as a resumed one's may,
		// is told by its prompts.
		if ev.SessionID != "" && ev.SessionID != s.Info().GetConversationId() {
			if err := d.reportConversation(s, msg.Seq, ev); err != nil {
				d.log.Warn("hooks: report dropped", "session", msg.Session, "err", err)
			}
		}
		// Last, as the hook waits for it: the turn waits on the hook.
		d.checkpointTurn(ctx, s, msg.Seq, ev)
	case agent.Compacting:
		s.ReportToolStatus(protocol.AgentCompacting, msg.Seq)
	case agent.Waiting:
		// Claude says it waits a few seconds into the approval its hook
		// still holds, its own dialog shown beside it: the approval, which
		// waits already, stands.
		s.ReportToolStatus(protocol.AgentWaiting, msg.Seq)
	case agent.Stopped:
		d.refreshActivity(s)
		s.ReportStatus(protocol.AgentDone, msg.Seq)
	case agent.Ended, agent.Idle:
		if ev.Kind == agent.Ended || ev.AuthError != nil {
			d.live.clearSuggestion(msg.Session)
		}
		s.ReportStatus(protocol.AgentIdle, msg.Seq)
		if ev.AuthError != nil {
			s.ReportAuthError(ev.AuthError, msg.Seq)
		}
	case agent.ChildStarted, agent.ChildStopped:
		s.ReportSubagent(ev.SessionID, ev.AgentID, ev.Kind == agent.ChildStarted, msg.Seq)
	case agent.Approval:
		if !s.ReportStatus(protocol.AgentWaiting, msg.Seq) {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		// The agent gives up on its hook when the approval is answered in
		// its terminal, it is interrupted or the hook times out: the hook
		// exits, its end of the connection closes, and the approval is
		// withdrawn from the window with it.
		asking, withdraw := context.WithCancel(ctx)
		defer withdraw()
		go func() {
			_, _ = conn.Read(make([]byte, 1))
			withdraw()
		}()
		reply := d.approvals.ask(asking, s, name, ev, msg.Seq)
		if reply.Decision != "" && len(reply.Output) == 0 {
			reply.Output = hooks.Reply(reply.Decision, reply.Message)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = json.NewEncoder(conn).Encode(reply)
	}
}

// isSubAgentEvent reports whether ev came from a subagent/worker of the
// session's currently bound parent conversation rather than the parent itself.
func isSubAgentEvent(agentName, parentID, parentLog string, ev agent.Event) bool {
	if parentID == "" || ev.SessionID == "" || ev.SessionID == parentID {
		return false
	}
	filter, ok := agents.As[agent.SubAgentHooks](agentName)
	return ok && filter.IsSubAgent(parentID, parentLog, ev.SessionID, ev.Transcript)
}
