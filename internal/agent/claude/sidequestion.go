package claude

import (
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
)

var (
	claudeBtwHeader = regexp.MustCompile(`(?i)^(?:/btw(?:\s|$)|btw\s*[:·])`)
	claudeBtwFooter = regexp.MustCompile(`(?i)(?:press\s+)?(?:esc(?:\s*(?:or|/)\s*(?:enter|space))?|enter(?:\s*(?:or|/)\s*esc)?)\s+to\s+(?:close|dismiss)`)
	claudePromptRow = regexp.MustCompile(`^\s*❯(?:\s|$)`)
)

// SideQuestionCanAsk reports whether Claude Code's terminal screen can safely
// accept a `/btw <question>` command without answering a dialog or colliding
// with an overlay or compaction.
func (a *Agent) SideQuestionCanAsk(screen, title string) (bool, string) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return false, "the agent screen is not ready"
	}
	if _, st := a.SideQuestionParse(trimmed); st != agent.SideQuestionNone {
		return false, "a side question overlay is already open"
	}
	// Check screen-only rules first so a stale working title spinner never masks
	// a lower-priority permission dialog or form on screen.
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
	}
	if a.Recall(trimmed) != nil {
		return false, "another view is open over the main prompt"
	}
	if agent.ScreenShowsCompacting(trimmed) {
		return false, "the agent is compacting the conversation"
	}
	if !hasClaudePromptBox(trimmed) {
		return false, "the agent prompt is not ready"
	}
	return true, ""
}

func hasClaudePromptBox(screen string) bool {
	// Reject if the prompt area contains picker/dialog navigation cues.
	pb := strings.ToLower(detect.PromptBox(detect.Input{Screen: screen}))
	for _, blocked := range []string{
		"enter to select",
		"enter to confirm",
		"esc to cancel",
		"tab/arrow keys",
		"arrow keys to navigate",
		"↑/↓ to navigate",
		"do you want to proceed?",
		"waiting for permission",
	} {
		if strings.Contains(pb, blocked) {
			return false
		}
	}
	lines := strings.Split(screen, "\n")
	start := max(len(lines)-12, 0)
	for _, l := range lines[start:] {
		if claudePromptRow.MatchString(l) {
			return true
		}
	}
	return false
}

// SideQuestionCommand formats question as Claude Code's native `/btw` command.
func (*Agent) SideQuestionCommand(question string) string {
	q := agent.NormalizeSideQuestion(question)
	if q == "" {
		return ""
	}
	return "/btw " + q
}

// SideQuestionParse reads Claude Code's `/btw` overlay from screen, returning
// the extracted answer and whether the overlay is absent, loading, or done.
func (*Agent) SideQuestionParse(screen string) (string, agent.SideQuestionState) {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return "", agent.SideQuestionNone
	}
	lines := strings.Split(trimmed, "\n")
	// Scan from the bottom within a bounded window so old scrollback lines
	// mentioning "/btw" do not masquerade as an active overlay.
	scanStart := max(len(lines)-40, 0)
	headerIdx := -1
	for i := len(lines) - 1; i >= scanStart; i-- {
		clean := agent.StripSideOverlayBorders(lines[i])
		if claudeBtwHeader.MatchString(clean) {
			// Skip if this is just the user still typing "/btw ..." inside the
			// prompt line ("❯ /btw ...").
			if claudePromptRow.MatchString(lines[i]) {
				continue
			}
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
		if claudeBtwFooter.MatchString(clean) {
			footerIdx = i
			break
		}
	}

	end := len(lines)
	if footerIdx >= 0 {
		end = footerIdx
	} else {
		// Stop before an underlying prompt box if no close footer is shown yet.
		for i := headerIdx + 1; i < len(lines); i++ {
			if claudePromptRow.MatchString(lines[i]) {
				end = i
				break
			}
		}
	}

	var body []string
	for _, raw := range lines[headerIdx+1 : end] {
		low := strings.ToLower(strings.TrimSpace(raw))
		if strings.HasPrefix(low, "? for shortcuts") || strings.Contains(low, "esc to interrupt") {
			continue
		}
		body = append(body, raw)
	}

	answer, loadingOnly := agent.CleanSideAnswer(body)
	if footerIdx >= 0 && answer != "" && !loadingOnly {
		return answer, agent.SideQuestionDone
	}
	return answer, agent.SideQuestionPending
}

// SideQuestionDismiss returns Escape only while Claude Code's `/btw` overlay
// is currently visible on screen.
func (a *Agent) SideQuestionDismiss(screen string) []byte {
	if _, st := a.SideQuestionParse(screen); st != agent.SideQuestionNone {
		return []byte("\x1b")
	}
	return nil
}
