// Package agentspec resolves an agent name, and one of its profiles, to the
// command and environment that launch it.
package agentspec

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agents"
)

// Agent is how a node launches one agent: its command, the flags and
// environment every session of it gets, and named profiles that add their
// own on top.
type Agent struct {
	Command  []string          `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Profiles []Profile         `json:"profiles,omitempty"`
}

// Profile is a named set of flags and environment for an agent, such as a
// claude that runs another model or with another account's key.
type Profile struct {
	Name string            `json:"name"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	// ContextServers picks the MCP servers its sessions get, by ID, an
	// empty list none; unset, they get every server enabled. A server
	// picked here is given even when turned off for the node.
	ContextServers *[]string `json:"context_servers,omitempty"`
}

// UnmarshalJSON also reads an agent written as its command alone, as
// configurations did before agents had flags and profiles.
func (a *Agent) UnmarshalJSON(b []byte) error {
	var command []string
	if err := json.Unmarshal(b, &command); err == nil {
		*a = Agent{Command: command}
		return nil
	}
	type plain Agent
	return json.Unmarshal(b, (*plain)(a))
}

// Profile returns the profile called name.
func (a Agent) Profile(name string) (Profile, bool) {
	i := slices.IndexFunc(a.Profiles, func(p Profile) bool { return p.Name == name })
	if i < 0 {
		return Profile{}, false
	}
	return a.Profiles[i], true
}

// ProfileNames lists the agent's profiles in order.
func (a Agent) ProfileNames() []string {
	names := make([]string, len(a.Profiles))
	for i, p := range a.Profiles {
		names[i] = p.Name
	}
	return names
}

// Builtin holds the default command line for each known terminal agent,
// and for the shell.
var Builtin = builtin()

func builtin() map[string][]string {
	out := map[string][]string{"shell": {defaultShell()}}
	for _, d := range agents.All() {
		out[d.Info().Name] = d.Info().Command
	}
	return out
}

// Defaults is an agent for each built-in, as a fresh configuration has.
func Defaults() map[string]Agent {
	out := make(map[string]Agent, len(Builtin))
	for name, command := range Builtin {
		out[name] = Agent{Command: slices.Clone(command)}
	}
	return out
}

// Lookup is the agent called name: the configured one, else the built-in.
// A configured built-in without a command of its own keeps the built-in's,
// so a node can give claude flags or profiles without restating it.
func Lookup(name string, agents map[string]Agent) (Agent, bool) {
	if a, ok := agents[name]; ok {
		if len(a.Command) == 0 {
			a.Command = Builtin[name]
		}
		return a, true
	}
	if command, ok := Builtin[name]; ok {
		return Agent{Command: command}, true
	}
	return Agent{}, false
}

// Launch is what starts a session: the command with its path resolved,
// and the environment it adds to the daemon's.
type Launch struct {
	Command []string
	Env     map[string]string
	// ContextServers are the MCP servers its profile picks, nil for the
	// node's enabled ones.
	ContextServers *[]string
}

// Resolve turns an agent name, a profile of it ("" for none) and extra
// arguments and environment into a command. Flags go agent, profile, then
// the request's, so a later one can override an earlier where the program
// takes the last; environment is merged in the same order.
//
// No shell starts the command, so the configured flags and environment
// values are expanded here as one would: $NAME and ${NAME} from the layers
// before, the daemon's environment under the agent's and the agent's under
// a profile's (not from their own, as in env A=1 B=$A), a leading ~ to the
// home directory, and $$ to a $. The request's own are taken as they are, as a
// shell has expanded them already.
func Resolve(name, profile string, args []string, env map[string]string, agents map[string]Agent) (Launch, error) {
	if name == "" {
		name = "shell"
	}
	a, ok := Lookup(name, agents)
	if !ok {
		return Launch{}, fmt.Errorf("unknown agent %q", name)
	}
	if len(a.Command) == 0 {
		return Launch{}, fmt.Errorf("agent %q has an empty command", name)
	}
	var p Profile
	if profile != "" {
		if p, ok = a.Profile(profile); !ok {
			return Launch{}, fmt.Errorf("agent %q has no profile %q", name, profile)
		}
	}
	path, err := exec.LookPath(a.Command[0])
	if err != nil {
		return Launch{}, fmt.Errorf("agent %q: %w", name, err)
	}
	var merged map[string]string
	lookup := func(name string) string {
		if v, ok := merged[name]; ok {
			return v
		}
		return os.Getenv(name)
	}
	for _, layer := range []map[string]string{a.Env, p.Env} {
		expanded := make(map[string]string, len(layer))
		for k, v := range layer {
			expanded[k] = expand(v, lookup)
		}
		if len(expanded) > 0 && merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, expanded)
	}
	if len(env) > 0 && merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, env)

	command := append([]string{path}, a.Command[1:]...)
	for _, arg := range slices.Concat(a.Args, p.Args) {
		command = append(command, expand(arg, lookup))
	}
	command = append(command, args...)
	return Launch{Command: command, Env: merged, ContextServers: p.ContextServers}, nil
}

// expand writes $NAME, ${NAME} and a leading ~ out, and $$ as $.
func expand(s string, lookup func(string) string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	if !strings.Contains(s, "$") {
		return s
	}
	const dollar = "\x00"
	s = strings.ReplaceAll(s, "$$", dollar)
	s = os.Expand(s, lookup)
	return strings.ReplaceAll(s, dollar, "$")
}

// Names lists the agents a node can launch, sorted.
func Names(agents map[string]Agent) []string {
	names := slices.Collect(maps.Keys(Builtin))
	for name := range agents {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// A target names what to launch: an agent, or one of its profiles as
// agent:profile, as `mako run claude:opus` and the GUI's picker write it.

// Target joins an agent and a profile into a target.
func Target(agent, profile string) string {
	if profile == "" {
		return agent
	}
	return agent + ":" + profile
}

// SplitTarget is Target's inverse.
func SplitTarget(target string) (agent, profile string) {
	agent, profile, _ = strings.Cut(target, ":")
	return agent, profile
}

// Targets lists every target of the agents a node can launch: each agent,
// then each of its profiles.
func Targets(agents map[string]Agent) []string {
	var out []string
	for _, name := range Names(agents) {
		out = append(out, name)
		a, _ := Lookup(name, agents)
		for _, p := range a.Profiles {
			out = append(out, Target(name, p.Name))
		}
	}
	return out
}

// ValidName reports whether s can name an agent or a profile: not empty,
// and without the colon a target joins them with or any space.
func ValidName(s string) bool {
	return s != "" && !strings.ContainsAny(s, ": \t\n")
}

// defaultShell is the user's login shell, else the platform's.
func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}
