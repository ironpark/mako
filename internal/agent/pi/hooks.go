package pi

import (
	"encoding/json"
	"fmt"

	"github.com/ironpark/mako/internal/agent"
)

// pi and omp take no hook commands: they load extensions, and an extension
// hears their events. mako writes one that runs its hook on each, the
// input as Claude Code hands its hooks, so CommonHooks reads it. It names
// the session by its ID and its log, which the session manager knows.

// events are the extension's events mako hooks, by the hook event each
// stands for; the agent's settled event ends a prompt's work.
var events = [][2]string{
	{"session_start", "SessionStart"},
	{"agent_start", "UserPromptSubmit"},
	{"tool_execution_end", "PostToolUse"},
	{"session_before_compact", "PreCompact"},
	{"session_shutdown", "SessionEnd"},
}

// extension is the extension's source after HookScript: hook, a shell
// command, runs on each of events.
const extension = `
const hook = %s;
const events = %s;

export default function (pi) {
	for (const [event, name] of events) {
		pi.on(event, (e, ctx) => {
			const sessions = ctx && ctx.sessionManager;
			runHook(hook, {
				hook_event_name: name,
				session_id: (sessions && sessions.getSessionId && sessions.getSessionId()) || "",
				transcript_path: (sessions && sessions.getSessionFile && sessions.getSessionFile()) || "",
				...(e && e.toolName ? { tool_name: e.toolName } : {}),
			});
		});
	}
}
`

// WithHooks has the agent load an extension running hook on its events,
// written in dir. The status line is Claude's alone.
func (a *Agent) WithHooks(l *agent.Launch, hook, _, dir string) bool {
	hooked := append(events[:len(events):len(events)], [2]string{a.settled, "Stop"})
	hookJSON, _ := json.Marshal(hook)
	eventsJSON, _ := json.Marshal(hooked)
	source := "// Written by mako: it runs mako's hook on this agent's events.\n" + agent.HookScript + fmt.Sprintf(extension, hookJSON, eventsJSON)
	path, err := agent.WriteOnce(dir, a.info.Name+"-hooks", ".js", source)
	if err != nil {
		return false
	}
	l.Command = withExtension(l.Command, path)
	return true
}

// withExtension is command loading the extension at path as well.
func withExtension(command []string, path string) []string {
	return append([]string{command[0], "--extension", path}, command[1:]...)
}
