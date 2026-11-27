// Package agentspec resolves an agent name, and one of its profiles, to the
// command and environment that launch it.
package agentspec

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
)

// Canonical permission values shared by Agent and Profile configuration.
const (
	// PermissionDefault explicitly selects the agent's built-in normal mode
	// without YOLO (used on profiles to override an agent-level permission/YOLO).
	PermissionDefault = "default"
	// PermissionYOLO bypasses permission prompts and sandboxing via agent.YoloFlag.
	PermissionYOLO = "yolo"
)

// Agent is how a node launches one agent: its command, the flags and
// environment every session of it gets, and named profiles that add their
// own on top.
type Agent struct {
	Command  []string          `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Profiles []Profile         `json:"profiles,omitempty"`
	// Model is the default model its sessions start on; "" leaves the agent's own default.
	Model string `json:"model,omitempty"`
	// Effort is the default reasoning effort its sessions start with; "" leaves the agent's own default.
	Effort string `json:"effort,omitempty"`
	// Permission is the startup permission mode ("", "manual", "acceptEdits",
	// "auto", "plan", "dontAsk", "readOnly", or "yolo").
	Permission string `json:"permission,omitempty"`
	// Yolo skips permission prompts and sandboxing when the agent's driver
	// supports it. Synchronized with Permission == "yolo" for backward compatibility.
	Yolo bool `json:"yolo,omitempty"`
	// Enabled offers the agent to start sessions with, or not; unset, it
	// is offered while its program is installed.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowAgent controls whether AI models/agents may delegate to the default
	// (no-profile) agent configuration via MCP. Nil defaults to true.
	AllowAgent *bool `json:"allow_agent,omitempty"`
	// AllowTemplate controls whether prompt templates may run the default
	// (no-profile) agent configuration via mako.ask / mako.spawn. Nil defaults to true.
	AllowTemplate *bool `json:"allow_template,omitempty"`
}

// AgentAllowed reports whether models/agents may spawn this agent's default configuration via MCP.
func (a Agent) AgentAllowed() bool { return a.AllowAgent == nil || *a.AllowAgent }

// TemplateAllowed reports whether prompt templates may execute this agent's default configuration.
func (a Agent) TemplateAllowed() bool { return a.AllowTemplate == nil || *a.AllowTemplate }

// EffectivePermission returns the agent's resolved startup permission mode,
// honoring explicit Permission, Yolo, and legacy YOLO flags in Args.
func (a Agent) EffectivePermission(name string) string {
	if perm := strings.TrimSpace(a.Permission); perm != "" {
		if perm == PermissionDefault {
			return ""
		}
		return perm
	}
	if a.Yolo {
		return PermissionYOLO
	}
	if _, lifted := LiftYoloArgs(name, a.Args); lifted {
		return PermissionYOLO
	}
	return ""
}

// EffectiveYolo reports whether agent name runs in YOLO mode, honoring
// Permission, the explicit Yolo switch, and legacy standalone YOLO flags in Args.
func (a Agent) EffectiveYolo(name string) bool {
	return a.EffectivePermission(name) == PermissionYOLO
}

// Profile is a named set of flags and environment for an agent, such as a
// claude that runs another model or with another account's key.
type Profile struct {
	Name string `json:"name"`
	// Description explains when to choose this profile, including its
	// specialties. It is discovery metadata, not a startup prompt.
	Description string   `json:"description,omitempty"`
	Args        []string `json:"args,omitempty"`
	// ReplaceArgs replaces the agent's base Args with this profile's Args
	// instead of appending after them.
	ReplaceArgs bool              `json:"replace_args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	// Model is the model its sessions start on, by the name the agent's
	// own model flag takes; "" leaves the agent's default. An agent mako
	// starts no model of its own on (agent.ModelFlag) ignores it.
	Model string `json:"model,omitempty"`
	// Effort is the reasoning effort its sessions start with; "" leaves the
	// agent's default. An agent without agent.EffortFlag ignores it.
	Effort string `json:"effort,omitempty"`
	// Permission overrides the agent's startup permission mode when non-empty
	// ("default" resets to the agent's built-in default without YOLO; "yolo"
	// enables YOLO; driver modes select that mode).
	Permission string `json:"permission,omitempty"`
	// Yolo overrides the agent's YOLO switch for this profile when set.
	Yolo *bool `json:"yolo,omitempty"`
	// ContextServers picks the MCP servers its sessions get, by ID, an
	// empty list none; unset, they get every server enabled. A server
	// picked here is given even when turned off for the node.
	ContextServers *[]string `json:"context_servers,omitempty"`
	// AllowAgent controls whether AI models/agents may delegate to this profile
	// via MCP (spawn_agent / list_agent_profiles). Nil defaults to true.
	AllowAgent *bool `json:"allow_agent,omitempty"`
	// AllowTemplate controls whether prompt templates may run this profile via
	// mako.ask / mako.spawn. Nil defaults to true.
	AllowTemplate *bool `json:"allow_template,omitempty"`
}

// AgentAllowed reports whether models/agents may spawn this profile via MCP.
func (p Profile) AgentAllowed() bool { return p.AllowAgent == nil || *p.AllowAgent }

