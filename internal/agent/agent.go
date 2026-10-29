// Package agent is what mako asks of an agent it works with: what it is,
// how its command line picks a conversation, how it takes MCP servers and
// hooks, what its hooks report, and how its logs read. Each agent answers
// in a driver of its own, in a package under this one, and the agents
// package lists them. Only Info is asked of every driver; the rest are
// capabilities a driver has or lacks, and mako follows of an agent what
// its driver has.
package agent

import (
	"encoding/json"
	"image/color"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// Driver is one agent mako knows.
type Driver interface {
	Info() Info
}

// Info is what an agent is.
type Info struct {
	Name string // as a launch target names it: claude
	// Programs are the executables that are the agent, without a
	// Windows .exe: a launch runs the agent when it runs one of them.
	Programs []string
	Command  []string // the command line a fresh configuration launches it with
	// Dirs are the folders, under the home folder, its own installer puts
	// it in, as well as the ones installers share (Find).
	Dirs  []string
	Icon  string // the GUI's glyph for it
	Color color.NRGBA
}

// Launcher reads and writes the command-line options that pick the
// conversation a launch starts or continues.
type Launcher interface {
	// ArgumentID is the conversation command explicitly picks, "" for none
	// or for a fork's source, which is not the new process's.
	ArgumentID(command []string) string
	// Picks reports whether command already picks its conversation.
	Picks(command []string) bool
	// Pin starts command as conversation id, chosen before the agent writes
	// anything; false for an agent that always picks its own.
	Pin(command []string, id string) ([]string, bool)
	// Resume is command continuing conversation id: its settings kept, its
	// prompt and the options that picked another conversation dropped.
	Resume(command []string, id string) []string
	// Isolate is a flag that keeps the log's writer in the session's process
	// tree, so a session binds without guessing; "" for none. Not every
	// version of an agent need have it.
	Isolate() string
}

// Launch is how mako starts an agent: its command line, and the variables
// it adds to the agent's environment. Readying a launch changes both.
type Launch struct {
	Command []string
	Env     map[string]string
}

// SetEnv sets an environment variable of the launch.
func (l *Launch) SetEnv(key, value string) {
	if l.Env == nil {
		l.Env = map[string]string{}
	}
	l.Env[key] = value
}

// Server is an MCP server, as a launch gives it to an agent.
type Server struct {
	ID      string
	Command string
	Args    []string
	Env     map[string]string
}

// ArgList is the server's arguments, empty rather than nil, as a
// configuration lists them.
func (s Server) ArgList() []string {
	if s.Args == nil {
		return []string{}
	}
	return s.Args
}

// Servers gives an agent MCP servers as it starts.
type Servers interface {
	// WithServers gives l servers, and returns what to clean up after the
	// session. Any file it writes goes in dir, named for session.
	WithServers(l *Launch, servers []Server, dir, session string) (func(), error)
}

// Hooks has the agent run mako's hook on the events mako follows it by.
type Hooks interface {
	// WithHooks has l run the shell command hook on the agent's events,
	// alongside the user's own hooks, and status as its status line unless
	// status is "" or the agent has none. false when l brings settings of
	// its own that mako's would replace. Any file it writes goes in dir.
	WithHooks(l *Launch, hook, status, dir string) bool
	// Event reads what the agent hands the hook. false for input it cannot
	// read, an event mako does not follow, or a subagent's.
	Event(input []byte) (Event, bool)
	// Reply is what the hook answers an approval with: decision is
	// protocol's allow or deny, and message why it denies.
	Reply(decision, message string) []byte
}

// HookGate is an agent not every version of which takes hooks.
type HookGate interface {
	// HookFeature is the command that lists the agent's features, and the
	// feature hooks need.
	HookFeature() (args []string, feature string)
}

// StatusLine is an agent that runs mako as its status line, which tells
// what its own logs do not.
type StatusLine interface {
	// Status is what the status line's input says of the session's tokens.
	Status(input []byte) *protocol.Usage
	// UserStatusLine is the status line command the user's own settings
	// name, given the status line's input; "" for none.
	UserStatusLine(input []byte) string
}

// Logs is an agent that writes its conversations down, a JSON record a
// line, one conversation a file.
type Logs interface {
	Place() Place
	// LogName is the file name of conversation id's log, "" when the name
	// is not the ID's alone: then a log path a hook reports is not trusted.
	LogName(id string) string
	// Identity reads a log's line for whose it is: its conversation's ID,
	// working directory and creation time. done is false to read on, and
	// true at the line that says, or that says the log is not one mako
	// reads, with no ID.
	Identity(line []byte) (id, cwd string, created time.Time, done bool)
	// NewDecoder reads one pass over a log's lines.
	NewDecoder() Decoder
	// NewUsage counts one log's tokens, line by line.
	NewUsage() Usage
}

// Folders is a Logs that keeps each conversation's log in a folder named
// by its ID, whose lines do not say whose it is.
type Folders interface {
	// LogPath is the path of conversation id's log under root; id "*"
	// globs every conversation's.
	LogPath(root, id string) string
	// Whose is whose the log at path is, as Identity reads it from a
	// line; no ID for a path that is not one of the agent's logs.
	Whose(path string) (id, cwd string, created time.Time)
}

// Decoder reads a log's lines, in order, into the chat's messages. It keeps
// what one line says of the next.
type Decoder interface {
	Line(line []byte) []*protocol.ChatMessage
}

// Usage counts what a log's lines say of its tokens into u.
type Usage interface {
	Count(line []byte, u *protocol.Usage)
}

// SubAgents is an agent whose calls start agents of their own, with logs
// of their own.
type SubAgents interface {
	NewSubAgents() SubAgentScan
}

// SubAgentLogs is a SubAgents whose subagents' logs read otherwise than
// its sessions', as Claude's, whose every record is a sidechain.
type SubAgentLogs interface {
	// NewAgentDecoder reads one pass over a subagent's log.
	NewAgentDecoder() Decoder
}

// SubAgentScan follows one conversation's subagents, reading again only
// what changed.
type SubAgentScan interface {
	// Read is the subagents the calls in messages started, oldest first;
	// path is the conversation's log.
	Read(path string, messages []*protocol.ChatMessage) []*protocol.SubAgent
}

// Tools names what an agent's tools do.
type Tools interface {
	// Tool is what a call of the tool name does, one of protocol's Tool
	// kinds, and its arguments in the form protocol gives that kind; nil
	// arguments for a tool whose arguments stay as they are.
	Tool(name string, args json.RawMessage) (kind string, canon map[string]any)
	// Output is what a call of kind gave back without what the agent wraps
	// it in, and the exit code it reports, -1 for none.
	Output(kind, text string) (out string, exit int)
}

// Caps are the protocol's Cap bits for what d has.
func Caps(d Driver) uint32 {
	var caps uint32
	if _, ok := d.(Logs); ok {
		caps |= protocol.CapChat
	}
	if _, ok := d.(Hooks); ok {
		caps |= protocol.CapHooks
	}
	if _, ok := d.(SubAgents); ok {
		caps |= protocol.CapSubAgents
	}
	return caps
}
