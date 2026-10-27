package transcript

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// Claude Code writes more than the user typed into a user's turn, each
// part in its own tag: a slash command as its name and arguments, what a
// local command printed, a background task's notice, notes for the model.
// The chat shows them as the user saw them: the command as typed, the
// output and the notice as rows of their own, and the notes not at all.

// hidden are the tags whose content is for the model alone.
var hidden = []string{"system-reminder", "local-command-caveat", "environment_context", "user_instructions"}

// midTurn opens the reminder that carries a message the user sent while
// the agent worked, and midTurnEnd the explanation after the message.
const (
	midTurn    = "The user sent a new message while you were working:\n"
	midTurnEnd = "\n\nThis is how Claude Code surfaces"
)

// pasted matches the tags Claude Code wraps pasted text in.
var pasted = regexp.MustCompile(`</?pasted_content[^>]*>`)

// ansi matches the escape sequences a local command colors its output with.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// inner is the content of s's first <name> tag, trimmed.
func inner(s, name string) (string, bool) {
	_, rest, ok := strings.Cut(s, "<"+name+">")
	if !ok {
		return "", false
	}
	body, _, _ := strings.Cut(rest, "</"+name+">")
	return strings.TrimSpace(body), true
}

// drop is s without its <name> tags and their content.
func drop(s, name string) string {
	open, end := "<"+name+">", "</"+name+">"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], end)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(end):]
	}
}

// midTurnMessages are the messages the user sent while the agent worked,
// which Claude Code hands it in reminders.
func midTurnMessages(s string) []*protocol.ChatMessage {
	var out []*protocol.ChatMessage
	for {
		_, rest, ok := strings.Cut(s, "<system-reminder>")
		if !ok {
			return out
		}
		body, after, _ := strings.Cut(rest, "</system-reminder>")
		if text, ok := strings.CutPrefix(strings.TrimSpace(body), midTurn); ok {
			text, _, _ = strings.Cut(text, midTurnEnd)
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
func unwrap(messages []*protocol.ChatMessage, command *string) []*protocol.ChatMessage {
	var out []*protocol.ChatMessage
	for _, m := range messages {
		switch m.GetRole() {
		case "user":
			out = append(out, userTurn(m.GetText(), command)...)
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
func userTurn(text string, command *string) []*protocol.ChatMessage {
	out := midTurnMessages(text)
	for _, name := range hidden {
		text = drop(text, name)
	}
	text = strings.TrimSpace(pasted.ReplaceAllString(text, ""))
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
	output := func(tool, s string, failed bool) *protocol.ChatMessage {
		s = strings.TrimSpace(ansi.ReplaceAllString(s, ""))
		return &protocol.ChatMessage{Role: "tool", Tool: tool, Detail: OneLine(s, 120), Text: s, Output: true, Error: failed}
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
		return append(out, output(cmp.Or(*command, "Command"), strings.TrimSpace(stdout+"\n"+stderr), stderr != ""))
	}
	if cmd, ok := inner(text, "bash-input"); ok && leads("bash-input") {
		*command = "Bash"
		return append(out, user("! "+cmd))
	}
	if leads("bash-stdout", "bash-stderr") {
		stdout, _ := inner(text, "bash-stdout")
		stderr, _ := inner(text, "bash-stderr")
		return append(out, output("Bash", strings.TrimSpace(stdout+"\n"+stderr), stderr != ""))
	}
	if body, ok := inner(text, "task-notification"); ok && leads("task-notification") {
		summary, _ := inner(body, "summary")
		result, _ := inner(body, "result")
		status, _ := inner(body, "status")
		m := output("Task", cmp.Or(result, summary), status == "failed" || status == "killed")
		m.Detail = cmp.Or(summary, m.GetDetail())
		return append(out, m)
	}
	if text != "" {
		out = append(out, user(text))
	}
	return out
}
