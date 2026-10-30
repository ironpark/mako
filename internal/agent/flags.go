package agent

import (
	"slices"
	"strings"
)

// Flags is a Launcher from a table of an agent's options: a driver whose
// command line picks conversations by options alone fills one in.
type Flags struct {
	// PromptFlag takes an interactive startup prompt; empty for a positional prompt.
	PromptFlag string
	// PinFlag sets a new conversation's ID, so a launch knows it before the
	// agent writes anything; "" when the agent always picks its own.
	PinFlag string
	// ResumeFlags continue the ID that follows them, and the first is how
	// mako resumes. A name without a dash is a subcommand.
	ResumeFlags []string
	// ForkFlag makes a resumed conversation into a new one.
	ForkFlag string
	// PickerValues and PickerFlags are the other options that choose a
	// conversation, with a value and without one.
	PickerValues, PickerFlags []string
	// Inputs are startup attachments that, like the prompt, already belong
	// to the history a resumed conversation has.
	Inputs []string
	// Values and Variadic are options a resumed launch keeps along with the
	// value, or the values, that follow them.
	Values, Variadic []string
	// IsolateFlag is Launcher.Isolate.
	IsolateFlag string
}

func (f *Flags) PromptArgs(text string) []string {
	if f.PromptFlag != "" {
		return []string{f.PromptFlag, text}
	}
	return []string{text}
}

// ArgumentID extracts an explicitly selected native ID. A fork's source
// conversation is not the new process's identity.
func (f *Flags) ArgumentID(args []string) string {
	var pinned, resumed string
	fork := false
	for i, arg := range args {
		name, v, hasValue := strings.Cut(arg, "=")
		if !hasValue && i+1 < len(args) {
			v = args[i+1]
		}
		switch {
		case f.PinFlag != "" && name == f.PinFlag:
			if pinned == "" && ValidID.MatchString(v) {
				pinned = v
			}
		case f.ForkFlag != "" && name == f.ForkFlag:
			fork = true
		case slices.Contains(f.ResumeFlags, name):
			if resumed == "" && ValidID.MatchString(v) {
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
func (f *Flags) picker(name string) (picks, takesValue bool) {
	switch {
	case name == "":
		return false, false
	case name == f.PinFlag || slices.Contains(f.ResumeFlags, name) || slices.Contains(f.PickerValues, name):
		return true, true
	case name == f.ForkFlag || slices.Contains(f.PickerFlags, name):
		return true, false
	}
	return false, false
}

// Picks reports whether command already chooses which conversation it
// starts or continues.
func (f *Flags) Picks(command []string) bool {
	return slices.ContainsFunc(command[1:], func(arg string) bool {
		name, _, _ := strings.Cut(arg, "=")
		picks, _ := f.picker(name)
		return picks
	})
}

// Pin starts command as conversation id, an ID chosen before the agent
// writes anything. It reports false for an agent that picks its own.
func (f *Flags) Pin(command []string, id string) ([]string, bool) {
	if f.PinFlag == "" {
		return command, false
	}
	return append(command, f.PinFlag, id), true
}

// Resume is command continuing conversation id. It keeps runtime settings
// but drops the original prompt and old conversation pickers: replaying the
// prompt would start the work twice.
func (f *Flags) Resume(command []string, id string) []string {
	args := []string{command[0], f.ResumeFlags[0], id}
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
		switch picks, takesValue := f.picker(name); {
		case slices.Contains(f.Inputs, name):
			i += values(-1)
		case picks && takesValue:
			i += values(1)
		case picks, !strings.HasPrefix(arg, "-"):
		case slices.Contains(f.Variadic, name):
			n := values(-1)
			args = append(args, command[i:i+n+1]...)
			i += n
		case !equals && slices.Contains(f.Values, name) && i+1 < len(command):
			args = append(args, arg, command[i+1])
			i++
		default:
			args = append(args, arg)
		}
	}
	return args
}

// Isolate is IsolateFlag.
func (f *Flags) Isolate() string { return f.IsolateFlag }
