package codex

import (
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
)

var (
	codexSideHeader = regexp.MustCompile(`(?i)^(?:/side(?:\s|$)|/btw(?:\s|$)|side\s+conversation\b)`)
	codexSideFooter = regexp.MustCompile(`(?i)(?:press\s+)?esc\s+to\s+(?:close|dismiss|return)\b`)
	codexSideTimer  = regexp.MustCompile(`(?i)\((?:[0-9]+[hm] )*[0-9]+s(?:\s*•\s*[^)]*to interrupt)?\)`)
)

// SideQuestionCanAsk reports whether Codex's terminal screen can safely accept
// a native `/side` command without answering a dialog or colliding with an
// overlay or compaction.
func (a *Agent) SideQuestionCanAsk(screen, title string) (bool, string) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return false, "the agent screen is not ready"
	}
	if _, st := a.SideQuestionParse(trimmed); st != agent.SideQuestionNone {
		return false, "a side conversation overlay is already open"
	}
	for _, in := range []detect.Input{
		{Screen: trimmed},
		{Screen: trimmed, Title: title},
	} {
		r := screenRules.Detect(in)
		if r.ID == "side_overlay" {
			return false, "a side conversation overlay is already open"
		}
		if r.State == detect.Blocked {
			return false, "the agent is waiting on an approval, dialog, or form"
		}
		if r.Hold {
			return false, "a modal view is open over the prompt"
		}
	}
	if agent.ScreenShowsCompacting(trimmed) {
		return false, "the agent is compacting the conversation"
	}
	lines := strings.Split(trimmed, "\n")
	if _, ok := currentPrompt(lines); !ok {
		return false, "the agent prompt is not ready"
	}
	after := strings.ToLower(afterPrompt(detect.Input{Screen: trimmed}))
	for _, blocked := range []string{
		"enter to confirm",
		"esc to cancel",
		"enter to submit",
		"enter to select",
		"allow command?",
	} {
		if strings.Contains(after, blocked) {
			return false, "the agent is waiting on an approval, dialog, or form"
		}
	}
	return true, ""
}

// SideQuestionCommand formats question as Codex's native `/side` command.
func (*Agent) SideQuestionCommand(question string) string {
	q := agent.NormalizeSideQuestion(question)
	if q == "" {
		return ""
	}
	return "/side " + q
}

// SideQuestionParse reads Codex's `/side` (or `/btw`) overlay from screen.
func (*Agent) SideQuestionParse(screen string) (string, agent.SideQuestionState) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return "", agent.SideQuestionNone
	}
	lines := strings.Split(trimmed, "\n")
	scanStart := max(len(lines)-40, 0)
	headerIdx := -1
	for i := len(lines) - 1; i >= scanStart; i-- {
		raw := strings.TrimSpace(lines[i])
		if promptLine(raw) {
			continue
		}
		clean := agent.StripSideOverlayBorders(lines[i])
		if codexSideHeader.MatchString(clean) {
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
		if codexSideFooter.MatchString(clean) {
			footerIdx = i
			break
		}
	}

	end := len(lines)
	if footerIdx >= 0 {
		end = footerIdx
	}

	var body []string
	working := false
	for _, raw := range lines[headerIdx+1 : end] {
		clean := agent.StripSideOverlayBorders(raw)
		if clean == "" {
			body = append(body, "")
			continue
		}
		if promptLine(clean) {
			continue
		}
		if codexSideTimer.MatchString(clean) {
			working = true
			continue
		}
		if strings.HasPrefix(clean, "• ") {
			clean = strings.TrimSpace(strings.TrimPrefix(clean, "• "))
		}
		body = append(body, clean)
	}

	answer, loadingOnly := agent.CleanSideAnswer(body)
	if footerIdx >= 0 && answer != "" && !loadingOnly && !working {
		return answer, agent.SideQuestionDone
	}
	return answer, agent.SideQuestionPending
}

// SideQuestionDismiss returns Escape only while Codex's side overlay is visible.
func (a *Agent) SideQuestionDismiss(screen string) []byte {
	if _, st := a.SideQuestionParse(screen); st != agent.SideQuestionNone {
		return []byte("\x1b")
	}
	return nil
}
