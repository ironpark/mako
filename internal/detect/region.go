package detect

import "strings"

// Region is the part of a screen a rule reads.
type Region func(Input) string

// Title is the title the program set.
func Title(in Input) string { return in.Title }

// Whole is the whole screen.
func Whole(in Input) string { return in.Screen }

// Bottom is the screen from its nth last line that has text.
func Bottom(n int) Region {
	return func(in Input) string {
		lines := lines(in.Screen)
		start, seen := len(lines), 0
		for i := len(lines) - 1; i >= 0 && seen < n; i-- {
			if strings.TrimSpace(lines[i]) != "" {
				start, seen = i, seen+1
			}
		}
		return join(lines[start:])
	}
}

// Top is the screen up to its nth line that has text.
func Top(n int) Region {
	return func(in Input) string {
		lines := lines(in.Screen)
		end, seen := 0, 0
		for i := 0; i < len(lines) && seen < n; i++ {
			if strings.TrimSpace(lines[i]) != "" {
				end, seen = i+1, seen+1
			}
		}
		return join(lines[:end])
	}
}

// AfterRule is the screen below its last horizontal rule, as a dialog
// under a divider is.
func AfterRule(in Input) string {
	lines := lines(in.Screen)
	for i := len(lines) - 1; i >= 0; i-- {
		if horizontalRule(lines[i]) {
			return join(lines[i+1:])
		}
	}
	return in.Screen
}

// PromptBox is what is inside the prompt box, a composer between two
// rules, the last such on the screen; "" when there is none.
func PromptBox(in Input) string {
	lines := lines(in.Screen)
	top, ok := promptBoxTop(lines)
	if !ok {
		return ""
	}
	end := len(lines)
	for i := top + 1; i < len(lines); i++ {
		if horizontalRule(lines[i]) {
			end = i
			break
		}
	}
	return join(lines[top+1 : end])
}

// LastAbovePromptBox is the last line with text above the prompt box, as a
// status line over a composer is.
func LastAbovePromptBox(in Input) string {
	lines := lines(in.Screen)
	if top, ok := promptBoxTop(lines); ok {
		lines = lines[:top]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func join(lines []string) string { return strings.Join(lines, "\n") }

// horizontalRule is a line of box drawing, "────", alone or titled after
// three or more.
func horizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	rest := strings.TrimLeft(t, "─")
	n := (len(t) - len(rest)) / len("─")
	return n > 0 && (strings.TrimSpace(rest) == "" || n >= 3)
}

// promptBoxTop is the line of the second last rule, the top of a box
// whose bottom is the last.
func promptBoxTop(lines []string) (int, bool) {
	seen := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if horizontalRule(lines[i]) {
			if seen++; seen == 2 {
				return i, true
			}
		}
	}
	return 0, false
}
