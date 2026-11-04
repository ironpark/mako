// Package detect reads what an agent is doing from its screen: the text
// its terminal shows and the title it set, matched against rules its
// driver gives. Hooks say what an agent did when it ran them; the screen
// says what it shows now, a dialog no hook told of among it.
//
// The rules' shape, a state each with a priority, a region of the screen
// and gates of text and patterns, follows herdr's agent detection
// (github.com/herdrdev/herdr, Apache-2.0).
package detect

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// State is what a screen shows an agent doing.
type State uint8

const (
	Unknown State = iota // no rule matched
	Idle                 // at its prompt
	Working              // on a turn
	Blocked              // asking: a dialog, an approval, a question
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Working:
		return "working"
	case Blocked:
		return "blocked"
	}
	return "unknown"
}

// Gate is what a region's text must hold for a rule to match. Every part
// given must hold: each of Contains, ignoring case; each of Regex; each of
// Lines on some line of its own; each of All; one of Any, when there are
// any; none of Not.
type Gate struct {
	Contains []string
	Regex    []string
	Lines    []string
	All      []Gate
	Any      []Gate
	Not      []Gate
}

// Rule is a state a screen shows when its region passes its gate. Of the
// rules that match, the one of highest priority says.
type Rule struct {
	ID       string
	State    State
	Priority int
	Region   Region
	// Suggests is a state the screen only suggests, as words a dialog
	// often has, rather than shows outright, as a prompt waiting, a dialog
	// or a spinner: it does not outweigh the hooks, and a turn's end it
	// suggests waits to be sure.
	Suggests bool
	// Hold is a screen that says nothing of the agent's state, as a
	// transcript viewer over it: what was read before stands.
	Hold bool
	Gate
}

// Input is what a screen shows: its text, row by row, and its title.
type Input struct {
	Screen string
	Title  string
}

// Rules are an agent's rules, compiled, highest priority first.
type Rules struct{ rules []compiled }

type compiled struct {
	Rule
	gate gate
}

type gate struct {
	contains      []string
	regex, lines  []*regexp.Regexp
	all, any, not []gate
}

// MustCompile compiles rules written in the program, panicking on a
// pattern Go cannot read.
func MustCompile(rules []Rule) *Rules {
	r, err := compile(rules)
	if err != nil {
		panic(err)
	}
	return r
}

func compile(rules []Rule) (*Rules, error) {
	out := &Rules{rules: make([]compiled, 0, len(rules))}
	for _, r := range rules {
		if r.Region == nil {
			return nil, fmt.Errorf("detect: rule %s has no region", r.ID)
		}
		g, err := compileGate(r.Gate)
		if err != nil {
			return nil, fmt.Errorf("detect: rule %s: %w", r.ID, err)
		}
		out.rules = append(out.rules, compiled{Rule: r, gate: g})
	}
	// Of rules at the same priority, the first written says.
	slices.SortStableFunc(out.rules, func(a, b compiled) int { return cmp.Compare(b.Priority, a.Priority) })
	return out, nil
}

func compileGate(g Gate) (out gate, err error) {
	for _, c := range g.Contains {
		out.contains = append(out.contains, strings.ToLower(c))
	}
	if out.regex, err = compileRes(g.Regex); err != nil {
		return out, err
	}
	if out.lines, err = compileRes(g.Lines); err != nil {
		return out, err
	}
	if out.all, err = compileGates(g.All); err != nil {
		return out, err
	}
	if out.any, err = compileGates(g.Any); err != nil {
		return out, err
	}
	out.not, err = compileGates(g.Not)
	return out, err
}

func compileRes(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

func compileGates(gates []Gate) ([]gate, error) {
	out := make([]gate, 0, len(gates))
	for _, g := range gates {
		c, err := compileGate(g)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Detect is the rule of highest priority in's screen matches; the zero
// Rule, Unknown, for none.
func (rs *Rules) Detect(in Input) Rule {
	in.Screen = strings.TrimRight(in.Screen, "\n")
	for i := range rs.rules {
		r := &rs.rules[i]
		if text := r.Region(in); r.gate.match(text, strings.ToLower(text)) {
			return r.Rule
		}
	}
	return Rule{}
}

func (g *gate) match(text, lower string) bool {
	for _, c := range g.contains {
		if !strings.Contains(lower, c) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range g.lines {
		if !anyLine(text, re) {
			return false
		}
	}
	matches := func(sub gate) bool { return sub.match(text, lower) }
	for _, sub := range g.all {
		if !matches(sub) {
			return false
		}
	}
	if len(g.any) > 0 && !slices.ContainsFunc(g.any, matches) {
		return false
	}
	return !slices.ContainsFunc(g.not, matches)
}

func anyLine(text string, re *regexp.Regexp) bool {
	for line := range strings.Lines(text) {
		if re.MatchString(strings.TrimRight(line, "\r\n")) {
			return true
		}
	}
	return false
}
