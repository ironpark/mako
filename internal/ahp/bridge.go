// Package ahp serves a node's sessions to Agent Host Protocol clients
// (docs/ahp.md). It follows each session's host through the daemon's own
// socket, as any front-end does (subscribe_session), and mirrors it into
// an AHP host of gosuda/go-ahp: a session there for each mako session,
// with its conversation as turns. What a client does there goes back to
// the session as a session_action. AHP types do not leave this package.
package ahp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/go-ahp/host"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// ClientID names the bridge to the session hosts it follows: its actions
// carry it, and front-ends show it as ClientName. It is this process's
// own: a host binds a client_id to where its requests come from, which
// for the bridge of each node is another place.
var ClientID = "ahp-" + randomID()

const ClientName = "AHP"

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// listEvery is how often the bridge lists the sessions, to add the new
// and remove the ended.
const listEvery = 2 * time.Second

// Options configure a Bridge.
type Options struct {
	// Dial connects to the daemon, as a front-end does.
	Dial func(ctx context.Context) (*client.Client, error)
	// EpochFile keeps the AHP serverSeq epoch across restarts, "" for one
	// kept in memory.
	EpochFile string
	// Version is the daemon's, for initialize.
	Version string
	Log     *slog.Logger
}

// Bridge is an AHP host mirroring the sessions a daemon reaches.
type Bridge struct {
	o    Options
	log  *slog.Logger
	host *host.Host

	mu       sync.Mutex
	c        *client.Client
	sessions map[string]*mirror // by AHP session URI
	agents   map[string]bool
	seq      uint64 // the bridge's session_action client_seq
}

// New makes a Bridge. Run starts its mirroring.
func New(o Options) (*Bridge, error) {
	if o.Dial == nil {
		return nil, errors.New("ahp: no dial")
	}
	b := &Bridge{o: o, log: o.Log, sessions: map[string]*mirror{}, agents: map[string]bool{}, seq: uint64(time.Now().UnixNano())}
	if b.log == nil {
		b.log = slog.New(slog.DiscardHandler)
	}
	cfg := host.Config{
		Backend:    backend{b},
		Agents:     []ahptypes.AgentInfo{agentInfo("mako")},
		ServerInfo: &ahptypes.Implementation{Name: "makod", Version: &o.Version},
		Logger:     b.log,
	}
	if o.EpochFile != "" {
		cfg.EpochStore = &host.FileEpochStore{Path: o.EpochFile}
	}
	h, err := host.New(cfg)
	if err != nil {
		return nil, err
	}
	b.host = h
	return b, nil
}

// ServeConn serves one AHP connection.
func (b *Bridge) ServeConn(ctx context.Context, t ahpTransport) error {
	return b.host.ServeConn(ctx, t)
}

// Close stops the host and every mirror.
func (b *Bridge) Close() error {
	b.mu.Lock()
	for _, m := range b.sessions {
		m.stop()
	}
	b.sessions = map[string]*mirror{}
	c := b.c
	b.c = nil
	b.mu.Unlock()
	if c != nil {
		c.Close()
	}
	return b.host.Close()
}

