package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A patch keeps the very messages on show, so their rows are not built
// again, and a patch of another chat is refused.
func TestPatchedKeepsTheMessagesOnShow(t *testing.T) {
	a, b, c := &protocol.ChatMessage{Text: "a"}, &protocol.ChatMessage{Text: "b"}, &protocol.ChatMessage{Text: "c"}
	shown := &protocol.Chat{Digest: 7, Messages: []*protocol.ChatMessage{a, b, c}}
	d := &protocol.ChatMessage{Text: "d"}
	chat, ok := patched(shown, &protocol.Chat{Digest: 8, Base: 7, Drop: 1, Keep: 2, Messages: []*protocol.ChatMessage{d}})
	if !ok {
		t.Fatal("a patch of the chat on show was refused")
	}
	if got := chat.GetMessages(); len(got) != 3 || got[0] != b || got[1] != c || got[2] != d || chat.GetBase() != 0 {
		t.Fatalf("patched to %v", got)
	}
	if _, ok := patched(shown, &protocol.Chat{Base: 6, Keep: 1}); ok {
		t.Fatal("a patch of another chat was taken")
	}
	if _, ok := patched(shown, &protocol.Chat{Base: 7, Drop: 2, Keep: 2}); ok {
		t.Fatal("a patch past the chat on show was taken")
	}
	whole := &protocol.Chat{Digest: 9, Messages: []*protocol.ChatMessage{d}}
	if chat, ok := patched(shown, whole); !ok || chat != whole {
		t.Fatal("a whole chat was not taken as it is")
	}
}
