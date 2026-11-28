package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// local reports whether nodeID addresses this daemon.
func (d *Daemon) local(nodeID string) bool {
	return nodeID == "" || nodeID == d.config().NodeID
}

func (d *Daemon) handleListSessions(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	req, _ := protocol.Body[*protocol.ListSessions](env) // older front-ends send nothing
	if req.GetApprovals() {
		d.approvals.watch(!fromPeer)
	}
	sessions := d.listSessions()
	// A peer asking is already fanning out itself; answering with local
	// sessions only keeps a mesh cycle from recursing.
	if fromPeer {
		return stream.Send(protocol.TypeSessions, env.GetId(), &protocol.Sessions{Sessions: sessions})
	}
	// A front-end asking a single daemon for sessions expects the whole mesh,
	// so fold in what the peers that let this daemon in sent of the nodes
	// it reaches through them, plus any tunneled SSH daemons.
	sessions = append(sessions, d.meshSessions(d.routing())...)
	if d.ssh != nil {
		sessions = appendUnlisted(sessions, d.ssh.daemonSessions())
	}
	return stream.Send(protocol.TypeSessions, env.GetId(), &protocol.Sessions{Sessions: sessions})
}

func (d *Daemon) handleOpenSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.OpenSession) (*protocol.SessionInfo, error) {
		var (
			s   *session.Session
			err error
		)
		if d.isDirectSSH(r.GetNodeId()) {
			s, err = d.openDirectSSHSession(ctx, r)
		} else {
			s, err = d.openSession(ctx, r)
		}
		if err != nil {
			return nil, err
		}
		// As the listing has it, which the front-end places it by at once.
		return d.withHead(s.Info()), nil
	})
}

func (d *Daemon) handleCloseSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeExited, func(r *protocol.CloseSession) (*protocol.Exited, error) {
		if err := d.closeSession(r.GetSessionId()); err != nil {
			return nil, err
		}
		return &protocol.Exited{SessionId: r.GetSessionId()}, nil
	})
}

func (d *Daemon) handleAnswerApproval(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.AnswerApproval) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		// A template is saved by the window that allows it, on whichever
		// node that is.
		if d.templateAsks.owns(r.GetApprovalId()) {
			if err := d.templateAsks.answer(r.GetApprovalId(), templateAnswer{decision: r.GetDecision(), option: r.GetOption(), message: r.GetMessage()}); err != nil {
				return nil, err
			}
			return s.Info(), nil
		}
		// A change of the window layout is shown, and so answered, on
		// its own node alone.
		if d.layouts.owns(r.GetApprovalId()) {
			if fromPeer {
				return nil, fmt.Errorf("a layout change is answered on its own node")
			}
			if err := d.layouts.answer(r.GetApprovalId(), layoutAnswer{decision: r.GetDecision(), option: r.GetOption(), message: r.GetMessage()}); err != nil {
				return nil, err
			}
			return s.Info(), nil
		}
		if err := d.approvals.answer(r.GetSessionId(), r); err != nil {
			return nil, err
		}
		return s.Info(), nil
	})
}

func (d *Daemon) handleSend(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.Send) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		if d.abortNativeTurn(ctx, s, r.GetText(), r.GetKeys()) {
			return s.Info(), nil
		}
		if d.deliverNativeUserPrompt(ctx, s, r.GetText(), r.GetKeys()) {
			if strings.TrimSpace(r.GetText()) != "" {
				s.ClearAuthErrorNow()
			}
			return s.Info(), nil
		}
		if err := s.Send(r.GetText(), r.GetKeys()); err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.GetText()) != "" {
			s.ClearAuthErrorNow()
		}
		return s.Info(), nil
	})
}

// isInterruptKeys reports whether keys is a bare Escape interrupt sequence
// (plain ESC or Kitty CSI-u Escape).
func isInterruptKeys(keys []byte) bool {
	return bytes.Equal(keys, []byte("\x1b")) ||
		bytes.Equal(keys, []byte("\x1b[27u")) ||
		bytes.Equal(keys, []byte("\x1b[27;1u"))
}

