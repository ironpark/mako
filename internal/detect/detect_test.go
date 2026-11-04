package detect

import "testing"

// Of the rules that match, the one of highest priority says; a gate
// holds when each of its parts does.
func TestTheRuleOfHighestPrioritySays(t *testing.T) {
	rules := MustCompile([]Rule{
		{ID: "prompt", State: Idle, Priority: 10, Region: Whole, Gate: Gate{Lines: []string{`^> `}}},
		{ID: "dialog", State: Blocked, Priority: 20, Region: Whole, Gate: Gate{
			Contains: []string{"PROCEED?"},
			Any:      []Gate{{Contains: []string{"1. yes"}}, {Contains: []string{"y/n"}}},
			Not:      []Gate{{Contains: []string{"cancelled"}}},
		}},
		{ID: "spinner", State: Working, Priority: 30, Region: Title, Gate: Gate{Regex: []string{`^⠋`}}},
	})
	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{Screen: "hello\n> "}, "prompt"},
		{Input{Screen: "Proceed?\n 1. Yes\n 2. No\n> "}, "dialog"},
		{Input{Screen: "Proceed?\n> "}, "prompt"},               // no choice
		{Input{Screen: "Proceed? y/n cancelled\n> "}, "prompt"}, // ruled out
		{Input{Screen: "Proceed? y/n\n> ", Title: "⠋ claude"}, "spinner"},
		{Input{Screen: "nothing"}, ""},
	} {
		if got := rules.Detect(c.in); got.ID != c.want {
			t.Errorf("%q: rule %q, want %q", c.in.Screen, got.ID, c.want)
		}
	}
}

func TestRegions(t *testing.T) {
	screen := "reply\n\n────────\n❯ write\n────────\n  ? for shortcuts\n\n"
	in := Input{Screen: screen}
	for _, c := range []struct {
		name, got, want string
	}{
		{"bottom", Bottom(2)(Input{Screen: "a\nb\n\nc\n"}), "b\n\nc\n"},
		{"top", Top(2)(Input{Screen: "\na\n\nb\nc"}), "\na\n\nb"},
		{"after rule", AfterRule(in), "  ? for shortcuts\n\n"},
		{"prompt box", PromptBox(in), "❯ write"},
		{"above prompt box", LastAbovePromptBox(in), "reply"},
		{"no prompt box", PromptBox(Input{Screen: "a\n────\nb"}), ""},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
	if !horizontalRule("─── title ") || horizontalRule("── title") || horizontalRule("text ───") {
		t.Error("horizontal rules")
	}
}

func TestARulesPatternMustCompile(t *testing.T) {
	if _, err := compile([]Rule{{ID: "bad", Region: Whole, Gate: Gate{Any: []Gate{{Regex: []string{`(?!x)`}}}}}}); err == nil {
		t.Error("a pattern Go cannot read compiled")
	}
	if _, err := compile([]Rule{{ID: "nowhere"}}); err == nil {
		t.Error("a rule with no region compiled")
	}
}
