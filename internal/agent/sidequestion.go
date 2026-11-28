package agent

import (
	"regexp"
	"strings"
)

// NormalizeSideQuestion collapses a multi-line side question into a single
// trimmed line suitable for submitting as a slash command argument.
func NormalizeSideQuestion(question string) string {
	fields := strings.Fields(question)
	return strings.Join(fields, " ")
}

var (
	// sideCompactingPattern matches screen lines indicating conversation
	// compaction in progress across Claude Code, Codex, and agy.
	sideCompactingPattern = regexp.MustCompile(`(?i)\b(?:auto-)?compacting(?:\s+(?:conversation|context|history))?(?:\.\.\.|…)?\b`)
	// sideSpinnerLine matches pure spinner / status lines inside a side
	// question overlay before any answer text appears.
	sideSpinnerLine = regexp.MustCompile(`(?i)^[\s\x{2800}-\x{28FF}\x{25D0}-\x{25D3}\x{002A}\x{00B7}\x{2722}\x{2733}\x{2736}\x{273B}\x{273D}•◦]*(?:thinking|answering|loading|working|generating|pondering|reasoning|asking)(?:\.\.\.|…)?(?:\s*\([^)]*\))?\s*$`)
	// sideOnlySpinner matches a line containing only spinner glyphs and whitespace.
	sideOnlySpinner = regexp.MustCompile(`^[\s\x{2800}-\x{28FF}\x{25D0}-\x{25D3}\x{002A}\x{00B7}\x{2722}\x{2733}\x{2736}\x{273B}\x{273D}]+$`)
)

// ScreenShowsCompacting reports whether screen shows an active compaction
// indicator near the bottom where the composer/status area sits.
func ScreenShowsCompacting(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, " \t\r\n"), "\n")
	start := max(len(lines)-14, 0)
	for _, line := range lines[start:] {
		if sideCompactingPattern.MatchString(line) {
			return true
		}
	}
	return false
}

// StripSideOverlayBorders removes common TUI box-drawing frame characters
// (vertical bars, left accent bars, top/bottom box edges) from an overlay line.
func StripSideOverlayBorders(line string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		return ""
	}
	// Pure horizontal box border lines.
	if isHorizontalBorder(s) {
		return ""
	}
	// Boxed line: │ ... │
	if strings.HasPrefix(s, "│") && strings.HasSuffix(s, "│") && len(s) >= len("││") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "│"), "│"))
		return s
	}
	// Left-bar accent line: ┃ ... or │ ... or ⎿ ...
	for _, prefix := range []string{"┃", "│", "║"} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(s, prefix))
		}
	}
	return s
}

func isHorizontalBorder(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case '─', '━', '═', '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼', ' ':
		default:
			return false
		}
	}
	return true
}

// CleanSideAnswer trims blank/border lines from extracted overlay lines and
// reports whether the body is still only showing a spinner/loading placeholder.
func CleanSideAnswer(lines []string) (answer string, loadingOnly bool) {
	var out []string
	sawLoading := false
	for _, raw := range lines {
		line := StripSideOverlayBorders(raw)
		if line == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if sideSpinnerLine.MatchString(line) || sideOnlySpinner.MatchString(line) {
			sawLoading = true
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "", sawLoading
	}
	return strings.Join(out, "\n"), false
}