// canAbortTurnNatively reports whether a Send request is a bare interrupt
// request safe to route through an agent's native turn-abort controller.
func canAbortTurnNatively(s *session.Session, text string, keys []byte) bool {
	if text != "" || !isInterruptKeys(keys) {
		return false
	}
	info := s.Info()
	if info.GetState() != protocol.StateRunning || info.GetDialog() != nil {
		return false
	}
	// If the agent is waiting on an interactive terminal prompt without an
	// out-of-band approval card (e.g. request_user_input), let Escape go to
	// the PTY so it cancels the prompt cleanly.
	if info.GetStatus() == protocol.AgentWaiting && info.GetApproval() == nil {
		return false
	}
	return true
}

func (d *Daemon) abortNativeTurn(ctx context.Context, s *session.Session, text string, keys []byte) bool {
	if !canAbortTurnNatively(s, text, keys) {
		return false
	}
	switch s.Info().GetAgent() {
	case "codex":
		return d.abortViaCodexBridge(ctx, s)
	default:
		return false
	}
}

// isSubmitKeys reports whether keys is a plain Enter submission from the
// composer (standard CR/LF or Kitty CSI-u Enter).
func isSubmitKeys(keys []byte) bool {
	return bytes.Equal(keys, []byte("\r")) ||
		bytes.Equal(keys, []byte("\n")) ||
		bytes.Equal(keys, []byte("\x1b[13u")) ||
		bytes.Equal(keys, []byte("\x1b[13;1u"))
}

// canDeliverUserPromptNatively reports whether a Send request is a regular
// composer user prompt safe to route through an agent's native prompt API
// instead of typing into the PTY. Slash commands, shell escapes, bare keys,
// paste-only writes, and interactive TUI dialogs stay on the PTY.
func canDeliverUserPromptNatively(s *session.Session, text string, keys []byte) bool {
	if !isSubmitKeys(keys) {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.Contains(text, "\x1b[201~") {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") {
		return false
	}
	info := s.Info()
	if info.GetState() != protocol.StateRunning || info.GetDialog() != nil {
		return false
	}
	// If the agent is waiting on an interactive terminal prompt (not an
	// approval card handled out-of-band), let the PTY receive the input.
	if info.GetStatus() == protocol.AgentWaiting && info.GetApproval() == nil {
		return false
	}
	return true
}

// deliverNativeUserPrompt submits a GUI composer prompt through the agent's
// native user-turn API when available (Codex app-server thread/queue/add),
// returning false to fall back to PTY.
//
// Claude Code stays on PTY for GUI user prompts because its Mod API
// ($.prompt.submit) unconditionally wraps messages with a "The mako-live
// plugin sent a message: ... in the user's place" envelope intended for
// plugin/inter-agent messages, not direct human turns.
// Antigravity (agy) stays on PTY for GUI user prompts because its agentapi
// send-message delivers async notification messages rather than normal user
// turns and requires an already-established conversation ID.
func (d *Daemon) deliverNativeUserPrompt(ctx context.Context, s *session.Session, text string, keys []byte) bool {
	if !canDeliverUserPromptNatively(s, text, keys) {
		return false
	}
	switch s.Info().GetAgent() {
	case "codex":
		return d.deliverViaCodexBridge(ctx, s.ID(), text)
	default:
		return false
	}
}

// handleAnswerQuestion answers the questions a session's agent shows in
// its terminal, by typing the answers there, as its driver says to, once
// its screen shows them asked.
func (d *Daemon) handleAnswerQuestion(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.AnswerQuestion) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		keyed, ok := agents.As[agent.KeyedAnswers](s.Agent())
		if !ok {
			return nil, errors.New("this agent's questions are answered in its terminal")
		}
		if !agent.Answered(r.GetQuestions(), r.GetAnswers()) {
			return nil, errors.New("every question needs an answer")
		}
		if !s.Shows(keyed.Asking) {
			return nil, errors.New("the agent is not asking now")
		}
		if err := s.Type(keyed.AnswerKeys(r.GetQuestions(), r.GetAnswers())); err != nil {
			return nil, err
		}
		return s.Info(), nil
	})
}

