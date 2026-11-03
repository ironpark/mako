package daemon

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/ironpark/mako/internal/agent"
)

// loggedCommands keeps the commands agents' logs say they take
// (agent.LoggedCommands), each log read on from where it was last, as it
// only grows; and the latest each agent's logs said, for a session whose
// log has not said yet, as a new one's has not before its first prompt.
type loggedCommands struct {
	mu     sync.Mutex
	logs   map[string]*commandLog
	latest map[string][]agent.Command // by agent
}

// commandLog is what is read of one log: up to offset, the end of its last
// whole line, the commands it named, in the order first named.
type commandLog struct {
	offset   int64
	commands []agent.Command
}

// of are the commands the log at path says the agent called name takes,
// read by driver; the latest its logs said when path is "" or says none.
func (l *loggedCommands) of(driver agent.LoggedCommands, name, path string) []agent.Command {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logs == nil {
		l.logs, l.latest = map[string]*commandLog{}, map[string][]agent.Command{}
	}
	if path == "" {
		return slices.Clone(l.latest[name])
	}
	log := l.logs[path]
	if log == nil {
		log = &commandLog{}
		l.logs[path] = log
	}
	log.read(driver, path)
	if len(log.commands) > 0 {
		l.latest[name] = log.commands
	}
	// A copy: the log's are changed in place as it is read on.
	return slices.Clone(l.latest[name])
}

// read reads what was written to the log at path since it was last read,
// whole lines only.
func (c *commandLog) read(driver agent.LoggedCommands, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() < c.offset {
		c.offset, c.commands = 0, nil // written anew
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // a line not yet whole is read once it is
		}
		c.offset += int64(len(line))
		for _, cmd := range driver.LogCommands(bytes.TrimSpace(line)) {
			if i := slices.IndexFunc(c.commands, func(k agent.Command) bool { return k.Name == cmd.Name }); i >= 0 {
				c.commands[i] = cmd
			} else {
				c.commands = append(c.commands, cmd)
			}
		}
	}
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
