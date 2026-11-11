package codex

import (
	"slices"
	"testing"

	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Screens as Codex 0.167 draws them.
const (
	codexPrompt = `• spawned
  Worked for 4s • 9:50 PM

› Ask Codex to do anything

  GPT-6-Luna medium Fast · ~/repro
  ← for agents · ? for shortcuts`
	codexQuestion = `  Question 2/2 (1 unanswered)
  Which color?
  › 1. Red                Choose Red.
    2. Blue               Choose Blue.
    3. None of the above  Optionally, add details in notes (tab)

  tab to add notes | enter to submit all | ←/→ to navigate questions | esc to interrupt`
)

func TestRecall(t *testing.T) {
	for _, c := range []struct{ name, screen, keys string }{
		{"at its prompt", codexPrompt, ""},
		{"asking", codexQuestion, ""},
		{"a sub-agent's thread", `  >_ OpenAI Codex (v0.167.0)

• Working (51s • esc to interrupt)

› Viewing sub-agent — direct input is disabled

  GPT-6-Luna medium Fast · ~/repro
  ← for agents · ? for shortcuts`, keyAltLeft},
		{"the command center", `  Agent command center  Group: Project  g
   All 12   Needs you 0   Working 0   Ready 1   Inactive 11                tab/⇧tab filter
  ──────────────────────────────────────────────
  › ○ Spawn sleep task                                Ready          23s ago  │

  ? help  esc back  ↑/↓ move  enter open  n new`, keyEscape},
		{"its shortcuts", codexPrompt[:len(codexPrompt)-31] + "\n  ? / esc close", keyEscape},
		{"an older transcript", "  ↑/↓ to scroll   pgup/pgdn to page   home/end to jump\n  q to quit   esc to edit prev", keyQuit},
	} {
		if got := string((*Agent)(nil).Recall(c.screen)); got != c.keys {
			t.Errorf("%s: %q, want %q", c.name, got, c.keys)
		}
	}
}

func TestAsking(t *testing.T) {
	if !(*Agent)(nil).Asking(codexQuestion) || (*Agent)(nil).Asking(codexPrompt) {
		t.Fatal("asking misread")
	}
}

func TestAnswerKeys(t *testing.T) {
	opts := []*protocol.QuestionOption{{Label: "A"}, {Label: "B"}}
	qs := []*protocol.Question{{Question: "1", Options: opts}, {Question: "2", Options: opts}, {Question: "3", Options: opts}}
	got := (*Agent)(nil).AnswerKeys(qs, []*protocol.QuestionAnswer{
		{Picked: []int32{1}},
		{Text: "mine"},
		{Picked: []int32{0, 1}, Text: "why"},
	})
	down, tab, enter := input.KeyArrowDown, input.KeyTab, input.KeyEnter
	want := []agent.Keystroke{
		{Keys: []input.Key{down, enter}},
		{Keys: []input.Key{down, down, tab}}, {Text: "mine", Keys: []input.Key{enter}},
		{Keys: []input.Key{tab}}, {Text: "why", Keys: []input.Key{enter}},
	}
	if !slices.EqualFunc(got, want, func(a, b agent.Keystroke) bool { return a.Text == b.Text && slices.Equal(a.Keys, b.Keys) }) {
		t.Fatalf("keys %v, want %v", got, want)
	}
}
