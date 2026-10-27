package transcript

import (
	"slices"
	"strings"
)

// Agent is what mako knows of one agent's native conversations: where its
// logs live and how they are written, and the command-line flags that pick
// a conversation. Every conversation rule lives here; launching an agent,
// and which binary is which agent, belong to agentspec and mcp.
type Agent struct {
	Name string

	// The log root is rootEnv when it is set, otherwise homeDir under HOME.
	// Logs sit in the directories logDirs matches under the root.
	rootEnv, homeDir string
	logDirs          []string
	// rollout logs are Codex's: a session_meta record, then response_item
	// records, rather than one record per message.
	rollout bool

	// pin sets a new conversation's ID, so a launch knows it before the
	// agent writes anything; "" when the agent always picks its own.
	pin string
	// resume continues the ID that follows it, and the first is how mako
	// resumes. A name without a dash is a subcommand.
	resume []string
	// fork makes a resumed conversation into a new one.
	fork string
	// pickerValues and pickerFlags are the other options that choose a
	// conversation, with a value and without one.
	pickerValues, pickerFlags []string
	// inputs are startup attachments that, like the prompt, already belong
	// to the history a resumed conversation has.
	inputs []string
	// values and variadic are options a resumed launch keeps along with the
	// value, or the values, that follow them.
	values, variadic []string
	// questions are the tools whose prompt asks the user something rather
	// than for leave: the agent runs its approval hook for them too, but
	// only its own prompt can take the answer, and allowing one would skip
	// it.
	questions []string
	// settings takes settings for one launch, among them hooks; "" when
	// the agent takes none.
	settings string
	// hooks is how the agent takes the hooks mako follows it by; see
	// hooks.go.
	hooks hookStyle
	// Isolate is a flag that keeps the log's writer in the session's process
	// tree, so a session can be bound without guessing by cwd or timestamp.
	// Not every version of the agent has it.
	Isolate string
}

var agents = []*Agent{
	{
		Name: "claude", rootEnv: "CLAUDE_CONFIG_DIR", homeDir: ".claude", logDirs: []string{"projects", "*"},
		pin: "--session-id", resume: []string{"--resume", "-r"}, fork: "--fork-session", settings: "--settings", hooks: settingsHooks,
		questions:    []string{"AskUserQuestion"},
		pickerValues: []string{"--from-pr"}, pickerFlags: []string{"--continue", "-c"},
		values:   strings.Fields("--model --effort --settings --setting-sources --system-prompt --system-prompt-file --append-system-prompt --append-system-prompt-file --permission-mode --permission-prompts --permission-prompt-tool --plugin-dir --plugin-url --agent --agents --name -n --debug-file --input-format --output-format --json-schema --max-budget-usd --max-turns --fallback-model --system-prompt-snapshot --remote-control-session-name-prefix"),
		variadic: strings.Fields("--add-dir --allowedTools --allowed-tools --disallowedTools --disallowed-tools --tools --mcp-config --betas --file"),
	},
	{
		Name: "codex", rootEnv: "CODEX_HOME", homeDir: ".codex", logDirs: []string{"sessions", "*", "*", "*"}, rollout: true,
		resume: []string{"resume"}, fork: "--fork", hooks: configHooks,
		pickerFlags: []string{"--last", "--all"},
		inputs:      []string{"--image", "-i"},
		values:      strings.Fields("--model -m --config -c --profile -p --sandbox -s --ask-for-approval -a --cd -C --add-dir --enable --disable --local-provider --remote --remote-auth-token-env"),
		Isolate:     "--no-daemon",
	},
}

// Lookup finds the agent named name, when mako reads its conversations.
func Lookup(name string) (*Agent, bool) {
	i := slices.IndexFunc(agents, func(a *Agent) bool { return a.Name == name })
	if i < 0 {
		return nil, false
	}
	return agents[i], true
}

// Supported reports whether agent writes a native log this package reads.
func Supported(agent string) bool {
	_, ok := Lookup(agent)
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
	return key == "HOME" || slices.ContainsFunc(agents, func(a *Agent) bool { return a.rootEnv == key })
}

// ArgumentID extracts an explicitly selected native ID. A fork's source
// conversation is not the new process's identity.
func (a *Agent) ArgumentID(args []string) string {
	var pinned, resumed string
	fork := false
	for i, arg := range args {
		name, v, hasValue := strings.Cut(arg, "=")
		if !hasValue && i+1 < len(args) {
			v = args[i+1]
		}
		switch {
		case a.pin != "" && name == a.pin:
			if pinned == "" && validID.MatchString(v) {
				pinned = v
			}
		case a.fork != "" && name == a.fork:
			fork = true
		case slices.Contains(a.resume, name):
			if resumed == "" && validID.MatchString(v) {
				resumed = v
			}
		}
	}
	switch {
	case pinned != "":
		return pinned
	case fork:
		return ""
	}
	return resumed
}

// picker reports whether name chooses a conversation, and whether a value
// follows it.
func (a *Agent) picker(name string) (picks, takesValue bool) {
	switch {
	case name == "":
		return false, false
	case name == a.pin || slices.Contains(a.resume, name) || slices.Contains(a.pickerValues, name):
		return true, true
	case name == a.fork || slices.Contains(a.pickerFlags, name):
		return true, false
	}
	return false, false
}

// Picks reports whether command already chooses which conversation it
// starts or continues.
func (a *Agent) Picks(command []string) bool {
	return slices.ContainsFunc(command[1:], func(arg string) bool {
		name, _, _ := strings.Cut(arg, "=")
		picks, _ := a.picker(name)
		return picks
	})
}

// Pin starts command as conversation id, an ID chosen before the agent
// writes anything. It reports false for an agent that picks its own.
func (a *Agent) Pin(command []string, id string) ([]string, bool) {
	if a.pin == "" {
		return command, false
	}
	return append(command, a.pin, id), true
}

// Resume is command continuing conversation id. It keeps runtime settings
// but drops the original prompt and old conversation pickers: replaying the
// prompt would start the work twice.
func (a *Agent) Resume(command []string, id string) []string {
	args := []string{command[0], a.resume[0], id}
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if arg == "--" {
			break
		}
		name, _, equals := strings.Cut(arg, "=")
		// values counts the arguments after arg that are its values: up to
		// max of them, or all when max < 0, stopping at the next flag.
		values := func(max int) int {
			n := 0
			for !equals && n != max && i+n+1 < len(command) && !strings.HasPrefix(command[i+n+1], "-") {
				n++
			}
			return n
		}
		switch picks, takesValue := a.picker(name); {
		case slices.Contains(a.inputs, name):
			i += values(-1)
		case picks && takesValue:
			i += values(1)
		case picks, !strings.HasPrefix(arg, "-"):
		case slices.Contains(a.variadic, name):
			n := values(-1)
			args = append(args, command[i:i+n+1]...)
			i += n
		case !equals && slices.Contains(a.values, name) && i+1 < len(command):
			args = append(args, arg, command[i+1])
			i++
		default:
			args = append(args, arg)
		}
	}
	return args
}

// Asks reports whether the agent's tool asks the user a question rather
// than for an approval.
func (a *Agent) Asks(tool string) bool { return slices.Contains(a.questions, tool) }
