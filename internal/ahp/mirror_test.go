package ahp

import (
	"context"
	"errors"
	"testing"

	"github.com/gosuda/go-ahp/host"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

func testMirror(t *testing.T) (*Bridge, *mirror) {
	t.Helper()
	b, err := New(Options{Dial: func(context.Context) (*client.Client, error) { return nil, errors.New("no daemon") }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	info := &protocol.SessionInfo{Id: "s", Agent: "claude", State: protocol.StateRunning}
	if err := b.advertise("claude"); err != nil {
		t.Fatal(err)
	}
	chat, err := b.host.AddSession(host.OwnedSession{Session: SessionURI("s"), Chat: ChatURI("s"), Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	m := newMirror(b, info, chat)
	b.sessions[m.uri] = m
	return b, m
}

func state(status string, approval *protocol.Approval, msgs ...*protocol.ChatMessage) *protocol.HostState {
	return &protocol.HostState{
		Info: &protocol.SessionInfo{Id: "s", Agent: "claude", Status: status, Approval: approval},
		Chat: &protocol.Chat{Conversation: "c", Messages: msgs},
	}
}

func toolCalls(c ahptypes.ChatState) map[string]any {
	out := map[string]any{}
	var parts []ahptypes.ResponsePart
	if c.ActiveTurn != nil {
		parts = c.ActiveTurn.ResponseParts
	} else if len(c.Turns) > 0 {
		parts = c.Turns[len(c.Turns)-1].ResponseParts
	}
	for _, p := range parts {
		if tc, ok := p.Value.(*ahptypes.ToolCallResponsePart); ok {
			switch v := tc.ToolCall.Value.(type) {
			case *ahptypes.ToolCallPendingConfirmationState:
				out[v.ToolCallId] = v
			case *ahptypes.ToolCallCompletedState:
				out[v.ToolCallId] = v
			default:
				out["other"] = v
			}
		}
	}
	return out
}

// An approval the agent waits on is a tool call waiting for a client's
// confirmation, closed once it is answered anywhere; the agent's calls are
// tool calls that complete with their results.
func TestMirrorShowsApprovalsAndCalls(t *testing.T) {
	b, m := testMirror(t)
	user := &protocol.ChatMessage{Role: "user", Text: "Clean up", Id: "u1"}
	call := &protocol.ChatMessage{Role: "tool", Id: "c1", Tool: "Bash", Detail: "rm -rf build", Pending: true}
	approval := &protocol.Approval{Id: "a1", Tool: "Bash", Detail: "rm -rf build"}
	m.apply(state(protocol.AgentWaiting, approval, user, call))

	c, _ := b.host.ChatState(m.chat)
	if c.ActiveTurn == nil || c.ActiveTurn.Message.Text != "Clean up" {
		t.Fatalf("active turn: %+v", c.ActiveTurn)
	}
	if _, ok := toolCalls(c)[approvalPrefix+"a1"].(*ahptypes.ToolCallPendingConfirmationState); !ok {
		t.Fatalf("no approval waiting for confirmation: %+v", toolCalls(c))
	}

	// Answered in the window: the call closes, and the agent's own
	// completes with its result.
	done := &protocol.ChatMessage{Role: "tool", Id: "c1", Tool: "Bash", Detail: "rm -rf build", Result: "removed"}
	m.apply(state(protocol.AgentWorking, nil, user, done))
	c, _ = b.host.ChatState(m.chat)
	calls := toolCalls(c)
	if _, ok := calls[approvalPrefix+"a1"].(*ahptypes.ToolCallCompletedState); !ok {
		t.Fatalf("the answered approval is not closed: %+v", calls)
	}
	if _, ok := calls["c1"].(*ahptypes.ToolCallCompletedState); !ok {
		t.Fatalf("the call is not complete: %+v", calls)
	}

	m.apply(state(protocol.AgentIdle, nil, user, done, &protocol.ChatMessage{Role: "assistant", Text: "Done."}))
	c, _ = b.host.ChatState(m.chat)
	if c.ActiveTurn != nil || len(c.Turns) != 1 || c.Turns[0].State != ahptypes.TurnStateComplete {
		t.Fatalf("turn not complete: %+v", c)
	}
}

// Turns are keyed by their user messages: messages that leave the window
// are not turns again, and a text that grows is sent as more of it.
func TestMirrorKeepsTurnsAcrossTheWindow(t *testing.T) {
	b, m := testMirror(t)
	u1 := &protocol.ChatMessage{Role: "user", Text: "one"}
	u2 := &protocol.ChatMessage{Role: "user", Text: "two"}
	m.apply(state(protocol.AgentIdle, nil, u1, &protocol.ChatMessage{Role: "assistant", Text: "1"}, u2, &protocol.ChatMessage{Role: "assistant", Text: "2"}))
	m.apply(state(protocol.AgentWorking, nil, u2, &protocol.ChatMessage{Role: "assistant", Text: "2"}, &protocol.ChatMessage{Role: "user", Text: "three"}, &protocol.ChatMessage{Role: "assistant", Text: "Thr"}))
	m.apply(state(protocol.AgentWorking, nil, u2, &protocol.ChatMessage{Role: "assistant", Text: "2"}, &protocol.ChatMessage{Role: "user", Text: "three"}, &protocol.ChatMessage{Role: "assistant", Text: "Three."}))
	c, _ := b.host.ChatState(m.chat)
	if len(c.Turns) != 2 || c.ActiveTurn == nil || c.ActiveTurn.Message.Text != "three" {
		t.Fatalf("turns %d, active %+v", len(c.Turns), c.ActiveTurn)
	}
	md, ok := c.ActiveTurn.ResponseParts[0].Value.(*ahptypes.MarkdownResponsePart)
	if !ok || md.Content != "Three." {
		t.Fatalf("parts: %+v", c.ActiveTurn.ResponseParts)
	}
}
