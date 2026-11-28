package policy

import (
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// Call is one tool call an agent asks approval for, as rules read it.
type Call struct {
	Agent string // the agent's name, as agentspec names it
	Tool  string // the tool's name, as the agent calls it
	Kind  string // what the call does, one of protocol's Tool kinds; "" for none known
	// Input is the call's arguments, whole: in the form protocol gives
	// Kind, or as the agent wrote them for a call of no kind.
	Input map[string]any
}

// toolKinds are the kinds of call a rule's tool names, whatever each agent
// calls its tools, by the name in lower case: a kind's own name, and the
// name Claude Code gives its tool of the kind. As in Claude Code's own
// permission rules, Edit names every kind that changes a file and Read
// every kind that reads them, so a rule for Bash or Edit holds for Codex's
// exec_command and apply_patch, agy's run_command and replace_file_content,
// and OpenCode's interactive_bash too.
var toolKinds = map[string][]string{
	"bash":                 {protocol.ToolShell},
	protocol.ToolShell:     {protocol.ToolShell},
	"edit":                 {protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolPatch},
	protocol.ToolMultiEdit: {protocol.ToolMultiEdit},
	protocol.ToolWrite:     {protocol.ToolWrite},
	protocol.ToolPatch:     {protocol.ToolPatch},
	"apply_patch":          {protocol.ToolPatch},
	"read":                 {protocol.ToolRead, protocol.ToolSearch},
	protocol.ToolSearch:    {protocol.ToolSearch},
	"grep":                 {protocol.ToolSearch},
	"glob":                 {protocol.ToolSearch},
	"webfetch":             {protocol.ToolFetch},
	protocol.ToolFetch:     {protocol.ToolFetch},
	protocol.ToolWebSearch: {protocol.ToolWebSearch},
	protocol.ToolTask:      {protocol.ToolTask},
	"agent":                {protocol.ToolTask},
}

// patternMatches reports whether r's pattern holds for call. A deny rule
// holds when its pattern matches anything the call acts on; an allow rule
// only when it matches everything the call acts on, so a call cannot carry
// more than the rule vouches for past it.
func patternMatches(r Rule, call Call) bool {
	if r.Action == ActionDeny {
		return slices.ContainsFunc(denySubjects(call), func(s string) bool { return matchGlobFold(r.Pattern, s) })
	}
	subjects, ok := allowSubjects(call)
	return ok && len(subjects) > 0 && !slices.ContainsFunc(subjects, func(s string) bool { return !matchGlobFold(r.Pattern, s) })
}

// denySubjects are what a deny pattern is matched against: everything the
// call acts on, and for a command, the whole line and each command in it.
func denySubjects(call Call) []string {
	subjects := targets(call)
	if call.Kind == protocol.ToolShell {
		for _, line := range slices.Clone(subjects) {
			cmds, _ := shellCommands(line)
			subjects = append(subjects, cmds...)
		}
	}
	return subjects
}

// allowSubjects are what an allow pattern must match, each of them: for a
// command, each command the line runs. ok is false for a call an allow
// pattern cannot vouch for, as a line that substitutes or redirects.
func allowSubjects(call Call) (subjects []string, ok bool) {
	subjects = targets(call)
	if call.Kind != protocol.ToolShell {
		return subjects, true
	}
	var cmds []string
	for _, line := range subjects {
		c, simple := shellCommands(line)
		if !simple {
			return nil, false
		}
		cmds = append(cmds, c...)
	}
	return cmds, true
}

// targets are what a call acts on, untruncated: the command it runs, every
// path it touches, the URL it fetches. A call of no kind known, or of one
// whose fields are missing, acts on every string its arguments hold.
func targets(call Call) []string {
	in := call.Input
	var out []string
	switch call.Kind {
	case protocol.ToolShell:
		out = present(str(in, "command"))
	case protocol.ToolPatch:
		out = patchPaths(str(in, "patch"))
	case protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolRead:
		out = present(str(in, "path"))
	case protocol.ToolSearch:
		out = present(str(in, "path"), str(in, "glob"))
	case protocol.ToolFetch:
		out = present(str(in, "url"))
	case protocol.ToolWebSearch:
		out = present(str(in, "query"))
	case protocol.ToolTask:
		out = present(str(in, "description"), str(in, "prompt"))
	}
	if len(out) == 0 {
		out = stringsIn(in, nil)
	}
	return out
}

// patchPaths are the files a patch in Codex's format adds, updates,
// deletes or moves to.
func patchPaths(patch string) []string {
	var paths []string
	for line := range strings.Lines(patch) {
		line = strings.TrimSpace(line)
		for _, op := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"} {
			if p, ok := strings.CutPrefix(line, op); ok {
				paths = append(paths, present(p)...)
			}
		}
	}
	return paths
}

// shellCommands splits a command line into the commands it runs: at
// newlines, ;, &, | and the edges of subshells and substitutions, outside
// quotes, each with its whitespace collapsed. simple is false for a line
// that holds more than a list of plain commands: a command or process
// substitution, a subshell, a redirection, or a quote left open. An allow
// pattern cannot vouch for such a line, though a deny pattern still reads
// the commands found in it.
func shellCommands(line string) (cmds []string, simple bool) {
	simple = true
	var cur strings.Builder
	cut := func() {
		if c := strings.Join(strings.Fields(cur.String()), " "); c != "" {
			cmds = append(cmds, c)
		}
		cur.Reset()
	}
	var quote rune
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			// Outside single quotes the next rune is taken as it is.
			cur.WriteRune(c)
			if i+1 < len(rs) {
				i++
				c = rs[i]
			}
		case c == '`' || c == '$' && i+1 < len(rs) && rs[i+1] == '(':
			// A substitution runs a command of its own, in double quotes too.
			simple = false
			cut()
			if c == '$' {
				i++
			}
			continue
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == ')':
			simple = false
			cut()
			continue
		case c == '<' || c == '>':
			simple = false
		case c == '\n' || c == ';' || c == '&' || c == '|':
			cut()
			continue
		}
		cur.WriteRune(c)
	}
	if quote != 0 {
		simple = false
	}
	cut()
	return cmds, simple
}

// str is in[key] when it is a string.
func str(in map[string]any, key string) string {
	s, _ := in[key].(string)
	return s
}

// present are those of ss that are not blank, trimmed.
func present(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// stringsIn appends to out every string v holds, at any depth, a map's in
// the order of its keys.
func stringsIn(v any, out []string) []string {
	switch v := v.(type) {
	case string:
		out = append(out, present(v)...)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = stringsIn(v[k], out)
		}
	case []any:
		for _, e := range v {
			out = stringsIn(e, out)
		}
	}
	return out
}
