package gui

import (
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A reply streaming shows as the agent's text and the calls it began,
// after the conversation, keyed apart from what the log holds.
func TestAReplyStreamingShowsAsTurns(t *testing.T) {
	turns := liveTurns([]*protocol.LiveBlock{
		{Index: 0, Kind: "text", Text: "Looking"},
		{Index: 1, Kind: "tool", Tool: "Bash"},
		{Index: 2, Kind: "text", Text: "  "},
	})
	var msgs []*protocol.ChatMessage
	for _, turn := range turns {
		if len(turn.Key) < 5 || turn.Key[:5] != "live:" {
			t.Fatalf("turn keyed %q", turn.Key)
		}
		msgs = append(msgs, turn.Messages...)
	}
	if len(msgs) != 2 || msgs[0].GetText() != "Looking" || msgs[1].GetTool() != "Bash" || !msgs[1].GetPending() {
		t.Fatalf("messages %v", msgs)
	}
	if liveTurns(nil) != nil {
		t.Fatal("turns of no reply")
	}
}

// Once the log grows, what streams waits for the read of it, so a block
// leaves the stream as the logged one shows.
func TestTheStreamWaitsForTheLog(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	state := m.chatState("s")
	m.tookLive("s", state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "a"}}})
	if len(ggui.Peek(state.live)) != 1 {
		t.Fatal("the stream did not show")
	}
	// No session is known, so the read never lands: the stream holds.
	state.loading = true
	m.tookLive("s", state, &protocol.ChatLive{Logged: true})
	m.tookLive("s", state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "b"}}})
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "a" || !state.reread {
		t.Fatalf("while the log is read: %v, reread %v", got, state.reread)
	}
	if held := state.liveHeld; len(held) != 1 || held[0].GetText() != "b" {
		t.Fatalf("held %v", held)
	}
}
