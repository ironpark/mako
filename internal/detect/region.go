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
		s := in.Screen
		if s == "" || n <= 0 {
			return ""
		}
		start, seen := len(s), 0
		end := len(s)
		for seen < n {
			nl := strings.LastIndexByte(s[:end], '\n')
			lineStart := nl + 1
			if strings.TrimSpace(s[lineStart:end]) != "" {
				start, seen = lineStart, seen+1
			}
			if nl < 0 {
				break
			}
			end = nl
		}
		return s[start:]
	}
}

// Top is the screen up to its nth line that has text.
func Top(n int) Region {
	return func(in Input) string {
		s := in.Screen
		if s == "" || n <= 0 {
			return ""
		}
		end, seen := 0, 0
		pos := 0
		for seen < n {
			nl := strings.IndexByte(s[pos:], '\n')
			lineEnd := len(s)
			if nl >= 0 {
				lineEnd = pos + nl
			}
			if strings.TrimSpace(s[pos:lineEnd]) != "" {
				end, seen = lineEnd, seen+1
			}
			if nl < 0 {
				break
			}
			pos = lineEnd + 1
		}
		return s[:end]
	}
}

// AfterRule is the screen below its last horizontal rule, as a dialog
// under a divider is.
func AfterRule(in Input) string {
	s := in.Screen
	if s == "" {
		return ""
	}
	end := len(s)
	for {
		nl := strings.LastIndexByte(s[:end], '\n')
		lineStart := nl + 1
		if horizontalRule(s[lineStart:end]) {
			if end >= len(s) {
				return ""
			}
			return s[end+1:]
		}
		if nl < 0 {
			break
		}
		end = nl
	}
	return s
}

// PromptBox is what is inside the prompt box, a composer between two
// rules, the last such on the screen; "" when there is none.
func PromptBox(in Input) string {
	s := in.Screen
	_, topEnd, botStart, ok := promptBoxBounds(s)
	if !ok {
		return ""
	}
	// The box lies strictly between top's trailing '\n' and bot's leading '\n'.
	contentStart := topEnd + 1
	contentEnd := botStart - 1
	if contentStart >= contentEnd {
		return ""
	}
	return s[contentStart:contentEnd]
}

// LastAbovePromptBox is the last line with text above the prompt box, as a
// status line over a composer is.
func LastAbovePromptBox(in Input) string {
	s := in.Screen
	if s == "" {
		return ""
	}
	end := len(s)
	if topStart, _, _, ok := promptBoxBounds(s); ok {
		if topStart == 0 {
			return ""
		}
		end = topStart - 1
	}
	for {
		nl := strings.LastIndexByte(s[:end], '\n')
		lineStart := nl + 1
		if line := s[lineStart:end]; strings.TrimSpace(line) != "" {
			return line
		}
		if nl < 0 {
			break
		}
		end = nl
	}
	return ""
}

// horizontalRule is a line of box drawing, "────", alone or titled after
// three or more.
func horizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	rest := strings.TrimLeft(t, "─")
	n := (len(t) - len(rest)) / len("─")
	return n > 0 && (strings.TrimSpace(rest) == "" || n >= 3)
}

// promptBoxBounds returns byte offsets of the second-to-last horizontal rule
// [topStart:topEnd] and the last horizontal rule's start botStart.
func promptBoxBounds(s string) (topStart, topEnd, botStart int, ok bool) {
	if s == "" {
		return 0, 0, 0, false
	}
	seen := 0
	end := len(s)
	for {
		nl := strings.LastIndexByte(s[:end], '\n')
		lineStart := nl + 1
		if horizontalRule(s[lineStart:end]) {
			seen++
			if seen == 1 {
				botStart = lineStart
			} else {
				return lineStart, end, botStart, true
			}
		}
		if nl < 0 {
			break
		}
		end = nl
	}
	return 0, 0, 0, false
}
