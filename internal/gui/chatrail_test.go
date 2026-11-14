package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// The rail has a tick for each prompt, with the last of the agent's words
// before the next one; tool calls and an empty prompt are not its own.
func TestRailTurnsArePromptsWithTheirReplies(t *testing.T) {
	turns := chatTurns([]*protocol.ChatMessage{
		{Role: "user", Text: "first?"},
		{Role: "assistant", Text: "Looking."},
		{Role: "tool", Tool: "Bash", Id: "t1"},
		{Role: "assistant", Text: "**Done**: it works."},
		{Role: "user", Text: "  "},
		{Role: "user", Text: "second\nmore"},
		{Role: "assistant", Tool: "Edit", Text: "a tool's words"},
	}, nil)
	got := railTurns(turns)
	if len(got) != 2 || got[0].Prompt != "first?" || got[0].Reply != "**Done**: it works." || got[1].Prompt != "second\nmore" || got[1].Reply != "" {
		t.Fatalf("rail turns %+v", got)
	}
	if got[1].Index != 5 {
		t.Fatalf("the second prompt is at %d of the feed", got[1].Index)
	}
}

func TestPlainPreviewTakesTheMarksOut(t *testing.T) {
	md := "## Result\n\n현재 남은 항목은 **C5.1 원격 CI**입니다.\n\n```go\ncode()\n```\n- `build` passes\n| a | b |\n"
	if got, want := plainPreview(md), "Result 현재 남은 항목은 C5.1 원격 CI입니다. build passes"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := plainPreview(strings.Repeat("가", 300)); !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "가가") {
		t.Fatalf("a long reply is not cut: %q", got)
	}
}

// At the latest the rail marks the last prompt; gone to the first, it
// marks that.
func TestTheRailFollowsTheReaderAndGoesToAPrompt(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	state := m.chatState("s")
	var messages []*protocol.ChatMessage
	for i := range 40 {
		messages = append(messages,
			&protocol.ChatMessage{Role: "user", Text: fmt.Sprintf("prompt %d", i), Id: fmt.Sprintf("u%d", i)},
			&protocol.ChatMessage{Role: "assistant", Text: strings.Repeat(fmt.Sprintf("reply %d. ", i), 40), Id: fmt.Sprintf("a%d", i)})
	}
	turns := ggui.State(chatTurns(messages, nil))
	scroll := &ggui.ScrollController{}
	find := &chatFinder{turns: turns, scroll: scroll, marks: map[string]rowMark{}}
	rail := newTurnRail(m, find, turns)
	p := ggui.ProbeBuilder(func() ggui.Widget {
		scrolling, feed := chatFeed(m, "s", state, turns, ggui.Box(), find)
		scrolling.Controller(scroll)
		return ggui.Stack(feed, rail, &afterPaint{fn: find.painted}).Expand()
	}, ggui.Sz(900, 700))
	defer p.Close()
	for range 10 {
		p.Frame()
	}
	prompts := ggui.Peek(rail.prompts)
	if len(prompts) != 40 {
		t.Fatalf("%d prompts on the rail", len(prompts))
	}
	if rail.current != prompts[39].Key {
		t.Fatalf("at the latest the rail marks %q", rail.current)
	}
	find.jumpTo(prompts[0].Key)
	for range 10 {
		p.Frame()
	}
	if rail.current != prompts[0].Key {
		t.Fatalf("gone to the first prompt, the rail marks %q", rail.current)
	}
	find.jumpTo(prompts[20].Key)
	for range 10 {
		p.Frame()
	}
	if rail.current != prompts[20].Key {
		t.Fatalf("gone to prompt 20, the rail marks %q", rail.current)
	}

	// The pointer on a tick shows its prompt's card; a click goes to it.
	top := (700 - rail.gap*40) / 2
	at := ggui.Pt(10, top+rail.gap*5+rail.gap/2)
	p.Move(at)
	for range 5 {
		p.Frame()
	}
	if _, ok := p.Semantics().Find(ggui.RoleGroup, "prompt 5"); !ok {
		t.Fatalf("no card for prompt 5:\n%s", p.Semantics().String())
	}
	p.Click(at)
	for range 10 {
		p.Frame()
	}
	if rail.current != prompts[5].Key {
		t.Fatalf("a click on prompt 5's tick went to %q", rail.current)
	}
}
