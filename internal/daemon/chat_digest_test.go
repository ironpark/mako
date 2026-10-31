package daemon

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A polled conversation keeps its digest while the chat is the same, and
// a chat read again is digested anew, the same contents to the same sum.
func TestChatDigestsAreKeptByChat(t *testing.T) {
	var d digests
	chat := &protocol.Chat{Conversation: "c", Messages: []*protocol.ChatMessage{{Role: "user", Text: "hi"}}}
	first := d.of(chat)
	chat.Messages[0].Text = "changed in place, which no caller may do"
	if d.of(chat) != first {
		t.Fatal("the same chat was digested anew")
	}
	again := &protocol.Chat{Conversation: "c", Messages: []*protocol.ChatMessage{{Role: "user", Text: "hi"}}}
	if d.of(again) != first {
		t.Fatal("a chat read again with the same contents has another digest")
	}
	other := &protocol.Chat{Conversation: "c", Messages: []*protocol.ChatMessage{{Role: "user", Text: "bye"}}}
	if d.of(other) == first {
		t.Fatal("other contents share a digest")
	}
	if combine(first, digest(&protocol.Usage{Output: 1})) == combine(first, digest(&protocol.Usage{Output: 2})) {
		t.Fatal("the usage does not reach the digest")
	}
}

// A reply that slides the window, answers its last call and adds turns is
// sent as what follows the messages it repeats of the reader's.
func TestPatchSendsWhatChanged(t *testing.T) {
	msg := func(text string) *protocol.ChatMessage { return &protocol.ChatMessage{Role: "user", Text: text} }
	call := &protocol.ChatMessage{Role: "tool", Id: "c", Pending: true}
	base := &protocol.Chat{Messages: []*protocol.ChatMessage{msg("a"), msg("b"), msg("c"), call}}
	// Each read makes its own messages: the same contents, other objects.
	answered := &protocol.ChatMessage{Role: "tool", Id: "c", Result: "done"}
	reply := &protocol.Chat{Messages: []*protocol.ChatMessage{msg("b"), msg("c"), answered, msg("d")}}
	patch(reply, base, 42)
	if reply.GetBase() != 42 || reply.GetDrop() != 1 || reply.GetKeep() != 2 {
		t.Fatalf("base %d, drop %d, keep %d; want 42, 1, 2", reply.GetBase(), reply.GetDrop(), reply.GetKeep())
	}
	if got := reply.GetMessages(); len(got) != 2 || got[0] != answered || got[1].GetText() != "d" {
		t.Fatalf("sent %v, want the answered call and d", got)
	}
	// Nothing of the reader's left: the whole is sent.
	whole := &protocol.Chat{Messages: []*protocol.ChatMessage{msg("x")}}
	patch(whole, base, 42)
	if whole.GetBase() != 0 || len(whole.GetMessages()) != 1 {
		t.Fatalf("a chat sharing nothing was patched: %v", whole)
	}
}

// Two chats are kept of each conversation, for a reader a read behind.
func TestSentChatsKeepTheLastTwo(t *testing.T) {
	var s sentChats
	one, two, three := &protocol.Chat{}, &protocol.Chat{}, &protocol.Chat{}
	s.add("", 1, one)
	s.add("", 2, two)
	s.add("", 2, two) // a second reader at the same chat keeps the first's
	if s.find("", 1) != one || s.find("", 2) != two {
		t.Fatal("the last two chats are not both kept")
	}
	s.add("", 3, three)
	if s.find("", 1) != nil || s.find("", 3) != three || s.find("agent", 3) != nil {
		t.Fatal("kept other than the last two of the session's own")
	}
}
