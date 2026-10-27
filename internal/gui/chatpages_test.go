package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// Pages joined read as one read would: a result the newer page begins with
// goes on the call in the older one, which is left as it was.
func TestJoinChatPutsResultsOnTheirCalls(t *testing.T) {
	call := &protocol.ChatMessage{Role: "tool", Id: "t1", Tool: "Bash", Pending: true}
	older := []*protocol.ChatMessage{{Role: "user", Text: "hi"}, call}
	newer := []*protocol.ChatMessage{
		{Role: "tool", Id: "t1", Text: "ok", Output: true},
		{Role: "assistant", Text: "done"},
	}
	got := joinChat(older, newer)
	if len(got) != 3 || got[1].GetResult() != "ok" || got[1].GetPending() || got[2].GetText() != "done" {
		t.Fatalf("joined %v", got)
	}
	if call.GetResult() != "" || !call.GetPending() {
		t.Fatal("changed the older page's call")
	}
}
