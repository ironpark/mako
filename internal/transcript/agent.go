package transcript

import (
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
)

// logsOf is the logs of the agent called name, when it writes them down.
func logsOf(name string) (agent.Logs, bool) { return agents.As[agent.Logs](name) }

// Supported reports whether agent writes a native log this package reads.
func Supported(agent string) bool {
	_, ok := logsOf(agent)
	return ok
}

// Env is the part of a process's environment that says where agents' logs
// are: environ with overrides applied. Credentials stay out.
func Env(environ []string, overrides map[string]string) map[string]string {
	out := map[string]string{}
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok && envKey(key) {
			out[key] = value
		}
	}
	for key, value := range overrides {
		if envKey(key) {
			out[key] = value
		}
	}
	return out
}

func envKey(key string) bool {
	if key == "HOME" {
		return true
	}
	for _, d := range agents.All() {
		if l, ok := d.(agent.Logs); ok && l.Place().RootEnv == key {
			return true
		}
	}
	return false
}