// TemplateAllowed reports whether prompt templates may execute this profile.
func (p Profile) TemplateAllowed() bool { return p.AllowTemplate == nil || *p.AllowTemplate }

// EffectivePermission returns the profile's resolved startup permission mode
// ("" for the agent's built-in default without extra flags, "yolo" for YOLO,
// or a driver-specific mode).
func (p Profile) EffectivePermission(name string, a Agent) string {
	if perm := strings.TrimSpace(p.Permission); perm != "" {
		if perm == PermissionDefault {
			return ""
		}
		return perm
	}
	if p.Yolo != nil {
		if *p.Yolo {
			return PermissionYOLO
		}
		return ""
	}
	if _, lifted := LiftYoloArgs(name, p.Args); lifted {
		return PermissionYOLO
	}
	if p.ReplaceArgs {
		if perm := strings.TrimSpace(a.Permission); perm != "" {
			if perm == PermissionDefault {
				return ""
			}
			return perm
		}
		if a.Yolo {
			return PermissionYOLO
		}
		return ""
	}
	return a.EffectivePermission(name)
}

// EffectiveYolo reports whether profile p of agent name runs in YOLO mode:
// p's explicit permission/Yolo override wins, else any legacy YOLO flag in
// p.Args, else a's.
func (p Profile) EffectiveYolo(name string, a Agent) bool {
	return p.EffectivePermission(name, a) == PermissionYOLO
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

// Builtin holds the default command line for each known terminal agent.
var Builtin = builtin()

func builtin() map[string][]string {
	out := map[string][]string{}
	for _, d := range agents.All() {
		out[d.Info().Name] = d.Info().Command
	}
	return out
}

// ShellName is what a session of the shell is started by: not an agent,
// but configured apart, as Shell, and launched as one.
const ShellName = "shell"

// Shell is how a node starts its shell: its program, empty for the user's
// login shell, and the arguments and environment it adds.
type Shell struct {
	Command []string          `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Agent is the shell as an agent is launched.
func (s Shell) Agent() Agent {
	command := s.Command
	if len(command) == 0 {
		command = []string{DefaultShell()}
	}
	return Agent{Command: command, Args: s.Args, Env: s.Env}
}

// WithShell is agents with shell among them, as ShellName, for a session
// to be started with.
func WithShell(agents map[string]Agent, shell Shell) map[string]Agent {
	out := maps.Clone(agents)
	if out == nil {
		out = map[string]Agent{}
	}
	out[ShellName] = shell.Agent()
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
// and the environment it adds to the daemon's, as the agent's driver
// readies it.
type Launch struct {
	agent.Launch
	// ContextServers are the MCP servers its profile picks, nil for the
	// node's enabled ones.
	ContextServers *[]string
	// Shown is Command as the session reports it, set before the daemon
	// adds its hooks and MCP servers: those are long, and sent with every
	// state. Nil reports Command.
	Shown []string
}

// ResolveOptions overrides a profile's or agent's model, effort, and YOLO
// settings for a single launch.
type ResolveOptions struct {
	Model  string
	Effort string
	Yolo   *bool
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
	return ResolveWith(name, profile, args, env, ResolveOptions{}, agents)
}

// ResolveWith resolves a launch with optional per-launch overrides for model,
// effort, and YOLO mode.
func ResolveWith(name, profile string, args []string, env map[string]string, opts ResolveOptions, configured map[string]Agent) (Launch, error) {
	if name == "" {
		name = ShellName
	}
	a, ok := Lookup(name, configured)
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
	path, err := findProgram(a.Command[0])
	if err != nil {
		return Launch{}, fmt.Errorf("agent %q: %w", name, err)
	}
	var merged map[string]string
	lookup := func(name string) string {
		if v, ok := merged[name]; ok {
			return v
		}
		if agent.IsSessionScopedEnv(name) {
			return ""
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

	effectiveModel := cmp.Or(opts.Model, p.Model, a.Model)
	effectiveEffort := cmp.Or(opts.Effort, p.Effort, a.Effort)
	var yoloDriver agent.YoloFlag
	var permDriver agent.PermissionFlag
	if e, ok := agents.Native(name, path); ok {
		if norm, ok := e.Driver.(agent.ModelEffortNormalizer); ok {
			effectiveModel, effectiveEffort = norm.NormalizeModelEffort(effectiveModel, effectiveEffort)
		}
		if yf, ok := e.Driver.(agent.YoloFlag); ok {
			yoloDriver = yf
		}
		if pf, ok := e.Driver.(agent.PermissionFlag); ok {
			permDriver = pf
		}
	}
	agentArgs, profileArgs := a.Args, p.Args
	if yoloDriver != nil {
		agentArgs, _ = LiftYoloArgs(name, agentArgs)
		profileArgs, _ = LiftYoloArgs(name, profileArgs)
	}
	var effectivePerm string
	if profile != "" {
		effectivePerm = p.EffectivePermission(name, a)
	} else {
		effectivePerm = a.EffectivePermission(name)
	}
	if opts.Yolo != nil {
		if *opts.Yolo {
			effectivePerm = PermissionYOLO
		} else if effectivePerm == PermissionYOLO {
			effectivePerm = ""
		}
	}

	command := append([]string{path}, a.Command[1:]...)
	var combinedArgs []string
	if profile != "" && p.ReplaceArgs {
		combinedArgs = profileArgs
	} else {
		combinedArgs = slices.Concat(agentArgs, profileArgs)
	}
	for _, arg := range combinedArgs {
		command = append(command, expand(arg, lookup))
	}
	command = append(command, modelArgs(name, path, effectiveModel)...)
	command = append(command, effortArgs(name, path, effectiveEffort)...)

	launch := Launch{ContextServers: p.ContextServers,
		Command: command,
		Env:     merged}
	if effectivePerm == PermissionYOLO {
		if yoloDriver != nil {
			yoloDriver.WithYolo(&launch.Launch)
		}
	} else if effectivePerm != "" && permDriver != nil {
		permDriver.WithPermission(&launch.Launch, effectivePerm)
	}
	launch.Command = append(launch.Command, args...)
	return launch, nil
}

// LiftYoloArgs removes known standalone YOLO flags for name from args and
// reports whether any were present.
func LiftYoloArgs(name string, args []string) (cleaned []string, lifted bool) {
	yf, ok := agents.As[agent.YoloFlag](name)
	if !ok {
		return slices.Clone(args), false
	}
	flags := yf.YoloFlags()
	if len(flags) == 0 {
		return slices.Clone(args), false
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if slices.Contains(flags, a) {
			lifted = true
			continue
		}
		out = append(out, a)
	}
	return out, lifted
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

// Installed reports whether the agent's program is there to run: on the
// PATH, or at the path its command names.
func (a Agent) Installed() bool {
	return len(a.Command) > 0 && installed(expand(a.Command[0], os.Getenv))
}

// On reports whether the agent named is offered to start sessions with:
// as its configuration says, else while it is installed.
func On(name string, agents map[string]Agent) bool {
	a, ok := Lookup(name, agents)
	switch {
	case !ok:
		return false
	case a.Enabled != nil:
		return *a.Enabled
	}
	return a.Installed()
}

// Offered lists the agents a node offers to start sessions with, sorted:
// those On. Any of Names may still be started by name.
func Offered(agents map[string]Agent) []string {
	return slices.DeleteFunc(Names(agents), func(name string) bool { return !On(name, agents) })
}

// installedFor is how long a program found, or not, is taken to be so:
// a node lists its agents every few seconds, and one installed shows up
// within this.
const installedFor = 30 * time.Second

// installedSeen is what installed last found, by program and PATH. Its
// key is a struct, not the two joined: a lookup then copies neither, as
// os.Getenv returns a piece of the environment it keeps. A sync.Map would
// box the key on every lookup.
var installedSeen struct {
	sync.Mutex
	m map[installedKey]installedAt
}

type installedKey struct{ program, path string }

type installedAt struct {
	ok bool
	at time.Time
}

// installed reports whether program can be run, looking it up on the PATH
// at most every installedFor.
func installed(program string) bool {
	key := installedKey{program, os.Getenv("PATH")}
	installedSeen.Lock()
	seen, ok := installedSeen.m[key]
	installedSeen.Unlock()
	if ok && time.Since(seen.at) < installedFor {
		return seen.ok
	}
	_, err := exec.LookPath(program)
	installedSeen.Lock()
	if installedSeen.m == nil {
		installedSeen.m = map[installedKey]installedAt{}
	}
	installedSeen.m[key] = installedAt{ok: err == nil, at: time.Now()}
	installedSeen.Unlock()
	return err == nil
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

// Targets lists every target of the agents a node offers: each agent,
// then each of its profiles.
func Targets(agents map[string]Agent) []string {
	var out []string
	for _, name := range Offered(agents) {
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

// DefaultShell is the user's login shell, else the platform's.
func DefaultShell() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// findProgram is where program is, as the agent it runs is installed when
// mako knows it.
func findProgram(program string) (string, error) {
	var dirs []string
	if e, ok := agents.ByProgram(program); ok {
		dirs = e.Info.Dirs
	}
	return agent.Find(program, dirs...)
}

// modelArgs are the flags that start the agent called name, run as
// program, on model, after its own flags and its profile's, which it
// overrides; none for no model, or an agent run through a wrapper of the
// user's, whose flags mako does not know.
func modelArgs(name, program, model string) []string {
	if model == "" {
		return nil
	}
	e, ok := agents.Native(name, program)
	if flag, flags := e.Driver.(agent.ModelFlag); ok && flags {
		return flag.ModelArgs(model)
	}
	return nil
}

// effortArgs are the flags that start the agent called name, run as
// program, with effort, after its own flags, its profile's, and its model.
func effortArgs(name, program, effort string) []string {
	if effort == "" {
		return nil
	}
	e, ok := agents.Native(name, program)
	if flag, flags := e.Driver.(agent.EffortFlag); ok && flags {
		return flag.EffortArgs(effort)
	}
	return nil
}
