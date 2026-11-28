package agy

import (
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
)

var (
	agyBtwHeader   = regexp.MustCompile(`(?i)^(?:/btw(?:\s|$)|btw\s*[:·]|side\s+question\b)`)
	agyBtwFooter   = regexp.MustCompile(`(?i)(?:press\s+)?esc\s+to\s+(?:dismiss|close)\b`)
	agyPromptLine  = regexp.MustCompile(`^\s*>(?:\s|$)`)
	agyRuleLine    = regexp.MustCompile(`^\s*─{4,}\s*$`)
	agySpinnerLine = regexp.MustCompile(`^\s*[\x{2801}-\x{28FF}]+\s+\S`)
)

// SideQuestionCanAsk reports whether agy's terminal screen can safely accept
// `/btw <question>` without answering a dialog or colliding with an overlay,
// slash menu, or compaction.
func (a *Agent) SideQuestionCanAsk(screen, title string) (bool, string) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return false, "the agent screen is not ready"
	}
	if _, st := a.SideQuestionParse(trimmed); st != agent.SideQuestionNone {
		return false, "a side question overlay is already open"
	}
	for _, in := range []detect.Input{
		{Screen: trimmed},
		{Screen: trimmed, Title: title},
	} {
		r := screenRules.Detect(in)
		if r.ID == "btw_overlay" {
			return false, "a side question overlay is already open"
		}
		if r.State == detect.Blocked {
			return false, "the agent is waiting on an approval, dialog, or form"
		}
		if r.Hold {
			return false, "a modal view is open over the prompt"
		}
		if r.ID == "slash_menu" {
			return false, "a slash command menu is open in the terminal"
		}
	}
	if a.Recall(trimmed) != nil {
		return false, "a modal view is open over the prompt"
	}
	if agent.ScreenShowsCompacting(trimmed) {
		return false, "the agent is compacting the conversation"
	}
	if !hasAgyPromptBox(trimmed) {
		return false, "the agent prompt is not ready"
	}
	return true, ""
}

func hasAgyPromptBox(screen string) bool {
	lines := strings.Split(screen, "\n")
	start := max(len(lines)-12, 0)
	bottom := strings.ToLower(strings.Join(lines[start:], "\n"))
	for _, blocked := range []string{
		"tab amend",
		"esc skip",
		"enter confirm",
		"enter select",
		"enter submit all",
		"do you trust the contents",
		"keyboard:",
	} {
		if strings.Contains(bottom, blocked) {
			return false
		}
	}
	hasPrompt := false
	hasRule := false
	for _, l := range lines[start:] {
		if agyRuleLine.MatchString(l) {
			hasRule = true
		}
		if agyPromptLine.MatchString(l) {
			hasPrompt = true
		}
	}
	return hasPrompt && hasRule
}

// SideQuestionCommand formats question as agy's native `/btw` command.
func (*Agent) SideQuestionCommand(question string) string {
	q := agent.NormalizeSideQuestion(question)
	if q == "" {
		return ""
	}
	return "/btw " + q
}

// SideQuestionParse reads agy's `/btw` overlay from screen.
func (*Agent) SideQuestionParse(screen string) (string, agent.SideQuestionState) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return "", agent.SideQuestionNone
	}
	lines := strings.Split(trimmed, "\n")
	scanStart := max(len(lines)-40, 0)
	headerIdx := -1
	for i := len(lines) - 1; i >= scanStart; i-- {
		if agyPromptLine.MatchString(lines[i]) {
			continue
		}
		clean := agent.StripSideOverlayBorders(lines[i])
		if agyBtwHeader.MatchString(clean) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return "", agent.SideQuestionNone
	}

	footerIdx := -1
	for i := headerIdx + 1; i < len(lines); i++ {
		clean := agent.StripSideOverlayBorders(lines[i])
		if agyBtwFooter.MatchString(clean) {
			footerIdx = i
			break
		}
	}

	end := len(lines)
	if footerIdx >= 0 {
		end = footerIdx
	} else {
		for i := headerIdx + 1; i < len(lines); i++ {
			if agyRuleLine.MatchString(lines[i]) || agyPromptLine.MatchString(lines[i]) {
				end = i
				break
			}
		}
	}

	var body []string
	spinning := false
	for _, raw := range lines[headerIdx+1 : end] {
		clean := agent.StripSideOverlayBorders(raw)
		if agySpinnerLine.MatchString(clean) {
			spinning = true
			continue
		}
		low := strings.ToLower(clean)
		if strings.HasPrefix(low, "? for shortcuts") || strings.HasPrefix(low, "esc to cancel") {
			continue
		}
		body = append(body, raw)
	}

	answer, loadingOnly := agent.CleanSideAnswer(body)
	if footerIdx >= 0 && answer != "" && !loadingOnly && !spinning {
		return answer, agent.SideQuestionDone
	}
	return answer, agent.SideQuestionPending
}

// SideQuestionDismiss returns Escape only while agy's `/btw` overlay is visible.
func (a *Agent) SideQuestionDismiss(screen string) []byte {
	if _, st := a.SideQuestionParse(screen); st != agent.SideQuestionNone {
		return []byte("\x1b")
	}
	return nil
}