// handleAnswerDialog picks an option of the dialog a session's agent shows
// in its terminal, as the user would there.
func (d *Daemon) handleAnswerDialog(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.AnswerDialog) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		if err := s.AnswerDialog(r.GetDialogId(), int(r.GetOption())); err != nil {
			return nil, err
		}
		return s.Info(), nil
	})
}

// handleSeeSession records what of a session's attention the user has
// looked at, which every front-end then shows as seen.
func (d *Daemon) handleSeeSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.SeeSession) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		s.See(r.GetSeq())
		return s.Info(), nil
	})
}

// handleConfigureAgent sets a session's agent's mode, model, effort, or fast
// mode as its driver or native bridge supports.
func (d *Daemon) handleConfigureAgent(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.ConfigureAgent) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		if r.FastMode != nil {
			if b := d.codexBridge(s.ID()); b != nil {
				if err := b.setFastMode(ctx, s, r.GetFastMode()); err != nil {
					return nil, err
				}
			} else if err := s.ConfigureFastMode(r.GetFastMode()); err != nil {
				return nil, err
			}
		}
		if r.GetModel() != "" {
			if err := d.ensureDynamicModelsForConfigure(ctx, s, r.GetModel()); err != nil {
				return nil, err
			}
		}
		if r.GetMode() != "" || r.GetModel() != "" || r.GetEffort() != "" {
			if err := s.Configure(r.GetMode(), r.GetModel(), r.GetEffort()); err != nil {
				return nil, err
			}
		}
		return s.Info(), nil
	})
}

func (d *Daemon) dynamicModelCatalog(agentName string) (agent.ModelCatalog, bool) {
	entry, ok := agents.Lookup(agentName)
	if !ok {
		return nil, false
	}
	m, ok := entry.Driver.(agent.Models)
	if !ok || len(m.Models()) > 0 {
		return nil, false
	}
	cat, ok := entry.Driver.(agent.ModelCatalog)
	return cat, ok
}

func catalogModelIDsAndVariants(models []agent.ModelInfo) (ids, variants []string) {
	ids = make([]string, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		ids = append(ids, m.ID)
		for _, tier := range m.Efforts {
			if tier != "" {
				variants = append(variants, m.ID+"-"+tier)
			}
		}
	}
	return ids, variants
}

func catalogContainsModel(models []agent.ModelInfo, want string) bool {
	if want == "" {
		return false
	}
	for _, m := range models {
		if m.ID == want {
			return true
		}
		for _, tier := range m.Efforts {
			if tier != "" && m.ID+"-"+tier == want {
				return true
			}
		}
	}
	return false
}

func (d *Daemon) applyDynamicModels(s *session.Session, models []agent.ModelInfo) {
	ids, variants := catalogModelIDsAndVariants(models)
	s.SetDynamicModelVariants(variants)
	s.SetDynamicModels(ids)
}

// seedDynamicModels populates SessionInfo.Controls.Models for agents whose
// models come from a dynamic ModelCatalog (such as agy). When the catalog
// cache is warm it finishes synchronously before openSession returns; when
// cold it continues in the background without blocking session startup.
func (d *Daemon) seedDynamicModels(s *session.Session) {
	if s == nil {
		return
	}
	cat, ok := d.dynamicModelCatalog(s.Agent())
	if !ok {
		return
	}
	callEnv := agentspec.IsolatedHomeEnvForAgent(s.Agent(), d.isolateHome(), d.agents())
	parentCtx := d.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(parentCtx, 12*time.Second)
		defer cancel()
		models, err := cat.Catalog(ctx, callEnv, false)
		if err != nil || len(models) == 0 {
			return
		}
		d.applyDynamicModels(s, models)
	}()
	select {
	case <-done:
	case <-time.After(25 * time.Millisecond):
	}
}