// Run mirrors the sessions until ctx ends.
func (b *Bridge) Run(ctx context.Context) error {
	t := time.NewTicker(listEvery)
	defer t.Stop()
	for {
		if err := b.reconcile(ctx); err != nil && ctx.Err() == nil {
			b.log.Debug("ahp: sessions not listed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// client is the bridge's connection to the daemon, dialed again once lost.
func (b *Bridge) client(ctx context.Context) (*client.Client, error) {
	b.mu.Lock()
	c := b.c
	b.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c, err := b.o.Dial(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.c == nil {
		b.c = c
	} else {
		c.Close()
		c = b.c
	}
	b.mu.Unlock()
	return c, nil
}

// dropClient forgets a connection that failed, for the next call to dial
// again.
func (b *Bridge) dropClient(c *client.Client) {
	b.mu.Lock()
	if b.c == c {
		b.c = nil
	}
	b.mu.Unlock()
	c.Close()
}

// mirrored reports whether a session is one AHP clients are shown: an
// agent's, running.
func mirrored(s *protocol.SessionInfo) bool {
	switch s.GetState() {
	case protocol.StateRunning, protocol.StateStarting:
	default:
		return false
	}
	return s.GetAgent() != "" && s.GetAgent() != "shell"
}

// SessionURI is the AHP session of a mako session, and ChatURI its chat.
func SessionURI(id string) string { return "ahp-session:/" + id }
func ChatURI(id string) string    { return "ahp-chat:/" + id }

// reconcile adds a mirror for each new session and removes those of the
// sessions gone.
func (b *Bridge) reconcile(ctx context.Context) error {
	c, err := b.client(ctx)
	if err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	list, err := c.Sessions(lctx)
	cancel()
	if err != nil {
		b.dropClient(c)
		return err
	}
	live := map[string]*protocol.SessionInfo{}
	for _, s := range list {
		if mirrored(s) {
			live[SessionURI(s.GetId())] = s
		}
	}
	b.mu.Lock()
	var gone []*mirror
	for uri, m := range b.sessions {
		if live[uri] == nil {
			gone = append(gone, m)
			delete(b.sessions, uri)
		}
	}
	var added []*protocol.SessionInfo
	for uri, s := range live {
		if b.sessions[uri] == nil {
			added = append(added, s)
		}
	}
	b.mu.Unlock()
	for _, m := range gone {
		m.stop()
		if err := b.host.RemoveSession(m.uri); err != nil {
			b.log.Debug("ahp: session not removed", "session", m.id, "err", err)
		}
	}
	slices.SortFunc(added, func(a, b *protocol.SessionInfo) int { return strings.Compare(a.GetId(), b.GetId()) })
	for _, s := range added {
		if err := b.add(ctx, s); err != nil {
			b.log.Warn("ahp: session not mirrored", "session", s.GetId(), "err", err)
		}
	}
	// A session's approvals wait for an AHP client while one is active on
	// it (session/activeClientSet), and go to the terminal otherwise.
	b.mu.Lock()
	mirrors := slices.Collect(maps.Values(b.sessions))
	b.mu.Unlock()
	for _, m := range mirrors {
		st, ok := b.host.SessionState(m.uri)
		m.showApprovals(ok && len(st.ActiveClients) > 0)
	}
	return nil
}

// add mirrors session s.
func (b *Bridge) add(ctx context.Context, s *protocol.SessionInfo) error {
	if err := b.advertise(s.GetAgent()); err != nil {
		return err
	}
	var dirs []string
	var project *ahptypes.ProjectInfo
	if p := s.GetProject(); p != "" {
		dirs = []string{fileURI(p)}
		project = &ahptypes.ProjectInfo{Uri: fileURI(p), DisplayName: lastElem(p)}
	}
	chat, err := b.host.AddSession(host.OwnedSession{
		Session: SessionURI(s.GetId()), Chat: ChatURI(s.GetId()),
		Provider: s.GetAgent(), Title: title(s), WorkingDirectories: dirs, Project: project,
	})
	if err != nil {
		return err
	}
	m := newMirror(b, s, chat)
	b.mu.Lock()
	b.sessions[m.uri] = m
	b.mu.Unlock()
	go m.run(ctx)
	return nil
}

// advertise adds agent to the providers the root state lists.
func (b *Bridge) advertise(agent string) error {
	b.mu.Lock()
	if b.agents[agent] {
		b.mu.Unlock()
		return nil
	}
	b.agents[agent] = true
	names := slices.Sorted(maps.Keys(b.agents))
	b.mu.Unlock()
	infos := make([]ahptypes.AgentInfo, 0, len(names))
	for _, n := range names {
		infos = append(infos, agentInfo(n))
	}
	return b.host.SetAgents(infos)
}

func agentInfo(name string) ahptypes.AgentInfo {
	return ahptypes.AgentInfo{Provider: name, DisplayName: name, Description: "A " + name + " session in Mako", Models: []ahptypes.SessionModelInfo{}}
}

func title(s *protocol.SessionInfo) string {
	for _, t := range []string{s.GetName(), s.GetTitle()} {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return s.GetAgent() + " " + s.GetId()
}

func fileURI(p string) string { return "file://" + strings.ReplaceAll(p, "\\", "/") }

func lastElem(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// mirrorOf is the mirror of AHP session uri.
func (b *Bridge) mirrorOf(uri string) *mirror {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[uri]
}

// act sends a session_action for session m, as the bridge's next, until
// the host answers: a try that fails on the way, which the host may have
// taken, is made again, the same action, which the host takes once. What
// the host refused (protocol.CodeNotTaken), or a daemon does not know, is
// not tried again.
func (b *Bridge) act(ctx context.Context, m *mirror, req *protocol.SessionAction) error {
	b.mu.Lock()
	b.seq++
	req.ClientId, req.ClientSeq = ClientID, b.seq
	b.mu.Unlock()
	req.NodeId, req.SessionId = m.node, m.id
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	wait := 250 * time.Millisecond
	for {
		c, err := b.client(ctx)
		if err == nil {
			if _, err = c.SessionAction(ctx, req); err == nil {
				return nil
			}
			if protocol.HasCode(err, protocol.CodeNotTaken) || protocol.HasCode(err, protocol.CodeUnsupported) {
				return err
			}
			if _, answered := errors.AsType[*protocol.CodedError](err); !answered {
				b.dropClient(c) // no answer of the daemon's: dial again
			}
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(2*wait, 4*time.Second)
	}
}

// backend takes what AHP clients do on the mirrored sessions to them.
type backend struct{ b *Bridge }

var errNotHere = errors.New("ahp: sessions are started in Mako")

func (backend) CreateSession(context.Context, host.SessionInfo) error { return errNotHere }
func (backend) DisposeSession(context.Context, string)                {}

func (be backend) StartTurn(ctx context.Context, t *host.Turn) error {
	m := be.b.mirrorOf(t.Session)
	if m == nil {
		return fmt.Errorf("ahp: no session %s", t.Session)
	}
	m.clientTurn(t)
	text := t.Message.Text
	return be.b.act(context.WithoutCancel(ctx), m, &protocol.SessionAction{Action: &protocol.SessionAction_SendPrompt{SendPrompt: &protocol.HostSendPrompt{Text: text}}})
}

func (be backend) CancelTurn(ctx context.Context, t *host.Turn) {
	m := be.b.mirrorOf(t.Session)
	if m == nil {
		return
	}
	m.turnEnded(t.ID)
	if err := be.b.act(context.WithoutCancel(ctx), m, &protocol.SessionAction{Action: &protocol.SessionAction_SendPrompt{SendPrompt: &protocol.HostSendPrompt{Keys: []byte{0x1b}}}}); err != nil {
		be.b.log.Warn("ahp: turn not stopped", "session", m.id, "err", err)
	}
}

func (be backend) ConfirmToolCall(ctx context.Context, t *host.Turn, c *ahptypes.ChatToolCallConfirmedAction) {
	m := be.b.mirrorOf(t.Session)
	id, ok := strings.CutPrefix(c.ToolCallId, approvalPrefix)
	if m == nil || !ok {
		return
	}
	decision := protocol.DecisionDeny
	if c.Approved {
		decision = protocol.DecisionAllow
	}
	m.approvalAnswered(id)
	if err := be.b.act(context.WithoutCancel(ctx), m, &protocol.SessionAction{Action: &protocol.SessionAction_AnswerApproval{AnswerApproval: &protocol.AnswerApproval{ApprovalId: id, Decision: decision}}}); err != nil {
		be.b.log.Warn("ahp: approval not answered", "session", m.id, "err", err)
	}
}

func (backend) AnswerInput(context.Context, *host.Turn, *ahptypes.ChatInputCompletedAction) {}

func (be backend) Steer(ctx context.Context, t *host.Turn, msg ahptypes.PendingMessage) {
	m := be.b.mirrorOf(t.Session)
	if m == nil {
		return
	}
	if err := be.b.act(context.WithoutCancel(ctx), m, &protocol.SessionAction{Action: &protocol.SessionAction_SendPrompt{SendPrompt: &protocol.HostSendPrompt{Text: msg.Message.Text}}}); err != nil {
		be.b.log.Warn("ahp: steering not sent", "session", m.id, "err", err)
		return
	}
	_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatPendingMessageRemovedAction{Kind: ahptypes.PendingMessageKindSteering, Id: msg.Id}})
}

// ObserveAction takes a client's draft to the session's host.
func (be backend) ObserveAction(ctx context.Context, e host.ActionEvent) {
	a, ok := e.Action.Value.(*ahptypes.ChatDraftChangedAction)
	if !ok {
		return
	}
	m := be.b.mirrorOf(SessionURI(strings.TrimPrefix(e.Channel, "ahp-chat:/")))
	if m == nil {
		return
	}
	text := ""
	if a.Draft != nil {
		text = a.Draft.Text
	}
	if !m.draftFromClient(text) {
		return
	}
	if err := be.b.act(context.WithoutCancel(ctx), m, &protocol.SessionAction{Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: text}}}); err != nil {
		be.b.log.Debug("ahp: draft not set", "session", m.id, "err", err)
	}
}
