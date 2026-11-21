package claude

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Claude Code writes more than the user typed into a user's turn, each
// part in its own tag: a slash command as its name and arguments, what a
// local command printed, a background task's notice, notes for the model.
// The chat shows them as the user saw them: the command as typed, the
// output and the notice as rows of their own, and the notes not at all.

// hidden are the tags whose content is for the model alone.
var hidden = []string{"system-reminder", "local-command-caveat"}

// midTurn opens the reminder that carries a message the user sent while
// the agent worked, and midTurnEnd the explanation after the message.
const (
	midTurn    = "The user sent a new message while you were working:\n"
	midTurnEnd = "\n\nThis is how Claude Code surfaces"
)

// pluginTurn and midTurnPlugin open the envelope Claude Code wraps around a
// prompt submitted by a plugin (such as mako-live) between turns or mid-turn.
var (
	pluginTurn    = regexp.MustCompile(`^The [^\n]+ plugin sent a message:\n`)
	midTurnPlugin = regexp.MustCompile(`^The [^\n]+ plugin sent a message while you were working:\n`)
)

// pasted matches the tags Claude Code wraps pasted text in.
var pasted = regexp.MustCompile(`</?pasted_content[^>]*>`)

// unwrapPluginPrompt strips Claude Code's "The <name> plugin sent a message:"
// envelope and its trailing "This is how Claude Code surfaces..." note.
func unwrapPluginPrompt(s string) (string, bool) {
	loc := pluginTurn.FindStringIndex(s)
	if loc == nil {
		return "", false
	}
	body := s[loc[1]:]
	end := strings.LastIndex(body, midTurnEnd)
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(body[:end]), true
}

// inner is the content of s's first <name> tag, trimmed.
func inner(s, name string) (string, bool) {
	_, rest, ok := strings.Cut(s, "<"+name+">")
	if !ok {
		return "", false
	}
	body, _, _ := strings.Cut(rest, "</"+name+">")
	return strings.TrimSpace(body), true
}

// midTurnMessages are the messages the user or a plugin sent while the agent
// worked, which Claude Code hands it in reminders.
func midTurnMessages(s string) []*protocol.ChatMessage {
	var out []*protocol.ChatMessage
	for {
		_, rest, ok := strings.Cut(s, "<system-reminder>")
		if !ok {
			return out
		}
		body, after, _ := strings.Cut(rest, "</system-reminder>")
		trimmed := strings.TrimSpace(body)
		var text string
		var matched bool
		if rest, ok := strings.CutPrefix(trimmed, midTurn); ok {
			text, matched = rest, true
		} else if loc := midTurnPlugin.FindStringIndex(trimmed); loc != nil {
			text, matched = trimmed[loc[1]:], true
		}
		if matched {
			if end := strings.LastIndex(text, midTurnEnd); end >= 0 {
				text = text[:end]
			}
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, &protocol.ChatMessage{Role: "user", Text: text})
			}
		}
		s = after
	}
}

// unwrap turns the tagged parts of the messages read from a record into
// what the user saw. command is the last slash command typed, whose output
// a later record holds.
func unwrap(messages []*protocol.ChatMessage, command *string, pluginText string) []*protocol.ChatMessage {
	var out []*protocol.ChatMessage
	for _, m := range messages {
		switch m.GetRole() {
		case "user":
			out = append(out, userTurn(m.GetText(), command, pluginText)...)
		case "tool":
			out = append(out, m)
			if m.GetOutput() {
				out = append(out, midTurnMessages(m.GetText())...)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

// userTurn is a user's turn as the user saw it.
func userTurn(text string, command *string, pluginText string) []*protocol.ChatMessage {
	out := midTurnMessages(text)
	text = agent.DropTags(text, hidden...)
	text = strings.TrimSpace(pasted.ReplaceAllString(text, ""))
	if pluginTurn.MatchString(text) {
		if pluginText != "" {
			text = strings.TrimSpace(pluginText)
		} else if unwrapped, ok := unwrapPluginPrompt(text); ok {
			text = unwrapped
		}
	}
	// A turn is a command, its output or a notice only when it opens with
	// the tag: the same tags pasted into a message are the message.
	leads := func(names ...string) bool {
		for _, n := range names {
			if strings.HasPrefix(text, "<"+n+">") {
				return true
			}
		}
		return false
	}
	user := func(s string) *protocol.ChatMessage { return &protocol.ChatMessage{Role: "user", Text: s} }
	output := func(tool, kind, s string, failed bool) *protocol.ChatMessage {
		s = strings.TrimSpace(agent.StripANSI(s))
		return &protocol.ChatMessage{Role: "tool", Tool: tool, Kind: kind, Detail: agent.OneLine(s, 120), Text: s, Output: true, Error: failed}
	}
	if name, ok := inner(text, "command-name"); ok && leads("command-name", "command-message") {
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		*command = name
		if args, _ := inner(text, "command-args"); args != "" {
			name += " " + args
		}
		return append(out, user(name))
	}
	if leads("local-command-stdout", "local-command-stderr") {
		stdout, _ := inner(text, "local-command-stdout")
		stderr, _ := inner(text, "local-command-stderr")
		return append(out, output(cmp.Or(*command, "Command"), protocol.ToolCommand, strings.TrimSpace(stdout+"\n"+stderr), stderr != ""))
	}
	if cmd, ok := inner(text, "bash-input"); ok && leads("bash-input") {
		*command = "Bash"
		return append(out, user("! "+cmd))
	}
	if leads("bash-stdout", "bash-stderr") {
		stdout, _ := inner(text, "bash-stdout")
		stderr, _ := inner(text, "bash-stderr")
		return append(out, output("Bash", protocol.ToolShell, strings.TrimSpace(stdout+"\n"+stderr), stderr != ""))
	}
	if body, ok := inner(text, "task-notification"); ok && leads("task-notification") {
		summary, _ := inner(body, "summary")
		result, _ := inner(body, "result")
		status, _ := inner(body, "status")
		m := output("Task", protocol.ToolTask, cmp.Or(result, summary), status == "failed" || status == "killed")
		m.Detail = cmp.Or(summary, m.GetDetail())
		return append(out, m)
	}
	if text != "" {
		out = append(out, user(text))
	}
	return out
}
