package detect

import "testing"

// Of the rules that match, the one of highest priority says; a gate
// holds when each of its parts does.
func TestTheRuleOfHighestPrioritySays(t *testing.T) {
	rules := MustCompile([]Rule{
		{ID: "prompt", State: Idle, Priority: 10, Region: Whole, Lines: []string{`^> `}},
		{ID: "dialog", State: Blocked, Priority: 20, Region: Whole,
			Contains: []string{"PROCEED?"},
			Any:      []Gate{{Contains: []string{"1. yes"}}, {Contains: []string{"y/n"}}},
			Not:      []Gate{{Contains: []string{"cancelled"}}}},
		{ID: "spinner", State: Working, Priority: 30, Region: Title, Regex: []string{`^⠋`}},
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
		{"empty bottom", Bottom(2)(Input{Screen: ""}), ""},
		{"blank bottom", Bottom(2)(Input{Screen: " \n\t\n"}), ""},
		{"empty top", Top(2)(Input{Screen: ""}), ""},
		{"blank top", Top(2)(Input{Screen: " \n\t\n"}), ""},
		{"after rule at end", AfterRule(Input{Screen: "a\n────"}), ""},
		{"after rule trailing nl", AfterRule(Input{Screen: "a\n────\n"}), ""},
		{"adjacent prompt rules", PromptBox(Input{Screen: "────\n────"}), ""},
		{"blank prompt box", PromptBox(Input{Screen: "────\n\n────"}), ""},
		{"multiline prompt box", PromptBox(Input{Screen: "────\na\nb\n────"}), "a\nb"},
		{"above prompt box at start", LastAbovePromptBox(Input{Screen: "────\nhi\n────"}), ""},
		{"above prompt box no rules", LastAbovePromptBox(Input{Screen: "first\nsecond\n\n"}), "second"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
	if !horizontalRule("─── title ") || horizontalRule("── title") || horizontalRule("text ───") {
		t.Error("horizontal rules")
	}

	// Region extractors must slice in.Screen directly without allocating.
	bottom3 := Bottom(3)
	top3 := Top(3)
	for _, tc := range []struct {
		name string
		fn   Region
	}{
		{"Bottom", bottom3},
		{"Top", top3},
		{"AfterRule", AfterRule},
		{"PromptBox", PromptBox},
		{"LastAbovePromptBox", LastAbovePromptBox},
	} {
		if allocs := testing.AllocsPerRun(50, func() { _ = tc.fn(in) }); allocs != 0 {
			t.Errorf("%s allocated %v times, want 0", tc.name, allocs)
		}
	}
}

func TestARulesPatternMustCompile(t *testing.T) {
	if _, err := compile([]Rule{{ID: "bad", Region: Whole, Any: []Gate{{Regex: []string{`(?!x)`}}}}}); err == nil {
		t.Error("a pattern Go cannot read compiled")
	}
	if _, err := compile([]Rule{{ID: "nowhere"}}); err == nil {
		t.Error("a rule with no region compiled")
	}
}
