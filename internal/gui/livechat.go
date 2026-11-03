package gui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// Following a chat: while it shows, the session's node says the moment its
// log grows, which reads it at once, and what of the agent's reply has
// streamed and is not yet logged, which shows after the conversation until
// the log has it (protocol.WatchChat).

// followChat follows session id's conversation into state until the
// function it returns is called, on the UI goroutine; nothing for a node
// too old to be followed.
func (m *model) followChat(id string, state *chatState) func() {
	info, ok := m.session(id)
	if !ok || !m.nodeHas(id, protocol.RevisionLiveChat) {
		return func() {}
	}
	req := &protocol.WatchChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for ctx.Err() == nil {
			if c, err := m.client(ctx); err == nil {
				_ = c.WatchChat(ctx, req, func(l *protocol.ChatLive) {
					m.post(func() {
						if ctx.Err() == nil && m.chats[id] == state {
							m.tookLive(id, state, l)
						}
					})
				})
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second): // the stream broke: again
			}
		}
	}()
	return func() {
		cancel()
		state.liveWaiting, state.liveHeld = false, nil
		state.live.Set(nil)
	}
}

// tookLive shows what the node said of the chat: a log that grew is read,
// and the reply streaming waits for the read, which may hold what left it.
func (m *model) tookLive(id string, state *chatState, l *protocol.ChatLive) {
	if l.GetLogged() {
		state.liveWaiting = true
		m.loadChat(id, state)
	}
	if state.liveWaiting {
		state.liveHeld = l.GetBlocks()
		return
	}
	state.live.Set(l.GetBlocks())
}

// liveTurns are the reply streaming as turns after the conversation's, its
// text as the agent's and the tool calls it began as calls waiting.
func liveTurns(blocks []*protocol.LiveBlock) []chatTurnItem {
	var msgs []*protocol.ChatMessage
	for _, b := range blocks {
		key := "live:" + strconv.Itoa(int(b.GetIndex()))
		switch {
		case b.GetKind() == "text" && strings.TrimSpace(b.GetText()) != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: b.GetText(), Id: key})
		case b.GetKind() == "tool" && b.GetTool() != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "tool", Tool: b.GetTool(), Id: key, Pending: true})
		}
	}
	turns := chatTurns(msgs, nil)
	for i := range turns {
		turns[i].Key = "live:" + turns[i].Key
	}
	return turns
}
