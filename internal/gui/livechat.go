package gui

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Following a chat: while it shows, the session's node says the moment its
// log grows, which reads it at once, and what of the agent's reply has
// streamed and is not yet logged, at most once a tick of its, which shows
// after the conversation until the log has it (protocol.WatchChat); a
// subagent's conversation too.

// chatFollow is what a chat keeps of following its conversation, on the UI
// goroutine. Once the log grows, what the node says next is held until
// the read of the log lands, so a block leaves the stream as the logged
// one shows.
type chatFollow struct {
	on      bool // the node is followed now
	waiting int  // reads out of a log that grew
	held    []*protocol.LiveBlock
	polled  time.Time // when the chat was last read every few seconds anyway
}

// followChat follows the conversation of session id, or of its subagent
// agent, into state, read again by read, which calls then once the read
// lands, until the function it returns is called, on the UI goroutine;
// nothing for a node too old to be followed.
func (m *model) followChat(id, agent string, state *chatState, read func(then func())) func() {
	info, ok := m.session(id)
	post := m.post // as the goroutine below finds it, read here
	if !ok || post == nil || !m.nodeHas(id, protocol.RevisionLiveChat) {
		return func() {}
	}
	req := &protocol.WatchChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, Agent: agent}
	ctx, cancel := context.WithCancel(context.Background())
	live := func(fn func()) {
		post(func() {
			if ctx.Err() == nil {
				fn()
			}
		})
	}
	go func() {
		for ctx.Err() == nil {
			if c, err := m.client(ctx); err == nil {
				_ = c.WatchChat(ctx, req, func(l *protocol.ChatLive) {
					live(func() { state.follow.on = true; tookLive(state, l, read) })
				})
			}
			live(func() { state.follow.on = false })
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second): // the stream broke: again
			}
		}
	}()
	return func() {
		cancel()
		state.follow = chatFollow{}
		state.live.Set(nil)
	}
}

// readThen reads state by read, and calls then once a read lands that went
// out after it was asked for: another read if one is out.
func readThen(state *chatState, then func(), read func()) {
	state.afterRead = append(state.afterRead, then)
	state.reread = state.loading
	read()
}

// pollChat reports whether a chat's read every few seconds is due: always
// while it is not followed, else once in watchedPollInterval.
func pollChat(state *chatState) bool {
	if state.follow.on && time.Since(state.follow.polled) < watchedPollInterval {
		return false
	}
	state.follow.polled = time.Now()
	return true
}

// tookLive shows what the node said of the chat: a log that grew is read,
// and the reply streaming waits for the read.
func tookLive(state *chatState, l *protocol.ChatLive, read func(then func())) {
	f := &state.follow
	// A log that grows again while it is read is read again: the read out
	// may have gone before it grew, and its last message would wait for
	// the next poll.
	if l.GetLogged() {
		f.waiting++
		read(func() {
			// A follow begun again since has nothing out.
			if f.waiting > 0 {
				if f.waiting--; f.waiting == 0 {
					state.live.Set(f.held)
				}
			}
		})
	}
	if f.waiting > 0 {
		f.held = l.GetBlocks()
		return
	}
	state.live.Set(l.GetBlocks())
}

// withLive is the turns logged, then those of the reply streaming: built
// apart, so a frame of the stream builds only its own.
func withLive(logged ggui.Readable[[]chatTurnItem], state *chatState) ggui.Readable[[]chatTurnItem] {
	live := ggui.Derived(func() []chatTurnItem { return liveTurns(state.live.Get()) })
	return ggui.Derived(func() []chatTurnItem {
		tail := live.Get()
		if len(tail) == 0 {
			return logged.Get()
		}
		return append(append([]chatTurnItem(nil), logged.Get()...), tail...)
	})
}

// liveTurns are the reply streaming as turns after the conversation's: its
// text as the agent's, the tool calls it began as calls waiting, with what
// their arguments say so far, and while it thinks, a line that says so.
func liveTurns(blocks []*protocol.LiveBlock) []chatTurnItem {
	var msgs []*protocol.ChatMessage
	for _, b := range blocks {
		key := "live:" + strconv.Itoa(int(b.GetIndex()))
		switch {
		case b.GetKind() == "text" && strings.TrimSpace(b.GetText()) != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: b.GetText(), Id: key})
		case b.GetKind() == "tool" && b.GetTool() != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "tool", Tool: b.GetTool(), Detail: argsPreview(b.GetText()), Id: key, Pending: true})
		}
	}
	turns := chatTurns(msgs, nil)
	for i := range turns {
		turns[i].Key = "live:" + turns[i].Key
	}
	if n := len(blocks); n > 0 && blocks[n-1].GetKind() == "thinking" {
		turns = append(turns, chatTurnItem{Key: "live:thinking", Messages: []*protocol.ChatMessage{{Role: "assistant"}}, Thinking: true})
	}
	return turns
}

// thinkingRow says the agent thinks, as its reply streams.
func thinkingRow() ggui.Widget {
	return ggui.Row(ui.Spinner().Size(13), muted(ggui.Text(T("Thinking…")).Size(12.5))).Gap(8).Align(ggui.AlignCenter)
}

// stringField finds the string fields of JSON that may stop short: each
// field's name, and its value so far.
var stringField = regexp.MustCompile(`"(\w+)"\s*:\s*"((?:[^"\\]|\\.)*)`)

// argsPreview is what a tool call's arguments, as far as they have
// streamed, say it does, as its row says once logged (agent.Detail).
func argsPreview(args string) string {
	fields := map[string]any{}
	for _, m := range stringField.FindAllStringSubmatch(args, -1) {
		raw := strings.TrimSuffix(m[2], `\`) // an escape cut in two
		if v, err := strconv.Unquote(`"` + raw + `"`); err == nil {
			fields[m[1]] = v
		} else {
			fields[m[1]] = raw
		}
	}
	return agent.Detail("", fields)
}
