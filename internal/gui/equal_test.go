package gui

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestDeepEqualSeesThroughMessageState(t *testing.T) {
	sent := &protocol.Chat{Supported: true, Usage: &protocol.Usage{Window: 200_000}, Messages: []*protocol.ChatMessage{{Text: "hi"}}}
	data, err := proto.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	read := func() chatResult {
		c := &protocol.Chat{}
		if err := proto.Unmarshal(data, c); err != nil {
			t.Fatal(err)
		}
		return chatResult{Chat: c, Loaded: true}
	}
	held, next := read(), read()
	proto.Equal(held.Chat.GetUsage(), sent.GetUsage()) // leaves state on the held message
	if !deepEqual(held, next) {
		t.Fatal("equal reads differ")
	}
	next.Chat.Messages[0].Text = "bye"
	if deepEqual(held, next) {
		t.Fatal("changed reads are equal")
	}
	if !deepEqual(map[string]dirListing{"a": {Loading: true}}, map[string]dirListing{"a": {Loading: true}}) ||
		deepEqual(map[string]dirListing{"a": {}}, map[string]dirListing{"b": {}}) {
		t.Fatal("maps")
	}
}
