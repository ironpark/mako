package protocol

import (
	"math"
	"testing"
)

// A patch applies to the conversation it was made from alone, with drop
// and keep inside it, and an event applies only after the state held.
func TestHostEventsApplyInOrder(t *testing.T) {
	held := &Chat{Conversation: "c", Messages: []*ChatMessage{{Text: "a"}, {Text: "b"}}}
	for name, p := range map[string]*HostChatPatch{
		"another conversation": {Conversation: "d", Keep: 1},
		"past the end":         {Conversation: "c", Drop: 1, Keep: 2},
		"overflowing":          {Conversation: "c", Drop: math.MaxInt32, Keep: math.MaxInt32},
		"negative":             {Conversation: "c", Drop: -1, Keep: 1},
	} {
		if p.Apply(held) != nil {
			t.Fatalf("%s: applied", name)
		}
	}
	if (&HostChatPatch{Conversation: "c"}).Apply(nil) != nil {
		t.Fatal("a patch applied to no conversation")
	}
	if got := (&HostChatPatch{Conversation: "c", Drop: 1, Keep: 1, Messages: []*ChatMessage{{Text: "c"}}}).Apply(held); len(got.GetMessages()) != 2 || got.GetMessages()[1].GetText() != "c" {
		t.Fatalf("applied: %v", got)
	}

	state := &HostState{Seq: 5}
	if ApplyHostEvent(state, &HostEvent{Seq: 5, Body: &HostEvent_Draft{Draft: &HostDraft{Text: "old"}}}) || state.GetDraft() != nil {
		t.Fatal("an event no later than the state applied")
	}
	if !ApplyHostEvent(state, &HostEvent{Seq: 6, Body: &HostEvent_Draft{Draft: &HostDraft{Text: "new"}}}) || state.GetDraft().GetText() != "new" || state.GetSeq() != 6 {
		t.Fatalf("state: %v", state)
	}
}