// ensureDynamicModelsForConfigure loads (and refreshes if needed) the dynamic
// model catalog before Configure validates wantModel against the session.
func (d *Daemon) ensureDynamicModelsForConfigure(ctx context.Context, s *session.Session, wantModel string) error {
	cat, ok := d.dynamicModelCatalog(s.Agent())
	if !ok {
		return nil
	}
	callEnv := agentspec.IsolatedHomeEnvForAgent(s.Agent(), d.isolateHome(), d.agents())
	models, err := cat.Catalog(ctx, callEnv, false)
	if err != nil || !catalogContainsModel(models, wantModel) {
		if refreshed, rerr := cat.Catalog(ctx, callEnv, true); rerr == nil && len(refreshed) > 0 {
			models, err = refreshed, nil
		} else if err == nil && rerr != nil && len(models) == 0 {
			err = rerr
		}
	}
	if len(models) > 0 {
		d.applyDynamicModels(s, models)
		return nil
	}
	if err != nil && len(s.Info().GetControls().GetModels()) == 0 {
		return fmt.Errorf("load %s models: %w", s.Agent(), err)
	}
	return nil
}

// maxSessionName caps a session's name, which every listing carries.
const maxSessionName = 200

func (d *Daemon) handleRenameSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.RenameSession) (*protocol.SessionInfo, error) {
		name := strings.Join(strings.Fields(r.GetName()), " ") // one line
		if len(name) > maxSessionName {
			return nil, fmt.Errorf("a session name is at most %d bytes", maxSessionName)
		}
		return d.renameSession(r.GetSessionId(), name)
	})
}

// handleSleepSession stops the program of a session whose agent rests,
// keeping the session to resume when it is next attached.
func (d *Daemon) handleSleepSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.SleepSession) (*protocol.SessionInfo, error) {
		info, err := d.sleepSession(r.GetSessionId())
		if err != nil {
			return nil, err
		}
		return d.withHead(info), nil
	})
}

// handleListCommands lists the slash commands a session's agent takes, as
// its driver finds them on this node and its log names them.
func (d *Daemon) handleListCommands(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeCommands, func(r *protocol.ListCommands) (*protocol.Commands, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %s", r.GetSessionId())
		}
		info := s.Info()
		out := &protocol.Commands{}
		commands, ok := agents.As[agent.Commands](info.GetAgent())
		if !ok {
			return out, nil
		}
		list := commands.Commands(info.GetProject(), s.TranscriptEnv())
		// Those the agent brings along, which its log alone names, read
		// up to now.
		d.refreshActivity(s)
		list = withLogged(list, d.loggedCommands.of(s.ID(), info.GetAgent()))
		for _, c := range list {
			out.Commands = append(out.Commands, &protocol.Command{Name: c.Name, Description: c.Description, Args: c.Args, Source: c.Source})
		}
		return out, nil
	})
}

// handleAttach turns the stream into a live session pipe: session output flows
// out, and input frames from the front-end flow into the PTY.
func (d *Daemon) handleAttach(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.Attach](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) && !d.isDirectSSH(req.GetNodeId()) {
		if !stream.Packs() {
			// The relay unpacks the peer's frames for the front-end, so
			// the peer may pack them as one stream.
			req.PackStream = true
		}
		return d.relayToPeer(ctx, protocol.TypeAttach, req, stream)
	}
	return d.attachLocal(ctx, req, attachWire{stream: stream, id: env.GetId(), sessionID: req.GetSessionId()})
}

