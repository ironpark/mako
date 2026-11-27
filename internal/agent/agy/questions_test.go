package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	agyQuestionMultiQ1 = `Question
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

Question 1/2: Pick color

> 1. Red
  2. Blue
  3. Write-in...

  ↑/↓ Navigate · → Next · enter Select · esc Skip
esc to cancel                                                                                auto · Gemini 4 Neon · high`

	agyQuestionMultiQ2 = `Question
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

Question 2/2: Pick fruits

> 1. [ ] Apple
  2. [ ] Banana
  3. [ ] Cherry
  4. Write-in...

  ↑/↓ Navigate · ← Back · space Toggle · enter Submit All · esc Skip
esc to cancel                                                                                auto · Gemini 4 Neon · high`

	agyQuestionWriteInOpen = `Question
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

Question 1/2: Pick color

  1. Red
  2. Blue
> 3. Write-in...

  Your answer:

  enter Submit · esc Back
esc to cancel                                                                                auto · Gemini 4 Neon · high`
)

func TestAskingAndStackedStatusLine(t *testing.T) {
	a := New()
	stacked := agyQuestionMultiQ1 + "\nctx 14% · 5h 72% · wk 94%\nextra status"
	for _, tc := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"multi q1", agyQuestionMultiQ1, true},
		{"multi q2 checkboxes", agyQuestionMultiQ2, true},
		{"stacked custom statusLine", stacked, true},
		{"write-in open", agyQuestionWriteInOpen, false},
		{"idle prompt", "? for shortcuts     auto · Gemini 4 Neon · high", false},
	} {
		if got := a.Asking(tc.screen); got != tc.want {
			t.Errorf("%s: Asking = %v, want %v", tc.name, got, tc.want)
		}
	}
	if r := a.ScreenRules().Detect(detect.Input{Screen: stacked}); r.State != detect.Blocked || r.ID != "question_prompt" {
		t.Fatalf("stacked statusLine screen rule = %+v, want blocked/question_prompt", r)
	}
	for _, file := range []string{"screen_permission.txt", "screen_trust.txt", "screen_idle.txt", "screen_slash_menu.txt"} {
		screen, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if a.Asking(string(screen)) {
			t.Errorf("%s: Asking = true, want false", file)
		}
	}
}

func TestAskQuestionToolPreservesMultipleQuestionsAndMultiSelect(t *testing.T) {
	a := New()
	raw := json.RawMessage(`{"questions":[
		{"question":"Pick color","is_multi_select":false,"options":["Red","Blue"]},
		{"question":"Pick fruits","is_multi_select":true,"options":["Apple","Banana","Cherry"]}
	]}`)
	msg := agent.Call(a, "1.0", "ask_question", raw)
	if msg.GetKind() != protocol.ToolQuestion {
		t.Fatalf("kind = %q", msg.GetKind())
	}
	qs := msg.GetQuestions()
	if len(qs) != 2 {
		t.Fatalf("len(Questions) = %d, want 2", len(qs))
	}
	if qs[0].GetQuestion() != "Pick color" || qs[0].GetMultiSelect() || len(qs[0].GetOptions()) != 2 {
		t.Fatalf("q0 = %+v", qs[0])
	}
	if qs[1].GetQuestion() != "Pick fruits" || !qs[1].GetMultiSelect() || len(qs[1].GetOptions()) != 3 {
		t.Fatalf("q1 = %+v", qs[1])
	}
}

func TestAnswerKeys(t *testing.T) {
	a := New()
	qs := []*protocol.Question{
		{
			Question: "Pick color",
			Options:  []*protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}},
		},
		{
			Question:    "Pick fruits",
			MultiSelect: true,
			Options:     []*protocol.QuestionOption{{Label: "Apple"}, {Label: "Banana"}, {Label: "Cherry"}},
		},
		{
			Question: "Any notes?",
			Options:  []*protocol.QuestionOption{{Label: "Yes"}, {Label: "No"}},
		},
		{
			Question:    "Toppings",
			MultiSelect: true,
			Options:     []*protocol.QuestionOption{{Label: "Cheese"}, {Label: "Olives"}},
		},
	}
	got := a.AnswerKeys(qs, []*protocol.QuestionAnswer{
		{Picked: []int32{1}},
		{Picked: []int32{2, 0}}, // unsorted, should toggle 0 then 2
		{Text: "custom\nnote"},
		{Picked: []int32{0}, Text: "extra basil"},
	})
	down, space, enter := input.KeyArrowDown, input.KeySpace, input.KeyEnter
	want := []agent.Keystroke{
		// Q1: single-select option 1 -> Down, Enter
		{Keys: []input.Key{down, enter}},
		// Q2: multi-select options 0 & 2 -> Space, Down, Down, Space, Enter
		{Keys: []input.Key{space, down, down, space, enter}},
		// Q3: single-select write-in -> Down, Down, Enter; then paste + Enter
		{Keys: []input.Key{down, down, enter}},
		{Text: "custom note", Keys: []input.Key{enter}},
		// Q4: multi-select option 0 + write-in -> Space, Down, Down, Enter; then paste + Enter
		{Keys: []input.Key{space, down, down, enter}},
		{Text: "extra basil", Keys: []input.Key{enter}},
	}
	if !slices.EqualFunc(got, want, func(x, y agent.Keystroke) bool {
		return x.Text == y.Text && slices.Equal(x.Keys, y.Keys)
	}) {
		t.Fatalf("AnswerKeys:\n got  %#v\n want %#v", got, want)
	}
}
