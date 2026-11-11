package agent

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// A question call's arguments read as their questions, kept whole in the
// question kind's form and read back from it.
func TestQuestionsReadBackFromTheirForm(t *testing.T) {
	in := map[string]any{"questions": []any{
		map[string]any{"question": "Which fruits?", "header": "Fruit", "multiSelect": true, "options": []any{
			map[string]any{"label": "Apple", "description": "red"}, "Pear"}},
		map[string]any{"question": "", "options": []any{"skipped"}},
		map[string]any{"question": "Which color?", "multi_select": false, "options": []any{map[string]any{"label": "Red"}}},
	}}
	want := []*protocol.Question{
		{Question: "Which fruits?", Header: "Fruit", MultiSelect: true, Options: []*protocol.QuestionOption{{Label: "Apple", Description: "red"}, {Label: "Pear"}}},
		{Question: "Which color?", Options: []*protocol.QuestionOption{{Label: "Red"}}},
	}
	canon := Question(in, "question")
	if canon["question"] != "Which fruits?" || len(List(canon, "options")) != 2 {
		t.Fatalf("form %v", canon)
	}
	for _, got := range [][]*protocol.Question{Questions(in), Questions(canon)} {
		if len(got) != len(want) {
			t.Fatalf("%d questions", len(got))
		}
		for i := range want {
			if !proto.Equal(got[i], want[i]) {
				t.Fatalf("question %d: %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestAnswers(t *testing.T) {
	q := &protocol.Question{Question: "Which?", Options: []*protocol.QuestionOption{{Label: "A"}, {Label: "B"}}}
	for _, c := range []struct {
		a    *protocol.QuestionAnswer
		want string
	}{
		{&protocol.QuestionAnswer{Picked: []int32{1}}, "B"},
		{&protocol.QuestionAnswer{Picked: []int32{0, 1}, Text: " and C "}, "A, B, and C"},
		{&protocol.QuestionAnswer{Picked: []int32{5, -1}}, ""},
		{&protocol.QuestionAnswer{Text: "mine"}, "mine"},
	} {
		if got := AnswerText(q, c.a); got != c.want {
			t.Errorf("%v: %q, want %q", c.a, got, c.want)
		}
	}
	qs := []*protocol.Question{q, q}
	if !Answered(qs, []*protocol.QuestionAnswer{{Picked: []int32{0}}, {Text: "x"}}) {
		t.Error("answered questions not taken")
	}
	if Answered(qs, []*protocol.QuestionAnswer{{Picked: []int32{0}}}) || Answered(qs, []*protocol.QuestionAnswer{{Picked: []int32{0}}, {}}) || Answered(nil, nil) {
		t.Error("unanswered questions taken")
	}
}
