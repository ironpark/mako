package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestGhostSuffix(t *testing.T) {
	for _, tc := range []struct {
		draft, suggestion, want string
	}{
		{"", "Run the tests", "Run the tests"},
		{"", "  Run the tests\n", "Run the tests"},
		{"run ", "Run the tests", "the tests"},
		{"RUN THE", "Run the tests", " tests"},
		{"Run the tests", "Run the tests", ""},
		{"Run the tests now", "Run the tests", ""},
		{"fix", "Run the tests", ""},
		{"run\nthe", "Run the tests", ""},
		{"테스", "테스트를 실행해", "트를 실행해"},
		{"x", "", ""},
	} {
		if got := ghostSuffix(tc.draft, tc.suggestion); got != tc.want {
			t.Errorf("ghostSuffix(%q, %q) = %q, want %q", tc.draft, tc.suggestion, got, tc.want)
		}
	}
}

func TestNextGhostWord(t *testing.T) {
	for _, tc := range []struct{ suffix, want string }{
		{" the tests", " the"},
		{"un the tests", "un"},
		{"tests", "tests"},
		{"  ", "  "},
		{"", ""},
	} {
		if got := nextGhostWord(tc.suffix); got != tc.want {
			t.Errorf("nextGhostWord(%q) = %q, want %q", tc.suffix, got, tc.want)
		}
	}
}

// The suggestion block of a live frame goes to the composer, not to the
// reply; a dismissal holds while the same suggestion comes again, and ends
// when it clears or another comes.
func TestTookLiveSplitsTheSuggestion(t *testing.T) {
	state := newChatState()
	read := func(then func()) { then() }
	tookLive(state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{
		{Index: -1, Kind: protocol.LiveKindSuggestion, Text: " Run the tests "},
		{Index: 0, Kind: "thinking", Text: "Thinking"},
	}}, read)
	if got := ggui.Peek(state.suggestion); got != "Run the tests" {
		t.Fatalf("suggestion = %q", got)
	}
	if live := ggui.Peek(state.live); len(live) != 1 || live[0].GetKind() != "thinking" {
		t.Fatalf("reply blocks = %v, want the thinking block alone", live)
	}

	state.dismissedSuggestion.Set("Run the tests")
	tookLive(state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{{Index: -1, Kind: protocol.LiveKindSuggestion, Text: "Run the tests"}}}, read)
	if got := ggui.Peek(state.dismissedSuggestion); got != "Run the tests" {
		t.Fatalf("the same suggestion again ended the dismissal: %q", got)
	}
	tookLive(state, &protocol.ChatLive{}, read)
	if got, dismissed := ggui.Peek(state.suggestion), ggui.Peek(state.dismissedSuggestion); got != "" || dismissed != "" {
		t.Fatalf("after a frame with none: suggestion %q, dismissed %q", got, dismissed)
	}
}

// ghostComposer is a composer for an idle, attached Claude session that the
// agent offered suggestion, with its field focused.
func ghostComposer(t *testing.T, suggestion string) (*model, *chatState, *ggui.Probe) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp", State: protocol.StateRunning, Status: protocol.AgentIdle}})
	state := m.chatState("s")
	state.suggestion.Set(suggestion)
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(600, 300))
	t.Cleanup(p.Close)
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatalf("no composer field:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	return m, state, p
}

func acceptChipShown(p *ggui.Probe) bool {
	_, ok := p.Semantics().Find(ggui.RoleButton, T("Accept suggestion"))
	return ok
}

// Tab takes the whole suggestion into an empty box, with the caret after it.
func TestGhostTabAcceptsTheSuggestion(t *testing.T) {
	_, state, p := ghostComposer(t, "Run the unit tests")
	if !acceptChipShown(p) {
		t.Fatal("no Accept chip while a suggestion shows")
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Text("!")
	if got := ggui.Peek(state.draft); got != "Run the unit tests!" {
		t.Fatalf("draft %q after Tab and typing", got)
	}
	if acceptChipShown(p) {
		t.Fatal("the Accept chip stayed with nothing left to accept")
	}
}

// → takes the rest of a suggestion the draft begins, whatever its case, and
// Alt+→ one word of it.
func TestGhostArrowsAcceptTheRest(t *testing.T) {
	_, state, p := ghostComposer(t, "Run the unit tests")
	p.Text("run")
	p.Type(ggui.Mods{Alt: true}, ggui.KeyArrowRight)
	if got := ggui.Peek(state.draft); got != "run the" {
		t.Fatalf("draft %q after Alt+→, want one more word", got)
	}
	p.Type(ggui.Mods{}, ggui.KeyArrowRight)
	if got := ggui.Peek(state.draft); got != "run the unit tests" {
		t.Fatalf("draft %q after →, want the rest", got)
	}
}

// A draft that does not begin the suggestion shows none, and Tab is the
// window's again.
func TestGhostHidesForAnotherDraft(t *testing.T) {
	_, state, p := ghostComposer(t, "Run the unit tests")
	p.Text("fix")
	if acceptChipShown(p) {
		t.Fatal("the Accept chip shows for a draft the suggestion does not continue")
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	if got := ggui.Peek(state.draft); got != "fix" {
		t.Fatalf("Tab changed the draft to %q", got)
	}
}

// Esc dismisses the suggestion until another comes.
func TestGhostEscDismisses(t *testing.T) {
	_, state, p := ghostComposer(t, "Run the unit tests")
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if acceptChipShown(p) {
		t.Fatal("the Accept chip stayed after Esc")
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	if got := ggui.Peek(state.draft); got != "" {
		t.Fatalf("Tab took a dismissed suggestion: %q", got)
	}
	state.suggestion.Set("Commit the changes")
	p.Frame()
	if !acceptChipShown(p) {
		t.Fatal("a new suggestion did not show after a dismissal")
	}
}

// No suggestion shows while the agent works, nor while the slash command
// menu lists commands.
func TestGhostYieldsToWorkAndTheCommandMenu(t *testing.T) {
	m, state, p := ghostComposer(t, "/compact the conversation")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp", State: protocol.StateRunning, Status: protocol.AgentWorking}})
	p.Frame()
	if acceptChipShown(p) {
		t.Fatal("the Accept chip shows while the agent works")
	}
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp", State: protocol.StateRunning, Status: protocol.AgentDone}})
	p.Frame()
	if !acceptChipShown(p) {
		t.Fatal("no Accept chip once the agent is done")
	}
	state.menu.read = time.Now()
	state.menu.commands.Set([]*protocol.Command{{Name: "compact", Args: "[instructions]"}, {Name: "clear"}})
	p.Text("/c")
	p.Frame()
	if acceptChipShown(p) {
		t.Fatal("the Accept chip shows over the command menu")
	}
}
