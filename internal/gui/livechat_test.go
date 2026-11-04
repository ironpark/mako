package gui

import (
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// A reply streaming shows as the agent's text and the calls it began,
// after the conversation, keyed apart from what the log holds.
func TestAReplyStreamingShowsAsTurns(t *testing.T) {
	turns := liveTurns([]*protocol.LiveBlock{
		{Index: 0, Kind: "text", Text: "Looking"},
		{Index: 1, Kind: "tool", Tool: "Bash", Text: `{"description":"List","command":"ls -la \"my`},
		{Index: 2, Kind: "text", Text: "  "},
	})
	var msgs []*protocol.ChatMessage
	for _, turn := range turns {
		if len(turn.Key) < 5 || turn.Key[:5] != "live:" {
			t.Fatalf("turn keyed %q", turn.Key)
		}
		msgs = append(msgs, turn.Messages...)
	}
	if len(msgs) != 2 || msgs[0].GetText() != "Looking" || msgs[1].GetTool() != "Bash" || !msgs[1].GetPending() || msgs[1].GetDetail() != `ls -la "my` {
		t.Fatalf("messages %v", msgs)
	}
	thinking := liveTurns([]*protocol.LiveBlock{{Index: 0, Kind: "text", Text: "a"}, {Index: 1, Kind: "thinking"}})
	if n := len(thinking); n != 2 || !thinking[n-1].Thinking {
		t.Fatalf("no line says it thinks: %+v", thinking)
	}
	if done := liveTurns([]*protocol.LiveBlock{{Index: 0, Kind: "thinking"}, {Index: 1, Kind: "text", Text: "a"}}); len(done) != 1 || done[0].Thinking {
		t.Fatalf("a thought done still shows: %+v", done)
	}
	if liveTurns(nil) != nil {
		t.Fatal("turns of no reply")
	}
}

// Once the log grows, what streams waits for the read of it, so a block
// leaves the stream as the logged one shows.
func TestTheStreamWaitsForTheLog(t *testing.T) {
	state := newChatState()
	var landed []func()
	read := func(then func()) { landed = append(landed, then) }
	tookLive(state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "a"}}}, read)
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("the stream shows %v", got)
	}
	tookLive(state, &protocol.ChatLive{Logged: true}, read)
	tookLive(state, &protocol.ChatLive{Logged: true, Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "b"}}}, read)
	// The log grew again while it was read: read again, as the read out
	// may have gone before it grew.
	if got := ggui.Peek(state.live); len(landed) != 2 || len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("while the log is read: %v, %d reads", got, len(landed))
	}
	landed[0]()
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("with a read still out: %v", got)
	}
	landed[1]()
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "b" {
		t.Fatalf("once the read landed: %v", got)
	}
}

// A read asked for while one is out comes after it: then waits for it.
func TestAReadAskedForWhileOneIsOutComesAfter(t *testing.T) {
	state := newChatState()
	reads, ran := 0, 0
	state.loading = true
	readThen(state, func() { ran++ }, func() { reads++ })
	if !state.reread || ran != 0 {
		t.Fatal("a read out did not ask for another")
	}
}

// A tool call's arguments, cut short as they stream, say what it does.
func TestArgumentsStreamingSayWhatACallDoes(t *testing.T) {
	for args, want := range map[string]string{
		`{"description":"Run","command":"go test ./...`: "go test ./...",
		`{"file_path":"/a/b.go","old_string":"x`:        "/a/b.go",
		`{"command":"echo \"hi\" \`:                     `echo "hi"`,
		`{"pattern":"TODO`:                              "TODO",
		`{"other":"x"}`:                                 "x",
	} {
		if got := argsPreview(args); got != want {
			t.Errorf("argsPreview(%s) = %q, want %q", args, got, want)
		}
	}
}
