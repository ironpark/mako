//go:build unix

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ahpclient "github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"

	"github.com/gosuda/go-ahp/wstransport"

	"github.com/ironpark/mako/internal/ahp"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// ahpChat is an AHP client's copy of one chat, kept by the official
// reducers.
type ahpChat struct {
	t    *testing.T
	sub  *ahpclient.Subscription
	chat ahptypes.ChatState
}

func (a *ahpChat) until(ctx context.Context, what string, ok func(*ahptypes.ChatState) bool) {
	a.t.Helper()
	for !ok(&a.chat) {
		select {
		case ev := <-a.sub.Events():
			if act, isAct := ev.(ahpclient.SubscriptionEventAction); isAct && act.Envelope.RejectionReason == nil {
				ahpclient.ApplyActionToChat(&a.chat, act.Envelope.Action)
			}
		case <-ctx.Done():
			a.t.Fatalf("no chat with %s; turns %+v, active %+v", what, a.chat.Turns, a.chat.ActiveTurn)
		}
	}
}

func turnText(parts []ahptypes.ResponsePart) string {
	var b strings.Builder
	for _, p := range parts {
		if md, ok := p.Value.(*ahptypes.MarkdownResponsePart); ok {
			b.WriteString(md.Content)
		}
	}
	return b.String()
}

// An AHP client sees a session's conversation as turns, its turns as the
// agent works, the draft both ways, and its own message typed into the
// session and its reply as its turn.
func TestAHPMirrorsASession(t *testing.T) {
	d := meshDaemon(t, "ahp")
	addSessionOf(t, d, "agent", "claude")
	s, _ := d.sessions.get("agent")
	log := newClaudeLog(t, s)
	log.add("user", "Look at the build")
	log.add("assistant", "Reading the logs.")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	b, err := ahp.New(ahp.Options{Dial: func(ctx context.Context) (*client.Client, error) { return client.Dial(ctx, d.Socket()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	go func() { _ = b.Run(ctx) }()
	ws, err := wstransport.NewHandler(b.ServeConn, wstransport.Options{Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ws)
	defer srv.Close()
	tr, err := ahpws.Connect(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), ahpws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ahpclient.Connect(ctx, tr, ahpclient.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(context.Background())
	if _, err := c.Initialize(ctx, "ahp-test", []string{ahptypes.ProtocolVersion}, nil); err != nil {
		t.Fatal(err)
	}

	chatURI := ahp.ChatURI("agent")
	var res *ahptypes.SubscribeResult
	var sub *ahpclient.Subscription
	waitFor(t, "the session mirrored", func() bool {
		res, sub, err = c.Subscribe(ctx, chatURI)
		return err == nil
	})
	a := &ahpChat{t: t, sub: sub, chat: *res.Snapshot.State.Chat}
	a.until(ctx, "the first turn", func(c *ahptypes.ChatState) bool {
		return len(c.Turns) == 1 && c.Turns[0].Message.Text == "Look at the build" && turnText(c.Turns[0].ResponseParts) == "Reading the logs."
	})

	// The agent works on a turn typed in its terminal.
	s.SetStatus(protocol.AgentWorking)
	log.add("user", "Fix it")
	log.add("assistant", "Fixing.")
	a.until(ctx, "the agent's turn", func(c *ahptypes.ChatState) bool {
		return c.ActiveTurn != nil && c.ActiveTurn.Message.Text == "Fix it"
	})
	s.SetStatus(protocol.AgentIdle)
	a.until(ctx, "the agent's turn done", func(c *ahptypes.ChatState) bool {
		return c.ActiveTurn == nil && len(c.Turns) == 2 && turnText(c.Turns[1].ResponseParts) == "Fixing."
	})

	// The client's message is typed into the session, and the turn the
	// log then shows is the client's.
	if _, err := c.Dispatch(ctx, chatURI, ahptypes.StateAction{Value: &ahptypes.ChatTurnStartedAction{
		Type: ahptypes.ActionTypeChatTurnStarted, TurnId: "mine", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Message: ahptypes.Message{Text: "Run the tests", Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser}},
	}}); err != nil {
		t.Fatal(err)
	}
	h := heldHost(t, d, s)
	waitFor(t, "the client's prompt typed", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, ev := range h.ring {
			if ev.GetPrompt().GetText() == "Run the tests" && ev.GetOrigin().GetClientId() == ahp.ClientID {
				return true
			}
		}
		return false
	})
	s.SetStatus(protocol.AgentWorking)
	log.add("user", "Run the tests")
	log.add("assistant", "All pass.")
	a.until(ctx, "the reply in the client's turn", func(c *ahptypes.ChatState) bool {
		return c.ActiveTurn != nil && c.ActiveTurn.Id == "mine" && turnText(c.ActiveTurn.ResponseParts) == "All pass."
	})
	s.SetStatus(protocol.AgentIdle)
	a.until(ctx, "the client's turn done", func(c *ahptypes.ChatState) bool {
		return c.ActiveTurn == nil && len(c.Turns) == 3 && c.Turns[2].Id == "mine"
	})

	// A draft written in AHP is the session's, and one written in Mako is
	// the AHP client's.
	if _, err := c.Dispatch(ctx, chatURI, ahptypes.StateAction{Value: &ahptypes.ChatDraftChangedAction{
		Type: ahptypes.ActionTypeChatDraftChanged, Draft: &ahptypes.Message{Text: "from AHP", Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser}},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the AHP draft in Mako", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.state.GetDraft().GetText() == "from AHP"
	})
	act(t, dialDaemon(t, d), &protocol.SessionAction{SessionId: "agent", ClientId: "desk", ClientSeq: 1, Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: "from Mako"}}})
	a.until(ctx, "the Mako draft in AHP", func(c *ahptypes.ChatState) bool {
		return c.Draft != nil && c.Draft.Text == "from Mako"
	})
}

// While an AHP client is active on a session, its approvals wait for an
// answer rather than going to the agent's terminal; with none, they do not.
func TestAHPHoldsApprovalsForAnActiveClient(t *testing.T) {
	d := meshDaemon(t, "ahp-approvals")
	addSessionOf(t, d, "agent", "claude")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	b, err := ahp.New(ahp.Options{Dial: func(ctx context.Context) (*client.Client, error) { return client.Dial(ctx, d.Socket()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	go func() { _ = b.Run(ctx) }()
	ws, err := wstransport.NewHandler(b.ServeConn, wstransport.Options{Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ws)
	defer srv.Close()
	tr, err := ahpws.Connect(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), ahpws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ahpclient.Connect(ctx, tr, ahpclient.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(context.Background())
	if _, err := c.Initialize(ctx, "phone", []string{ahptypes.ProtocolVersion}, nil); err != nil {
		t.Fatal(err)
	}
	session := ahp.SessionURI("agent")
	waitFor(t, "the session mirrored", func() bool {
		_, _, err := c.Subscribe(ctx, session)
		return err == nil
	})
	time.Sleep(2500 * time.Millisecond) // a listing of the bridge's
	if d.approvals.watchingSession("agent") {
		t.Fatal("approvals wait with no AHP client active")
	}
	if _, err := c.Dispatch(ctx, session, ahptypes.StateAction{Value: &ahptypes.SessionActiveClientSetAction{
		Type: ahptypes.ActionTypeSessionActiveClientSet, ActiveClient: ahptypes.SessionActiveClient{ClientId: "phone", Tools: []ahptypes.ToolDefinition{}},
	}}); err != nil {
		t.Fatal(err)
	}
	waitForWithin(t, 10*time.Second, "approvals held for the active client", func() bool { return d.approvals.watchingSession("agent") })
}
