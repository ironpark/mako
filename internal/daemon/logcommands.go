package daemon

import (
	"slices"
	"sync"

	"github.com/ironpark/mako/internal/agent"
)

// loggedCommands keeps the commands each session's log says its agent
// takes (agent.LoggedCommands), as the reads of its activity find them;
// and the latest each agent's logs said, for a session whose log has not
// said yet, as a new one's has not before its first prompt.
type loggedCommands struct {
	mu        sync.Mutex
	bySession map[string][]agent.Command
	latest    map[string][]agent.Command // by agent
}

// set notes what session's log, of the agent called name, said.
func (l *loggedCommands) set(session, name string, commands []agent.Command) {
	if len(commands) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bySession == nil {
		l.bySession, l.latest = map[string][]agent.Command{}, map[string][]agent.Command{}
	}
	l.bySession[session], l.latest[name] = commands, commands
}

// of are the commands session's log said, else the latest the logs of the
// agent called name said.
func (l *loggedCommands) of(session, name string) []agent.Command {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.bySession[session]; len(c) > 0 {
		return slices.Clone(c)
	}
	return slices.Clone(l.latest[name])
}

// forget drops a session's, as it closes.
func (l *loggedCommands) forget(session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.bySession, session)
}

// withLogged is commands, then those logged that none of them names.
func withLogged(commands, logged []agent.Command) []agent.Command {
	for _, c := range logged {
		if !slices.ContainsFunc(commands, func(k agent.Command) bool { return k.Name == c.Name }) {
			commands = append(commands, c)
		}
	}
	return commands
}