// attachLocal pipes a session of this daemon through w.
func (d *Daemon) attachLocal(ctx context.Context, req *protocol.Attach, w attachWire) (err error) {
	s, err := d.attachSession(ctx, req.GetSessionId())
	if err != nil {
		return err
	}
	if req.GetPackStream() {
		w.stream.PackStream()
	}
	sub := s.Subscribe()
	defer func() { sub.Close() }()

	resync := func() error {
		snap, err := s.Snapshot(true)
		if err != nil {
			return err
		}
		return sendSnapshot(w, snap)
	}
	if req.GetReplay() {
		if err := resync(); err != nil {
			return err
		}
	}

	served := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Inbound frames on an attached stream are input and resize only.
	// readInput is the stream's one reader until the front-end hangs up,
	// so the attach waits for it: serveStream reads the stream next. An
	// error is told the front-end first, as it hangs up once it hears of
	// the attach's end; a daemon stopping hangs up itself.
	input := make(chan struct{})
	go func() {
		defer close(input)
		defer cancel()
		w.readInput(s)
	}()
	defer func() {
		switch {
		case served.Err() != nil:
			_ = w.stream.Hangup()
		case err != nil && !isClosed(err):
			err = protocol.WriteError(w.stream, w.id, err)
		}
		<-input
	}()

	for {
		chunk, ok := sub.Next(ctx)
		if !ok {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			info := s.Info()
			if info.GetState() != protocol.StateExited {
				// The session dropped us for falling behind. Resubscribe
				// and resync from the current screen instead of
				// reporting an exit.
				sub.Close()
				sub = s.Subscribe()
				if err := resync(); err != nil {
					return err
				}
				continue
			}
			return w.exited(&protocol.Exited{SessionId: req.GetSessionId(), ExitCode: info.GetExitCode()})
		}
		if err := w.output(chunk); err != nil {
			if err == io.EOF || isClosed(err) {
				return nil
			}
			return err
		}
	}
}

// snapshotChunk is how much of a replay's screen text one piece of output
// carries, so a long scrollback neither exceeds the frame limit nor arrives
// as one piece.
const snapshotChunk = 256 << 10

// sendSnapshot sends snap without its text, then the screen as output the
// front-end feeds to its terminal like live output.
func sendSnapshot(w attachWire, snap *protocol.Snapshot) error {
	text := snap.GetText()
	snap.Text = ""
	if err := w.snapshot(snap); err != nil {
		return err
	}
	for len(text) > 0 {
		n := min(len(text), snapshotChunk)
		if err := w.output([]byte(text[:n])); err != nil {
			return err
		}
		text = text[n:]
	}
	return nil
}

// attachWire is an attached stream's messages: the session's screen,
// output and end toward the front-end, its input and resizes back.
type attachWire struct {
	stream    *transport.Stream
	id        string
	sessionID string
}

func (w attachWire) snapshot(snap *protocol.Snapshot) error {
	return w.stream.Send(protocol.TypeSnapshot, w.id, snap)
}

func (w attachWire) output(p []byte) error {
	return w.stream.Send(protocol.TypeOutput, w.id, &protocol.Output{SessionId: w.sessionID, Data: p})
}

func (w attachWire) exited(ex *protocol.Exited) error {
	return w.stream.Send(protocol.TypeExited, w.id, ex)
}

// readInput feeds the front-end's input and resizes to s until the stream
// ends.
func (w attachWire) readInput(s *session.Session) {
	for {
		in, err := w.stream.Recv()
		if err != nil {
			return
		}
		switch in.Type() {
		case protocol.TypeInput:
			_, _ = s.Write(in.GetInput().GetData())
		case protocol.TypeResize:
			_ = resize(s, in.GetResize())
		}
	}
}

// resize applies a Resize to s: the colours it carries, and its size
// unless it carries colours alone.
func resize(s *session.Session, r *protocol.Resize) error {
	s.SetColors(r.GetColors())
	if r.GetColors() != nil && r.GetCols() == 0 && r.GetRows() == 0 {
		return nil
	}
	return s.ResizeCells(uint16(r.GetCols()), uint16(r.GetRows()), uint16(r.GetCellWidth()), uint16(r.GetCellHeight()))
}
